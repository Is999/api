package config

import (
	"math"
	"strings"
	"testing"

	appconfig "api/internal/config"
	"api/internal/svc"
	"api/internal/types"

	yaml "go.yaml.in/yaml/v2"
)

// TestConfigReloadItemsKeepsZeroAndFalse 验证真实查询保留关闭值，同时压缩空数组并隐藏敏感原文。
func TestConfigReloadItemsKeepsZeroAndFalse(t *testing.T) {
	cfg := appconfig.Config{
		MySQL: appconfig.MySQLConfig{ReadDataSources: []string{}},
		Redis: appconfig.RedisConfig{Password: "test-password"},
		Auth:  appconfig.AuthConfig{PasswordMinLength: 8},
	}
	logicObj := NewSystemLogic(t.Context(), svc.NewServiceContext(cfg, "test", svc.Dependencies{}))
	result := logicObj.ConfigReloadItems(&types.ConfigItemQueryReq{})
	data, ok := result.Data.(*types.ConfigItemQueryResp)
	if !ok {
		t.Fatalf("配置项查询响应无效: %#v", result)
	}
	var snapshot map[string]any
	if err := yaml.Unmarshal([]byte(data.SnapshotYAML), &snapshot); err != nil {
		t.Fatal(err)
	}
	mysql, ok := snapshot["mysql"].(map[any]any)
	if !ok || mysql["max_open_conns"] != 0 || mysql["debug"] != false {
		t.Fatalf("快照必须保留数字 0 和 false: %v", mysql)
	}
	// 显式空数组与敏感字段的 null 展示不同，此处只断言容器压缩。
	if _, exists := mysql["read_data_sources"]; exists {
		t.Fatal("空数组应从 YAML 展示中省略")
	}
	redisConfig, ok := snapshot["redis"].(map[any]any)
	if !ok || redisConfig["password"] != "te****rd" || strings.Contains(data.SnapshotYAML, cfg.Redis.Password) {
		t.Fatal("配置快照不得包含密码原文")
	}
	var runtimeSnapshot map[string]any
	if err := yaml.Unmarshal([]byte(data.RuntimeYAML), &runtimeSnapshot); err != nil {
		t.Fatal(err)
	}
	auth, ok := runtimeSnapshot["auth"].(map[any]any)
	if !ok || auth["register_enabled"] != false || auth["profile_cache_ttl_seconds"] != 0 {
		t.Fatalf("已展示的运行期配置段必须保留 0 和 false: %v", auth)
	}
	if _, exists := runtimeSnapshot["hot_reload"]; exists {
		t.Fatal("整体零值配置段仍应省略")
	}
}

// TestBuildMaskedConfigViewMasksSensitiveValues 确保配置视图同时隐藏密钥、网络地址和映射路径。
func TestBuildMaskedConfigViewMasksSensitiveValues(t *testing.T) {
	// 输入覆盖密钥、IPv4、IPv6、CIDR 以及地址映射的键和值。
	cfg := appconfig.Config{
		AppID:        "site-a",
		AppKey:       "app-secret-value",
		JwtSecret:    "jwt-secret-value",
		JwtExpiresIn: 3600,
		TrustedProxies: []string{
			"::1",
			"fd00::/8",
		},
		Redis: appconfig.RedisConfig{
			Addrs: []string{"127.0.0.1:6379"},
			AddrMap: map[string]string{
				"10.23.45.67:6379": "redis.internal.example:6379",
			},
			Password: "redis-password",
			PoolSize: 8,
		},
		Ops: appconfig.OpsConfig{
			ConfigReloadToken:      "ops-token-value",
			ConfigReloadAllowedIPs: []string{"2001:db8::/32"},
		},
	}
	view, err := buildMaskedConfigView(cfg)
	if err != nil {
		t.Fatalf("buildMaskedConfigView() error = %v", err)
	}
	// YAML 值和扁平路径都不得泄露原始网络或密钥信息。
	snapshot := view.snapshotYAML
	for _, secret := range []string{
		"app-secret-value", "jwt-secret-value", "redis-password", "ops-token-value", "127.0.0.1:6379",
		"10.23.45.67:6379", "redis.internal.example:6379", "::1", "fd00::/8", "2001:db8::/32",
	} {
		if strings.Contains(snapshot, secret) {
			t.Fatalf("脱敏快照泄露敏感值 %q: %s", secret, snapshot)
		}
	}
	for _, item := range view.items {
		if strings.Contains(item.Path, "10.23.45.67:6379") {
			t.Fatalf("脱敏配置路径泄露 addr_map 原始地址: %#v", item)
		}
	}
	if view.sensitiveTotal == 0 {
		t.Fatal("期望至少识别出一个敏感配置项")
	}
}

