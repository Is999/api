package configload

import (
	"reflect"
	"strings"
	"testing"

	"api/internal/config"
)

// TestDetectReloadRestartImpact 验证运行期字段热加载不会误触发重启，监听端口变化必须提示重启。
func TestDetectReloadRestartImpact(t *testing.T) {
	oldCfg := config.Config{
		HotReload: config.HotReloadConfig{
			Enabled:              true,
			CheckIntervalSeconds: 5,
		},
	}
	newCfg := oldCfg
	newCfg.HotReload.CheckIntervalSeconds = 10

	restartRequired, reason := DetectReloadRestartImpact(oldCfg, newCfg)
	if restartRequired || reason != "" {
		t.Fatalf("runtime config should not require restart: restart=%v reason=%q", restartRequired, reason)
	}

	newCfg.Port = oldCfg.Port + 1
	restartRequired, reason = DetectReloadRestartImpact(oldCfg, newCfg)
	if !restartRequired || reason == "" {
		t.Fatalf("port change should require restart: restart=%v reason=%q", restartRequired, reason)
	}
}

// TestHotReloadRestartSpecsValid 确保热加载重启边界规格完整且顺序稳定。
func TestHotReloadRestartSpecsValid(t *testing.T) {
	// 期望顺序同时约束接口展示和日志中的重启原因。
	specs := hotReloadRestartSpecs()
	wantReasons := []string{
		"HTTP服务配置变更",
		"应用ID变更",
		"应用密钥变更",
		"实例标识变更",
		"可信代理配置变更",
		"JWT认证配置变更",
		"安全链路配置变更",
		"Collector配置变更",
		"雪花ID worker 配置变更",
		"用户写入分表路由配置变更",
		"MySQL连接配置变更",
		"Redis连接配置变更",
		"可观测性配置变更",
		"Lark告警配置变更",
	}
	if len(specs) != len(wantReasons) {
		t.Fatalf("热加载重启边界数量不符合预期: got=%d want=%d", len(specs), len(wantReasons))
	}
	// 每项必须同时提供变化判断和旧值保留逻辑，原因不得重复。
	seen := make(map[string]struct{}, len(specs))
	for index, spec := range specs {
		if spec.Reason != wantReasons[index] {
			t.Fatalf("热加载重启边界顺序不符合预期: index=%d got=%s want=%s", index, spec.Reason, wantReasons[index])
		}
		if spec.Changed == nil {
			t.Fatalf("热加载重启边界缺少变化判断: %s", spec.Reason)
		}
		if spec.Preserve == nil {
			t.Fatalf("热加载重启边界缺少原值保留逻辑: %s", spec.Reason)
		}
		if _, ok := seen[spec.Reason]; ok {
			t.Fatalf("热加载重启边界重复: %s", spec.Reason)
		}
		seen[spec.Reason] = struct{}{}
	}
}

