package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	i18n "api/common/i18n"
	"api/common/runtimecfg"
	"api/internal/bootstrap/configload"
	"api/internal/config"
	"api/internal/svc"
)

// TestReloadConfigFilePreservesVersionAfterFailure 保证同一非法候选重复提交仍失败且不伪造生效版本。
func TestReloadConfigFilePreservesVersionAfterFailure(t *testing.T) {
	file := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(file, []byte("Name: [\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	svcCtx := svc.NewServiceContext(config.Config{}, "applied", svc.Dependencies{})
	svcCtx.UpdateHotReloadStatus(svc.HotReloadStatus{ConfigVersion: "applied"})
	app := &App{ServiceContext: svcCtx}
	for attempt := range 3 {
		if _, err := app.reloadConfigFile(t.Context(), "manual_api", file, configload.Load); err == nil {
			t.Fatalf("第%d次非法候选被误判成功", attempt+1)
		}
		status := svcCtx.CurrentHotReloadStatus()
		if status.LastStatus != "failed" || status.ConfigVersion != "applied" || svcCtx.CurrentVersion() != "applied" {
			t.Fatalf("失败不能推进生效版本: status=%+v version=%s", status, svcCtx.CurrentVersion())
		}
	}
}

// TestWatchConfigFileLoadsStartupChange 保证装配期间更新的配置在首轮监听中应用。
func TestWatchConfigFileLoadsStartupChange(t *testing.T) {
	initial, err := os.ReadFile(filepath.Join("..", "..", "etc", "config.dnmp.sample.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(file, initial, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, version, _, err := LoadConfig(file)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{ServiceContext: svc.NewServiceContext(cfg, version, svc.Dependencies{})}
	previous := runtimecfg.Get()
	t.Cleanup(func() { runtimecfg.Restore(previous) })
	// 文件先替换，内存仍保留旧快照，模拟数据库和组件装配期间的更新。
	updated := strings.Replace(string(initial), "check_interval_seconds: 5", "check_interval_seconds: 2", 1)
	if updated == string(initial) {
		t.Fatal("样例轮询间隔变化，需更新本用例输入")
	}
	if err := os.WriteFile(file, []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()
	app.watchConfigFile(ctx, file)
	if got := app.ServiceContext.CurrentConfig().HotReload.CheckIntervalSeconds; got != 2 {
		t.Fatalf("首轮未加载新配置，间隔=%d", got)
	}
}

// TestReloadConfigFileSkipsUnchangedSnapshot 验证配置文件未变化时不会重复发布运行时快照。
func TestReloadConfigFileSkipsUnchangedSnapshot(t *testing.T) {
	// 临时文件提供完整可加载配置，后续不再改动其内容。
	dir := t.TempDir()
	configFile := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(configFile, []byte(`
Name: "api"
Host: "127.0.0.1"
Port: 8890
Mode: "dev"
internal_server:
  host: "127.0.0.1"
  port: 8891
app_id: "1"
app_key: "test-app-key-0123456789"
snowflake:
  worker_id: 1
jwt_secret: "test-secret-please-change"
auth:
  password_min_length: 8
hot_reload:
  enabled: false
ops:
  config_reload_token: "test-api-ops-token"
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
		t.Fatalf("WriteFile(config) error = %v", err)
	}
	cfg, version, _, err := LoadConfig(configFile)
	if err != nil {
		t.Fatalf("LoadConfig() error = %v", err)
	}
	svcCtx := svc.NewServiceContext(cfg, version, svc.Dependencies{})
	svcCtx.UpdateHotReloadStatus(svc.HotReloadStatus{
		ConfigVersion: version,
		ReloadCount:   3,
	})
	app := &App{ServiceContext: svcCtx}

	// 预置独立运行快照，用于确认无变化分支没有重复发布。
	prev := runtimecfg.Get()
	runtimecfg.Set(runtimecfg.Snapshot{AppID: "stable-app"})
	t.Cleanup(func() {
		runtimecfg.Restore(prev)
	})
	if _, err = app.reloadConfigFile(context.Background(), "manual_api", configFile, configload.Load); err != nil {
		t.Fatalf("reloadConfigFile() error = %v", err)
	}

	// 无变化只更新说明，不增加次数或覆盖全局运行配置。
	status := svcCtx.CurrentHotReloadStatus()
	if status.ReloadCount != 3 {
		t.Fatalf("配置无变化不应增加 ReloadCount，实际为 %d", status.ReloadCount)
	}
	if status.LastMessage != "配置无变化" {
		t.Fatalf("期望记录配置无变化，实际为 %q", status.LastMessage)
	}
	if status.LastMessageKey != i18n.MsgKeyHotReloadUnchanged {
		t.Fatalf("期望记录配置无变化 key，实际为 %q", status.LastMessageKey)
	}
	if got := runtimecfg.AppID(); got != "stable-app" {
		t.Fatalf("配置无变化不应重复设置 runtimecfg，实际 app_id=%q", got)
	}
}

// TestWatchConfigFileRecoversAfterInitialFingerprintFailure 验证启动时文件短暂缺失不会永久结束 watcher。
func TestWatchConfigFileRecoversAfterInitialFingerprintFailure(t *testing.T) {
	// watcher 在目标文件尚不存在时启动，覆盖首次指纹读取失败。
	dir := t.TempDir()
	configFile := filepath.Join(dir, "config.yaml")
	cfg := config.Config{
		AppID:     "site-a",
		JwtSecret: "test-secret-please-change",
		HotReload: config.HotReloadConfig{Enabled: true, CheckIntervalSeconds: 1},
	}
	svcCtx := svc.NewServiceContext(cfg, "initial-version", svc.Dependencies{})
	app := &App{ServiceContext: svcCtx, ConfigFile: configFile}

	previousRuntime := runtimecfg.Get()
	t.Cleanup(func() {
		runtimecfg.Restore(previousRuntime)
		stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = app.stopConfigHotReload(stopCtx)
	})
	app.startConfigHotReload()

	// 首轮立即读取文件；失败后保留 watcher，等待后续文件恢复。
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && svcCtx.CurrentHotReloadStatus().LastMessageKey != i18n.MsgKeyHotReloadFileStatusReadFailed {
		time.Sleep(10 * time.Millisecond)
	}
	if status := svcCtx.CurrentHotReloadStatus(); !status.Watching || status.LastMessageKey != i18n.MsgKeyHotReloadFileStatusReadFailed {
		t.Fatalf("initial watcher status = %+v, want running fingerprint failure", status)
	}
	// 文件补齐后 watcher 应在下一轮自动加载，无需重新启动。
	if err := os.WriteFile(configFile, []byte(`
Name: "api"
Host: "127.0.0.1"
Port: 8890
Mode: "dev"
internal_server:
  host: "127.0.0.1"
  port: 8891
app_id: "site-a"
app_key: "test-app-key-0123456789"
snowflake:
  worker_id: 1
jwt_secret: "test-secret-please-change"
auth:
  password_min_length: 8
hot_reload:
  enabled: true
  check_interval_seconds: 1
ops:
  config_reload_token: "test-api-ops-token"
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
		t.Fatalf("WriteFile(config) error = %v", err)
	}

	// 有界轮询等待成功状态，避免测试永久阻塞。
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && svcCtx.CurrentHotReloadStatus().LastStatus != "success" {
		time.Sleep(20 * time.Millisecond)
	}
	status := svcCtx.CurrentHotReloadStatus()
	if !status.Watching || status.LastStatus != "success" || status.ConfigVersion == "initial-version" {
		t.Fatalf("recovered watcher status = %+v, want successful reload and continued watching", status)
	}
}