// TestBuildMaskedConfigViewKeepsStableOrder 验证配置分页、分组和地址脱敏序号不受 map 遍历顺序影响。
func TestBuildMaskedConfigViewKeepsStableOrder(t *testing.T) {
	cfg := appconfig.Config{Redis: appconfig.RedisConfig{AddrMap: map[string]string{
		"10.0.0.2:6379": "redis-b.internal:6379",
		"10.0.0.1:6379": "redis-a.internal:6379",
	}}}
	var previousYAML string
	for range 10 {
		view, err := buildMaskedConfigView(cfg)
		if err != nil {
			t.Fatal(err)
		}
		for index := 1; index < len(view.items); index++ {
			if view.items[index-1].Path >= view.items[index].Path {
				t.Fatalf("配置路径未递增: %q >= %q", view.items[index-1].Path, view.items[index].Path)
			}
		}
		for index := 1; index < len(view.sections); index++ {
			if view.sections[index-1].Name >= view.sections[index].Name {
				t.Fatalf("配置分组未递增: %q >= %q", view.sections[index-1].Name, view.sections[index].Name)
			}
		}
		if previousYAML != "" && view.snapshotYAML != previousYAML {
			t.Fatal("同一配置重复查询产生不同脱敏 YAML")
		}
		previousYAML = view.snapshotYAML
	}
}

// TestIsConfigAddressLikeMasksIPFamilies 验证地址值识别同时覆盖 IPv4、IPv6 和 CIDR，且普通文案不会被误判。
func TestIsConfigAddressLikeMasksIPFamilies(t *testing.T) {
	cases := []struct {
		value string // value 是配置叶子原始文本。
		want  bool   // want 表示该值是否承载可定位网络拓扑。
	}{
		{value: "127.0.0.1", want: true},
		{value: "::1", want: true},
		{value: "[2001:db8::1]", want: true},
		{value: "fd00::/8", want: true},
		{value: "10.0.0.0/8", want: true},
		{value: "公开配置说明", want: false},
	}
	for _, tt := range cases {
		if got := isConfigAddressLike(tt.value); got != tt.want {
			t.Fatalf("isConfigAddressLike(%q) = %t, want %t", tt.value, got, tt.want)
		}
	}
}

// TestPaginateConfigItemsRejectsOverflowingPage 校验极大页码不会整数溢出后回读首批数据。
func TestPaginateConfigItemsRejectsOverflowingPage(t *testing.T) {
	items := []types.ConfigItem{{Path: "app_id"}}
	got := paginateConfigItems(items, math.MaxInt, 100)
	if len(got) != 0 {
		t.Fatalf("期望极大页码返回空列表，实际 %#v", got)
	}
}

// TestPaginateConfigItemsEmptyDefaultPage 验证空结果使用默认页大小时直接返回空列表。
func TestPaginateConfigItemsEmptyDefaultPage(t *testing.T) {
	// HTTP 已归一化页大小；这里只验证分页辅助函数保留的缺省参数分支。
	if got := paginateConfigItems(nil, 1, 0); len(got) != 0 {
		t.Fatalf("empty page = %#v", got)
	}
}

// TestBuildMaskedRuntimeYAMLOnlyIncludesRuntimeSections 只展示允许外置的配置段；外置不表示所有字段都可热加载。
func TestBuildMaskedRuntimeYAMLOnlyIncludesRuntimeSections(t *testing.T) {
	cfg := appconfig.Config{
		JwtSecret: "jwt-secret-value",
		Auth: appconfig.AuthConfig{
			RegisterEnabled: true,
		},
		HotReload: appconfig.HotReloadConfig{
			Enabled:              true,
			CheckIntervalSeconds: 5,
		},
		MySQL: appconfig.MySQLConfig{
			WriteDataSource: "user:pass@tcp(127.0.0.1:3306)/api",
		},
	}
	yamlText, err := buildMaskedRuntimeYAML(cfg)
	if err != nil {
		t.Fatalf("buildMaskedRuntimeYAML() error = %v", err)
	}
	if !strings.Contains(yamlText, "auth:") || !strings.Contains(yamlText, "hot_reload:") {
		t.Fatalf("运行期 YAML 缺少外置配置段: %s", yamlText)
	}
	if strings.Contains(yamlText, "mysql:") || strings.Contains(yamlText, "jwt_secret") {
		t.Fatalf("运行期 YAML 不应包含启动期配置段: %s", yamlText)
	}
}