// TestBuildReloadEffectiveConfigPreservesRestartOnlyFields 确保待重启字段保留原值，运行期字段仍可刷新。
func TestBuildReloadEffectiveConfigPreservesRestartOnlyFields(t *testing.T) {
	// 旧快照覆盖所有启动期边界，并保留一个可在线更新的认证参数。
	oldCfg := config.Config{
		AppID:      "old-app",
		AppKey:     "old-app-key",
		InstanceID: "old-instance",
		TrustedProxies: []string{
			"127.0.0.1/32",
		},
		JwtSecret:    "old-jwt-secret",
		JwtExpiresIn: 3600,
		Auth: config.AuthConfig{
			Issuer:                 "old-issuer",
			ProfileCacheTTLSeconds: 300,
		},
		Security: config.SecurityConfig{
			SecretKey: config.SecuritySecretKeyConfig{
				StableVersion: "old-v1",
				SignStatus:    1,
				Versions:      []config.SecuritySecretKeyVersionConfig{{KeyVersion: "old-v1"}},
			},
		},
		Collector: config.CollectorConfig{
			Enabled: true,
			Kafka:   config.CollectorKafkaConfig{Brokers: []string{"old-kafka:9092"}},
		},
		MySQL: config.MySQLConfig{
			WriteDataSource: "old-write",
			MaxOpenConns:    10,
		},
		SiteMySQL: config.SiteMySQLConfig{
			"site": {WriteDataSource: "old-site"},
		},
		Redis: config.RedisConfig{
			Addrs: []string{"127.0.0.1:6379"},
		},
		Snowflake: config.SnowflakeConfig{
			WorkerID: int64Ptr(1),
		},
		User: config.UserConfig{
			RouteShardCount: 1,
		},
		Observability: config.ObservabilityConfig{
			ServiceName:  "old-service",
			Environment:  "dev",
			OTLPEndpoint: "old-collector:4317",
			OTLPProtocol: "grpc",
		},
	}
	oldCfg.Host = "127.0.0.1"
	oldCfg.Port = 8890
	oldCfg.Mode = "dev"

	// 新快照同时修改启动期字段和运行期字段，验证二者分流。
	newCfg := oldCfg
	newCfg.AppID = "new-app"
	newCfg.AppKey = "new-app-key"
	newCfg.InstanceID = "new-instance"
	newCfg.TrustedProxies = []string{"10.0.0.0/8"}
	newCfg.JwtSecret = "new-jwt-secret"
	newCfg.JwtExpiresIn = 7200
	newCfg.Auth.Issuer = "new-issuer"
	newCfg.Auth.ProfileCacheTTLSeconds = 600
	newCfg.Security.SecretKey.StableVersion = "new-v2"
	newCfg.Security.SecretKey.Versions = []config.SecuritySecretKeyVersionConfig{{KeyVersion: "new-v2"}}
	newCfg.Collector.Kafka.Brokers = []string{"new-kafka:9092"}
	newCfg.Host = "0.0.0.0"
	newCfg.Port = 8891
	newCfg.Mode = "pro"
	newCfg.Snowflake.WorkerID = int64Ptr(2)
	newCfg.User.RouteShardCount = 2
	newCfg.MySQL = config.MySQLConfig{WriteDataSource: "new-write", MaxOpenConns: 20}
	newCfg.SiteMySQL = config.SiteMySQLConfig{"site": {WriteDataSource: "new-site"}}
	newCfg.Redis = config.RedisConfig{Addrs: []string{"127.0.0.1:6380"}}
	newCfg.Observability.ServiceName = "new-service"
	newCfg.Observability.Environment = "pro"
	newCfg.Observability.OTLPEndpoint = "new-collector:4317"
	newCfg.Observability.OTLPProtocol = "http"
	oldCfg.Alert.Lark.Enabled = false
	newCfg.Alert.Lark.Enabled = true
	newCfg.Alert.Lark.WebhookURL = "https://open.larksuite.com/open-apis/bot/v2/hook/test"

	// 生效快照必须保留所有启动期旧值，仅刷新明确支持热加载的字段。
	effective := BuildReloadEffectiveConfig(oldCfg, newCfg)
	if effective.Host != oldCfg.Host || effective.Port != oldCfg.Port || effective.Mode != oldCfg.Mode {
		t.Fatalf("期望 HTTP 服务配置保持原值，实际 host=%s port=%d mode=%s", effective.Host, effective.Port, effective.Mode)
	}
	if effective.MySQL.WriteDataSource != oldCfg.MySQL.WriteDataSource {
		t.Fatalf("期望 MySQL 保持原值，实际为 %+v", effective.MySQL)
	}
	if effective.SiteMySQL["site"].WriteDataSource != oldCfg.SiteMySQL["site"].WriteDataSource {
		t.Fatalf("期望 SiteMySQL 保持原值，实际为 %+v", effective.SiteMySQL)
	}
	if effective.Redis.Addrs[0] != oldCfg.Redis.Addrs[0] {
		t.Fatalf("期望 Redis 保持原值，实际为 %+v", effective.Redis)
	}
	if effective.Snowflake.WorkerID == nil || *effective.Snowflake.WorkerID != *oldCfg.Snowflake.WorkerID {
		t.Fatalf("期望雪花 worker_id 保持原值，实际为 %+v", effective.Snowflake)
	}
	if effective.User.RouteShardCount != oldCfg.User.RouteShardCount {
		t.Fatalf("期望用户写入分表路由保持原值，实际为 %+v", effective.User)
	}
	if !reflect.DeepEqual(effective.Observability, oldCfg.Observability) {
		t.Fatalf("期望可观测性配置整体保持原值，实际为 %+v", effective.Observability)
	}
	if effective.Observability.Environment != oldCfg.Observability.Environment {
		t.Fatalf("期望观测环境跟随旧运行模式，实际 environment=%s", effective.Observability.Environment)
	}
	if effective.Alert.Lark.Enabled != oldCfg.Alert.Lark.Enabled || effective.Alert.Lark.WebhookURL != oldCfg.Alert.Lark.WebhookURL {
		t.Fatalf("期望 Lark 告警配置保持原值，实际为 %+v", effective.Alert.Lark)
	}
	if effective.AppID != oldCfg.AppID || effective.AppKey != oldCfg.AppKey {
		t.Fatalf("期望应用身份配置保持原值，实际 app_id=%s app_key=%s", effective.AppID, effective.AppKey)
	}
	if effective.InstanceID != oldCfg.InstanceID || !reflect.DeepEqual(effective.TrustedProxies, oldCfg.TrustedProxies) {
		t.Fatalf("期望实例与可信代理配置保持原值，实际 instance_id=%s trusted_proxies=%v", effective.InstanceID, effective.TrustedProxies)
	}
	if effective.JwtSecret != oldCfg.JwtSecret || effective.JwtExpiresIn != oldCfg.JwtExpiresIn || effective.Auth.Issuer != oldCfg.Auth.Issuer {
		t.Fatalf("期望 JWT 配置保持原值，实际 secret=%s expires=%d issuer=%s", effective.JwtSecret, effective.JwtExpiresIn, effective.Auth.Issuer)
	}
	if effective.Auth.ProfileCacheTTLSeconds != newCfg.Auth.ProfileCacheTTLSeconds {
		t.Fatalf("期望认证运行参数刷新为新值，实际 profile_cache_ttl=%d", effective.Auth.ProfileCacheTTLSeconds)
	}
	if !reflect.DeepEqual(effective.Security, oldCfg.Security) {
		t.Fatalf("期望安全链路配置保持原值，实际为 %+v", effective.Security)
	}
	if !reflect.DeepEqual(effective.Collector, oldCfg.Collector) {
		t.Fatalf("期望 Collector 配置保持原值，实际为 %+v", effective.Collector)
	}
	// 每个被保留的变化都必须出现在重启原因中。
	restartRequired, reason := DetectReloadRestartImpact(oldCfg, newCfg)
	if !restartRequired {
		t.Fatal("期望启动期配置变化提示重启")
	}
	for _, want := range []string{"HTTP服务配置变更", "应用ID变更", "应用密钥变更", "实例标识变更", "可信代理配置变更", "JWT认证配置变更", "安全链路配置变更", "Collector配置变更", "可观测性配置变更"} {
		if !strings.Contains(reason, want) {
			t.Fatalf("重启原因 %q 缺少 %q", reason, want)
		}
	}
}
