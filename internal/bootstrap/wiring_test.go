package bootstrap

import (
	"os"
	"path/filepath"
	"testing"

	"api/internal/bootstrap/configload"
	"api/internal/config"
)

// TestLoadConfigSampleRequiresProductionSecrets 验证生产示例配置仍会拒绝占位密钥。
func TestLoadConfigSampleRequiresProductionSecrets(t *testing.T) {
	file := filepath.Join("..", "..", "etc", "config.sample.yaml")
	if _, _, _, err := LoadConfig(file); err == nil {
		t.Fatal("expected production sample with placeholders to be rejected")
	}
}

// TestLoadConfigDNMPSample 验证 DNMP 本地示例配置可加载并生成配置版本。
func TestLoadConfigDNMPSample(t *testing.T) {
	file := filepath.Join("..", "..", "etc", "config.dnmp.sample.yaml")
	cfg, version, securityKeys, err := LoadConfig(file)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.Name == "" {
		t.Fatal("config name should not be empty")
	}
	if version == "" {
		t.Fatal("config version should not be empty")
	}
	if securityKeys != nil {
		t.Fatal("empty security config should not build a key registry")
	}
}

// TestNormalizeConfigUsesModeForObservability 确保观测环境复用顶层 Mode，不维护第二套环境。
func TestNormalizeConfigUsesModeForObservability(t *testing.T) {
	cfg := config.Config{
		Observability: config.ObservabilityConfig{
			Environment: "custom-env",
		},
	}
	cfg.Mode = "pro"

	configload.Normalize(&cfg)

	if cfg.Observability.Environment != "pro" {
		t.Fatalf("期望观测环境复用 Mode，实际为 %q", cfg.Observability.Environment)
	}
}

// TestLoadConfigMergesRuntimeConfigFile 验证主配置可合并外置运行时配置文件。
func TestLoadConfigMergesRuntimeConfigFile(t *testing.T) {
	// 主文件只声明外置路径和启动期配置，运行期字段写入独立文件。
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "config.d"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	mainFile := filepath.Join(dir, "config.yaml")
	runtimeFile := filepath.Join(dir, "config.d", "runtime.yaml")
	if err := os.WriteFile(mainFile, []byte(`
Name: "api"
Host: "0.0.0.0"
Port: 8890
Mode: "dev"
internal_server:
  host: "127.0.0.1"
  port: 8891
app_id: "1"
snowflake:
  worker_id: 1
jwt_secret: "test-secret-please-change"
app_key: "test-app-key-0123456789"
auth:
  password_min_length: 8
hot_reload:
  enabled: false
security:
  secret_key:
    sign_status: 1
    crypto_status: 1
config_files:
  runtime: "config.d/runtime.yaml"
redis:
  type: "single"
  addrs:
    - "127.0.0.1:6379"
  password: ""
  db: 0
  pool_size: 1
mysql:
  write_data_source: "root:pwd@tcp(127.0.0.1:3306)/api"
  max_open_conns: 20
  max_idle_conns: 10
  conn_max_lifetime: 300
  debug: false
`), 0o644); err != nil {
		t.Fatalf("WriteFile(main) error = %v", err)
	}
	if err := os.WriteFile(runtimeFile, []byte(`
auth:
  password_min_length: 12
hot_reload:
  enabled: true
  check_interval_seconds: 9
security:
  secret_key:
    sign_status: 0
    crypto_status: 0
collector:
  enabled: true
  kafka:
    brokers:
      - "127.0.0.1:9092"
  tasks:
    auth.security:
      topic: "api_collector_auth_security_events"
ops:
  config_reload_token: "runtime-api-ops-token"
`), 0o644); err != nil {
		t.Fatalf("WriteFile(runtime) error = %v", err)
	}

	// 加载后逐项验证运行期文件确实覆盖对应配置段。
	cfg, _, _, err := LoadConfig(mainFile)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	if cfg.Auth.PasswordMinLength != 12 {
		t.Fatalf("password_min_length = %d, want 12", cfg.Auth.PasswordMinLength)
	}
	if !cfg.HotReload.Enabled || cfg.HotReload.CheckIntervalSeconds != 9 {
		t.Fatalf("hot_reload config not merged: %+v", cfg.HotReload)
	}
	if cfg.Security.SecretKey.SignStatus != 0 || cfg.Security.SecretKey.CryptoStatus != 0 {
		t.Fatalf("security config not merged: %+v", cfg.Security)
	}
	if !cfg.Collector.Enabled || cfg.Collector.Tasks[config.CollectorBizTypeAuthSecurity].Topic != config.CollectorTopicAuthSecurity {
		t.Fatalf("collector config not merged: %+v", cfg.Collector)
	}
	if cfg.Ops.ConfigReloadToken != "runtime-api-ops-token" {
		t.Fatalf("ops config not merged: %+v", cfg.Ops)
	}
}

// TestConfigBundleFingerprintIncludesRuntimeFile 验证配置包指纹会纳入外置运行时配置文件。
func TestConfigBundleFingerprintIncludesRuntimeFile(t *testing.T) {
	// 主文件保持不变，只修改其引用的运行期文件。
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "config.d"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	mainFile := filepath.Join(dir, "config.yaml")
	runtimeFile := filepath.Join(dir, "config.d", "runtime.yaml")
	if err := os.WriteFile(mainFile, []byte(`
Name: "api"
Host: "0.0.0.0"
Port: 8890
Mode: "dev"
jwt_secret: "test-secret-please-change"
config_files:
  runtime: "config.d/runtime.yaml"
redis:
  type: "single"
  addrs:
    - "127.0.0.1:6379"
  password: ""
  db: 0
  pool_size: 1
`), 0o644); err != nil {
		t.Fatalf("WriteFile(main) error = %v", err)
	}
	if err := os.WriteFile(runtimeFile, []byte("collector:\n  enabled: false\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(runtime first) error = %v", err)
	}
	// 两次指纹之间仅改变运行期内容，结果必须随之变化。
	first, err := configload.BundleFingerprint(mainFile)
	if err != nil {
		t.Fatalf("BundleFingerprint(first) error = %v", err)
	}
	if err := os.WriteFile(runtimeFile, []byte("collector:\n  enabled: true\n"), 0o644); err != nil {
		t.Fatalf("WriteFile(runtime second) error = %v", err)
	}
	second, err := configload.BundleFingerprint(mainFile)
	if err != nil {
		t.Fatalf("BundleFingerprint(second) error = %v", err)
	}
	if first == second {
		t.Fatal("runtime file change should update bundle fingerprint")
	}
}
