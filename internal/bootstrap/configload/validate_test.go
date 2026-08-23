package configload

import (
	"fmt"
	"strings"
	"testing"

	"api/common/idgen"
	"api/internal/config"

	"github.com/zeromicro/go-zero/rest"
)

// TestValidateConfigRejectsWeakJWTSecret 确保明显弱 JWT 密钥不能通过启动校验。
func TestValidateConfigRejectsWeakJWTSecret(t *testing.T) {
	cfg := validBootstrapConfig()
	cfg.JwtSecret = "short"
	if err := Validate(cfg); err == nil {
		t.Fatal("expected weak jwt_secret to be rejected")
	}
}

// TestValidateConfigRejectsMissingPersistentRootKey 确保所有模式在接收联系方式请求前已具备持久数据根密钥。
func TestValidateConfigRejectsMissingPersistentRootKey(t *testing.T) {
	for _, appKey := range []string{"", "short", " app-key-0123456789 ", "app-key-0123456789\n"} {
		cfg := validBootstrapConfig()
		cfg.AppKey = appKey
		if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "app_key") {
			t.Fatalf("期望 app_key=%q 被拒绝，实际为 %v", appKey, err)
		}
	}
}

// TestValidateConfigRejectsJWTWhitespace 确保签发和验签不会因密钥被静默 trim 而形成第二套配置语义。
func TestValidateConfigRejectsJWTWhitespace(t *testing.T) {
	cfg := validBootstrapConfig()
	cfg.JwtSecret = " test-secret-0123456789 "
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "jwt_secret") {
		t.Fatalf("期望包含首尾空白的 jwt_secret 被拒绝，实际为 %v", err)
	}
}

// TestValidateConfigRejectsNonCanonicalMode 确保运行模式只接受配置枚举声明的规范值。
func TestValidateConfigRejectsNonCanonicalMode(t *testing.T) {
	for _, mode := range []string{"", "prod", "production", "PRO", " dev ", "unknown"} {
		cfg := validBootstrapConfig()
		cfg.Mode = mode
		if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "Mode") {
			t.Fatalf("期望 Mode=%q 被拒绝，实际为 %v", mode, err)
		}
	}
}

// TestValidateConfigRejectsNonCanonicalHTTPListener 确保实际监听和启动探测使用同一 Host/Port。
func TestValidateConfigRejectsNonCanonicalHTTPListener(t *testing.T) {
	for _, edit := range []func(*config.Config){
		func(cfg *config.Config) { cfg.Host = " 127.0.0.1 " },
		func(cfg *config.Config) { cfg.Host = "" },
		func(cfg *config.Config) { cfg.Port = 0 },
		func(cfg *config.Config) { cfg.Port = 65536 },
	} {
		cfg := validBootstrapConfig()
		edit(&cfg)
		if err := Validate(cfg); err == nil {
			t.Fatal("expected invalid HTTP listener error")
		}
	}
}

// TestValidateConfigAcceptsDeclaredModes 确保文档声明的五种规范模式均走确定校验分支。
func TestValidateConfigAcceptsDeclaredModes(t *testing.T) {
	for _, mode := range []string{config.ModeDevelopment, config.ModeTest, config.ModeRuntimeTest, config.ModePreRelease} {
		cfg := validBootstrapConfig()
		cfg.Mode = mode
		if err := Validate(cfg); err != nil {
			t.Fatalf("期望 Mode=%q 通过，实际为 %v", mode, err)
		}
	}
	if err := Validate(validProductionBootstrapConfig()); err != nil {
		t.Fatalf("期望生产规范模式通过，实际为 %v", err)
	}
}

// TestValidateConfigRejectsUnsafeJWTExpiry 确保登录态 TTL 不会溢出 duration 或形成超长期令牌。
func TestValidateConfigRejectsUnsafeJWTExpiry(t *testing.T) {
	for _, expiresIn := range []int64{-1, config.MaxJWTExpiresInSeconds + 1} {
		cfg := validBootstrapConfig()
		cfg.JwtExpiresIn = expiresIn
		if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "jwt_expires_in") {
			t.Fatalf("期望 jwt_expires_in=%d 返回边界错误，实际为 %v", expiresIn, err)
		}
	}
	for _, expiresIn := range []int64{1, config.MaxJWTExpiresInSeconds} {
		cfg := validBootstrapConfig()
		cfg.JwtExpiresIn = expiresIn
		if err := Validate(cfg); err != nil {
			t.Fatalf("期望 jwt_expires_in=%d 通过，实际为 %v", expiresIn, err)
		}
	}
}

// TestValidateConfigRejectsUnsafeMySQLPools 确保主库和读副本都使用有界连接池配置。
func TestValidateConfigRejectsUnsafeMySQLPools(t *testing.T) {
	tests := []struct {
		name string               // name 表示连接池错误场景。
		edit func(*config.Config) // edit 注入待验证的配置错误。
		want string               // want 是期望错误字段。
	}{
		{name: "missing write dsn", edit: func(cfg *config.Config) { cfg.MySQL.WriteDataSource = "" }, want: "write_data_source"},
		{name: "unlimited open", edit: func(cfg *config.Config) { cfg.MySQL.MaxOpenConns = 0 }, want: "max_open_conns"},
		{name: "idle over open", edit: func(cfg *config.Config) { cfg.MySQL.MaxIdleConns = cfg.MySQL.MaxOpenConns + 1 }, want: "max_idle_conns"},
		{name: "missing lifetime", edit: func(cfg *config.Config) { cfg.MySQL.ConnMaxLifetime = 0 }, want: "conn_max_lifetime"},
		{name: "too many replicas", edit: func(cfg *config.Config) {
			cfg.MySQL.ReadDataSources = make([]string, config.MaxMySQLReadDataSourceCount+1)
			for index := range cfg.MySQL.ReadDataSources {
				cfg.MySQL.ReadDataSources[index] = fmt.Sprintf("user:password@tcp(127.0.0.1:3306)/replica_%d", index)
			}
		}, want: "read_data_sources"},
		{name: "duplicate replica", edit: func(cfg *config.Config) {
			cfg.MySQL.ReadDataSources = []string{"user:password@tcp(127.0.0.1:3306)/replica", "user:password@tcp(127.0.0.1:3306)/replica"}
		}, want: "重复"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validBootstrapConfig()
			tt.edit(&cfg)
			if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("期望 MySQL 配置返回包含 %q 的错误，实际为 %v", tt.want, err)
			}
		})
	}
}

// TestValidateConfigRejectsUnsafeSiteMySQL 确保命名库数量、名称和连接池都不能绕过主库规则。
func TestValidateConfigRejectsUnsafeSiteMySQL(t *testing.T) {
	for _, name := range []string{" log ", "MAIN", "main", "archive:old"} {
		cfg := validBootstrapConfig()
		cfg.SiteMySQL = config.SiteMySQLConfig{name: validMySQLConfig("archive")}
		if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "site_mysql") {
			t.Fatalf("期望 site_mysql 名称 %q 返回错误，实际为 %v", name, err)
		}
	}
	cfg := validBootstrapConfig()
	cfg.SiteMySQL = config.SiteMySQLConfig{"archive": validMySQLConfig("archive")}
	invalid := cfg.SiteMySQL["archive"]
	invalid.MaxOpenConns = 0
	cfg.SiteMySQL["archive"] = invalid
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "site_mysql.archive.max_open_conns") {
		t.Fatalf("期望命名库无界连接池返回错误，实际为 %v", err)
	}
	many := validBootstrapConfig()
	many.SiteMySQL = make(config.SiteMySQLConfig, maxSiteMySQLCount+1)
	for index := 0; index <= maxSiteMySQLCount; index++ {
		name := fmt.Sprintf("archive_%d", index)
		many.SiteMySQL[name] = validMySQLConfig(name)
	}
	if err := Validate(many); err == nil || !strings.Contains(err.Error(), "site_mysql 不能超过") {
		t.Fatalf("期望过多命名库返回数量上限错误，实际为 %v", err)
	}
}

// TestValidateConfigRejectsInvalidCollectorKafka 确保启用 Collector 时必须配置 Kafka broker。
func TestValidateConfigRejectsInvalidCollectorKafka(t *testing.T) {
	cfg := validBootstrapConfig()
	cfg.Collector = config.CollectorConfig{
		Enabled: true,
		Tasks: map[string]config.CollectorTaskConfig{
			config.CollectorBizTypeAuthSecurity: {Topic: config.CollectorTopicAuthSecurity},
		},
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected collector without kafka brokers to be rejected")
	}
}

// TestValidateConfigRejectsMissingAppID 确保 app_id 缺失时不会落到共享 Redis 默认命名空间。
func TestValidateConfigRejectsMissingAppID(t *testing.T) {
	cfg := validBootstrapConfig()
	cfg.AppID = ""
	if err := Validate(cfg); err == nil {
		t.Fatal("expected missing app_id to be rejected")
	}
}

// TestValidateConfigRejectsInvalidTrustedProxy 确保错误代理网段不会静默退化为错误客户端 IP。
func TestValidateConfigRejectsInvalidTrustedProxy(t *testing.T) {
	cfg := validBootstrapConfig()
	cfg.TrustedProxies = []string{"not-a-cidr"}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected invalid trusted_proxies to be rejected")
	}
}

// TestValidateConfigRejectsNonCanonicalTrustedProxies 确保公共解析器不会静默丢弃空项或合并重复规则。
func TestValidateConfigRejectsNonCanonicalTrustedProxies(t *testing.T) {
	for _, proxies := range [][]string{{""}, {" 10.0.0.0/8"}, {"10.0.0.0/8", "10.0.0.0/8"}} {
		cfg := validBootstrapConfig()
		cfg.TrustedProxies = proxies
		if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "trusted_proxies") {
			t.Fatalf("期望 trusted_proxies=%q 被拒绝，实际为 %v", proxies, err)
		}
	}
}

// TestValidateConfigRejectsNonCanonicalJWTIssuer 确保 token issuer 与启动配置只有精确匹配语义。
func TestValidateConfigRejectsNonCanonicalJWTIssuer(t *testing.T) {
	for _, issuer := range []string{" ", " api "} {
		cfg := validBootstrapConfig()
		cfg.Auth.Issuer = issuer
		if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "auth.issuer") {
			t.Fatalf("期望 auth.issuer=%q 被拒绝，实际为 %v", issuer, err)
		}
	}
}

// TestValidateConfigRejectsUnsafeAppID 确保 Redis 命名空间不能被分隔符或空白破坏。
func TestValidateConfigRejectsUnsafeAppID(t *testing.T) {
	for _, appID := range []string{"site:other", "site name", "site/{slot}"} {
		cfg := validBootstrapConfig()
		cfg.AppID = appID
		if err := Validate(cfg); err == nil {
			t.Fatalf("expected unsafe app_id %q to be rejected", appID)
		}
	}
}

// TestValidateConfigRejectsNonCanonicalInstanceID 确保节点标识不会在 trace 中被 trim 成另一实例名。
func TestValidateConfigRejectsNonCanonicalInstanceID(t *testing.T) {
	for _, instanceID := range []string{" api-1 ", "api:1", "API 1"} {
		cfg := validBootstrapConfig()
		cfg.InstanceID = instanceID
		if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "instance_id") {
			t.Fatalf("期望 instance_id=%q 被拒绝，实际为 %v", instanceID, err)
		}
	}
}

// TestValidateConfigRejectsInvalidRedisMode 确保 Redis 模式拼写和地址语义不会静默推断成其它客户端。
func TestValidateConfigRejectsInvalidRedisMode(t *testing.T) {
	tests := []config.RedisConfig{
		{Type: "", Addrs: []string{"127.0.0.1:6379"}, PoolSize: 1},
		{Type: "standalone", Addrs: []string{"127.0.0.1:6379"}, PoolSize: 1},
		{Type: "SINGLE", Addrs: []string{"127.0.0.1:6379"}, PoolSize: 1},
		{Type: "cluser", Addrs: []string{"127.0.0.1:6379"}, PoolSize: 1},
		{Type: "single", Addrs: []string{"127.0.0.1:6379", "127.0.0.1:6380"}, PoolSize: 1},
		{Type: "single", Addrs: []string{"127.0.0.1:6379"}, AddrMap: map[string]string{"a": "b"}, PoolSize: 1},
		{Type: "cluster", Addrs: []string{"127.0.0.1:6379"}, DB: 1, PoolSize: 1},
	}
	for _, redisCfg := range tests {
		cfg := validBootstrapConfig()
		cfg.Redis = redisCfg
		if err := Validate(cfg); err == nil {
			t.Fatalf("expected invalid redis config to be rejected: %+v", redisCfg)
		}
	}
}

// TestValidateConfigRejectsUnsafeRedisResourceBounds 确保地址探测数量与每节点连接池都有生产硬边界。
func TestValidateConfigRejectsUnsafeRedisResourceBounds(t *testing.T) {
	tests := []struct {
		name string               // name 表示 Redis 错误场景。
		edit func(*config.Config) // edit 注入待验证的越界配置。
		want string               // want 是期望错误字段。
	}{
		{name: "duplicate address", edit: func(cfg *config.Config) {
			cfg.Redis.Type = "cluster"
			cfg.Redis.Addrs = []string{"127.0.0.1:6379", "127.0.0.1:6379"}
		}, want: "重复"},
		{name: "too many addresses", edit: func(cfg *config.Config) {
			cfg.Redis.Type = "cluster"
			cfg.Redis.Addrs = make([]string, config.MaxRedisAddressCount+1)
			for index := range cfg.Redis.Addrs {
				cfg.Redis.Addrs[index] = fmt.Sprintf("127.0.0.1:%d", 7000+index)
			}
		}, want: "redis.addrs"},
		{name: "oversized pool", edit: func(cfg *config.Config) {
			cfg.Redis.PoolSize = config.MaxRedisPoolSize + 1
		}, want: "redis.pool_size"},
		{name: "oversized address map", edit: func(cfg *config.Config) {
			cfg.Redis.Type = "cluster"
			cfg.Redis.AddrMap = make(map[string]string, config.MaxRedisAddressMapCount+1)
			for index := 0; index <= config.MaxRedisAddressMapCount; index++ {
				cfg.Redis.AddrMap[fmt.Sprintf("node-%d", index)] = "127.0.0.1"
			}
		}, want: "redis.addr_map"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validBootstrapConfig()
			tt.edit(&cfg)
			if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("期望 Redis 配置返回包含 %q 的错误，实际为 %v", tt.want, err)
			}
		})
	}
}

// TestValidateConfigRejectsUnsafeRuntimeBounds 确保密码、热加载和 trace 配置不会溢出底层运行时边界。
func TestValidateConfigRejectsUnsafeRuntimeBounds(t *testing.T) {
	tests := []func(*config.Config){
		func(cfg *config.Config) { cfg.Auth.PasswordMinLength = maxPasswordBytes + 1 },
		func(cfg *config.Config) { cfg.Auth.ProfileCacheTTLSeconds = config.MaxProfileCacheTTLSeconds + 1 },
		func(cfg *config.Config) { cfg.HotReload.CheckIntervalSeconds = maxHotReloadIntervalSeconds + 1 },
		func(cfg *config.Config) { cfg.Observability.SampleRatio = 1.01 },
		func(cfg *config.Config) { cfg.Observability.SlowSQLMs = -1 },
		func(cfg *config.Config) { cfg.Observability.RedisSlowMs = config.MaxSlowThresholdMilliseconds + 1 },
	}
	for _, mutate := range tests {
		cfg := validBootstrapConfig()
		mutate(&cfg)
		if err := Validate(cfg); err == nil {
			t.Fatalf("expected unsafe runtime bounds to be rejected: %+v", cfg)
		}
	}
}

// TestValidateConfigRejectsLarkDualOrNonCanonicalSources 确保 Lark 明文值和文件引用不存在静默优先级。
func TestValidateConfigRejectsLarkDualOrNonCanonicalSources(t *testing.T) {
	tests := []func(*config.Config){
		func(cfg *config.Config) {
			cfg.Alert.Lark = config.LarkAlertConfig{Enabled: true, WebhookURL: "https://example.com/hook", WebhookURLRef: "/run/secrets/lark_url"}
		},
		func(cfg *config.Config) {
			cfg.Alert.Lark = config.LarkAlertConfig{Enabled: true, WebhookURL: " https://example.com/hook"}
		},
		func(cfg *config.Config) {
			cfg.Alert.Lark = config.LarkAlertConfig{Enabled: true, WebhookURL: "https://example.com/hook", Secret: "secret", SecretRef: "/run/secrets/lark_secret"}
		},
	}
	for _, edit := range tests {
		cfg := validBootstrapConfig()
		edit(&cfg)
		if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "alert.lark") {
			t.Fatalf("期望 Lark 双来源或非规范配置被拒绝，实际为 %v", err)
		}
	}
}

// TestValidateConfigRejectsOTLPProtocolAliases 确保 OTLP 配置只有 grpc/http 两种规范值。
func TestValidateConfigRejectsOTLPProtocolAliases(t *testing.T) {
	for _, protocol := range []string{"grpc/protobuf", "http/protobuf", "http-protobuf", "HTTP", " http "} {
		cfg := validBootstrapConfig()
		cfg.Observability.OTLPProtocol = protocol
		if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "otlp_protocol") {
			t.Fatalf("期望 otlp_protocol=%q 被拒绝，实际为 %v", protocol, err)
		}
	}
}

// TestValidateConfigRejectsNonCanonicalOTLPEndpoint 确保上报地址只有 host:port 一种解释。
func TestValidateConfigRejectsNonCanonicalOTLPEndpoint(t *testing.T) {
	for _, endpoint := range []string{" http://127.0.0.1:4318 ", "http://127.0.0.1:4318", "127.0.0.1:4318/v1/traces", "127.0.0.1", "127.0.0.1:0"} {
		cfg := validBootstrapConfig()
		cfg.Observability.OTLPEndpoint = endpoint
		if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "otlp_endpoint") {
			t.Fatalf("期望 otlp_endpoint=%q 被拒绝，实际为 %v", endpoint, err)
		}
	}
}

// TestValidateConfigRejectsNormalizedSiteMySQLCollision 确保扩展库名不会在连接注册时 trim/大小写覆盖。
func TestValidateConfigRejectsNormalizedSiteMySQLCollision(t *testing.T) {
	cfg := validBootstrapConfig()
	cfg.SiteMySQL = config.SiteMySQLConfig{
		"site":   {},
		" Site ": {},
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected normalized site_mysql name collision to be rejected")
	}
}

// TestValidateConfigRejectsMissingSnowflakeWorkerID 确保未启用 Redis 租约时雪花 worker_id 缺失会启动失败。
func TestValidateConfigRejectsMissingSnowflakeWorkerID(t *testing.T) {
	cfg := validBootstrapConfig()
	cfg.Snowflake.WorkerID = nil
	if err := Validate(cfg); err == nil {
		t.Fatal("expected missing snowflake.worker_id to be rejected")
	}
}

// TestValidateConfigAcceptsRedisSnowflakeLease 确保 Redis 租约模式不要求静态 worker_id。
func TestValidateConfigAcceptsRedisSnowflakeLease(t *testing.T) {
	cfg := validBootstrapConfig()
	cfg.Snowflake.WorkerID = nil
	cfg.Snowflake.Redis = config.SnowflakeRedisConfig{
		Enabled: true,
		Namespaces: map[string]config.SnowflakeRedisNamespaceConfig{
			"user": {NodeCount: 10},
		},
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("expected redis snowflake lease config to pass: %v", err)
	}
}

// TestValidateConfigRejectsInvalidSnowflakeNamespaceNodeCount 确保 namespace node_id 池大小受雪花位宽约束。
func TestValidateConfigRejectsInvalidSnowflakeNamespaceNodeCount(t *testing.T) {
	cfg := validBootstrapConfig()
	cfg.Snowflake.WorkerID = nil
	cfg.Snowflake.Redis = config.SnowflakeRedisConfig{
		Enabled: true,
		Namespaces: map[string]config.SnowflakeRedisNamespaceConfig{
			"user": {NodeCount: int(idgen.SnowflakeMaxWorkerID + 2)},
		},
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected oversized snowflake.redis namespace node_count to be rejected")
	}

	cfg = validBootstrapConfig()
	cfg.Snowflake.WorkerID = nil
	cfg.Snowflake.Redis = config.SnowflakeRedisConfig{
		Enabled: true,
		Namespaces: map[string]config.SnowflakeRedisNamespaceConfig{
			"user": {NodeCount: -1},
		},
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected negative snowflake.redis namespace node_count to be rejected")
	}
}

// TestValidateConfigRejectsNonCanonicalSnowflakeNamespace 确保 namespace 不会在运行时 trim 后无序覆盖或因大小写失配失效。
func TestValidateConfigRejectsNonCanonicalSnowflakeNamespace(t *testing.T) {
	for _, namespace := range []string{" user ", "User"} {
		cfg := validBootstrapConfig()
		cfg.Snowflake.WorkerID = nil
		cfg.Snowflake.Redis = config.SnowflakeRedisConfig{
			Enabled: true,
			Namespaces: map[string]config.SnowflakeRedisNamespaceConfig{
				namespace: {NodeCount: 10},
			},
		}
		if err := Validate(cfg); err == nil {
			t.Fatalf("expected non-canonical namespace %q to be rejected", namespace)
		}
	}
}

// TestValidateConfigAcceptsIDSegmentNamespace 确保高吞吐业务可单独启用 Redis Segment 号段。
func TestValidateConfigAcceptsIDSegmentNamespace(t *testing.T) {
	cfg := validBootstrapConfig()
	cfg.Snowflake.Segment = config.IDSegmentConfig{
		Enabled:                true,
		Scope:                  "main",
		AllocateTimeoutSeconds: 2,
		Namespaces: map[string]config.IDSegmentNamespaceConfig{
			"recharge.order": {
				Enabled:           true,
				Step:              10000,
				PrefetchThreshold: 2000,
			},
		},
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("expected valid ID Segment config to pass: %v", err)
	}
}

// TestValidateConfigRejectsNonCanonicalInheritedSegmentScope 确保 Segment 继承 Redis scope 时不再静默 trim。
func TestValidateConfigRejectsNonCanonicalInheritedSegmentScope(t *testing.T) {
	cfg := validBootstrapConfig()
	cfg.Snowflake.Redis.Scope = " shared "
	cfg.Snowflake.Segment = config.IDSegmentConfig{
		Enabled: true,
		Namespaces: map[string]config.IDSegmentNamespaceConfig{
			"order": {Enabled: true},
		},
	}
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "snowflake.segment.scope") {
		t.Fatalf("期望非规范继承 scope 被拒绝，实际为 %v", err)
	}
}

// TestValidateConfigRejectsIDSegmentWithoutNamespace 确保开启 Segment 时不能没有启用的业务 namespace。
func TestValidateConfigRejectsIDSegmentWithoutNamespace(t *testing.T) {
	cfg := validBootstrapConfig()
	cfg.Snowflake.Segment = config.IDSegmentConfig{Enabled: true}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected ID Segment without namespace to be rejected")
	}
}

// TestValidateConfigRejectsInvalidIDSegmentOptions 确保 Segment 号段参数保持在可控范围内。
func TestValidateConfigRejectsInvalidIDSegmentOptions(t *testing.T) {
	cfg := validBootstrapConfig()
	cfg.Snowflake.Segment = config.IDSegmentConfig{
		Enabled: true,
		Scope:   "main scope",
		Namespaces: map[string]config.IDSegmentNamespaceConfig{
			"recharge.order": {Enabled: true, Step: 1000},
		},
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected ID Segment scope whitespace to be rejected")
	}

	cfg = validBootstrapConfig()
	cfg.Snowflake.Segment = config.IDSegmentConfig{
		Enabled: true,
		Scope:   "main",
		Namespaces: map[string]config.IDSegmentNamespaceConfig{
			"recharge.order": {Enabled: true, Step: 1000, PrefetchThreshold: 1000},
		},
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected ID Segment prefetch threshold to be rejected")
	}
}

// TestValidateConfigRejectsSnowflakeScopeWhitespace 确保部署级 scope 不能包含空白字符。
func TestValidateConfigRejectsSnowflakeScopeWhitespace(t *testing.T) {
	cfg := validBootstrapConfig()
	cfg.Snowflake.WorkerID = nil
	cfg.Snowflake.Redis = config.SnowflakeRedisConfig{
		Enabled: true,
		Scope:   "main scope",
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected snowflake.redis.scope with whitespace to be rejected")
	}
}

// TestValidateConfigRejectsMixedSnowflakeModes 确保 Redis 租约模式不会被静态 worker_id 覆盖。
func TestValidateConfigRejectsMixedSnowflakeModes(t *testing.T) {
	cfg := validBootstrapConfig()
	cfg.Snowflake.Redis.Enabled = true
	if err := Validate(cfg); err == nil {
		t.Fatal("expected mixed snowflake worker_id and redis lease to be rejected")
	}
}

// TestValidateConfigRejectsUnsafeSnowflakeLeaseInterval 确保续约间隔不能贴近租约 TTL。
func TestValidateConfigRejectsUnsafeSnowflakeLeaseInterval(t *testing.T) {
	cfg := validBootstrapConfig()
	cfg.Snowflake.WorkerID = nil
	cfg.Snowflake.Redis = config.SnowflakeRedisConfig{
		Enabled:              true,
		LeaseSeconds:         20,
		RenewIntervalSeconds: 10,
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected unsafe snowflake redis renew interval to be rejected")
	}
}

// TestValidateConfigRejectsOversizedSnowflakeLease 确保极端租约值不会溢出 time.Duration 并触发 ticker panic。
func TestValidateConfigRejectsOversizedSnowflakeLease(t *testing.T) {
	cfg := validBootstrapConfig()
	cfg.Snowflake.WorkerID = nil
	cfg.Snowflake.Redis = config.SnowflakeRedisConfig{
		Enabled:              true,
		LeaseSeconds:         maxSnowflakeRedisLeaseSeconds + 1,
		RenewIntervalSeconds: 1,
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected oversized snowflake Redis lease to be rejected")
	}
}

// TestValidateConfigRejectsInvalidSnowflakeWorkerID 确保雪花 worker_id 越界时启动失败。
func TestValidateConfigRejectsInvalidSnowflakeWorkerID(t *testing.T) {
	cfg := validBootstrapConfig()
	cfg.Snowflake.WorkerID = int64Ptr(1024)
	if err := Validate(cfg); err == nil {
		t.Fatal("expected invalid snowflake.worker_id to be rejected")
	}
}

// TestValidateConfigRejectsInvalidUserRouteShardCount 确保业务用户写入路由只允许平滑拆分档位。
func TestValidateConfigRejectsInvalidUserRouteShardCount(t *testing.T) {
	cfg := validBootstrapConfig()
	cfg.User.RouteShardCount = 3
	if err := Validate(cfg); err == nil {
		t.Fatal("expected invalid user.route_shard_count to be rejected")
	}
}

// TestValidateConfigAcceptsUserRouteShardCount 验证运行时允许已支持的物理拆分档位。
func TestValidateConfigAcceptsUserRouteShardCount(t *testing.T) {
	cfg := validBootstrapConfig()
	cfg.User.RouteShardCount = 2
	if err := Validate(cfg); err != nil {
		t.Fatalf("valid user.route_shard_count should pass: %v", err)
	}
}

// TestNormalizeConfigDefaultsUserRouteShardCount 确保业务用户写入路由缺省时稳定回落单表。
func TestNormalizeConfigDefaultsUserRouteShardCount(t *testing.T) {
	cfg := config.Config{}
	Normalize(&cfg)
	if cfg.User.RouteShardCount != defaultUserRouteShardCount {
		t.Fatalf("route_shard_count = %d, want %d", cfg.User.RouteShardCount, defaultUserRouteShardCount)
	}
}

// TestNormalizeConfigPreservesInvalidUserRouteShardCount 确保非法负数不会被默认值掩盖。
func TestNormalizeConfigPreservesInvalidUserRouteShardCount(t *testing.T) {
	cfg := config.Config{}
	cfg.User.RouteShardCount = -1
	Normalize(&cfg)
	if cfg.User.RouteShardCount != -1 {
		t.Fatalf("route_shard_count = %d, want -1", cfg.User.RouteShardCount)
	}
}

// TestNormalizeConfigPreservesInvalidAuthBounds 确保负数认证参数不会在严格校验前被默认值掩盖。
func TestNormalizeConfigPreservesInvalidAuthBounds(t *testing.T) {
	cfg := validBootstrapConfig()
	cfg.Auth.SessionTTLSeconds = -1
	cfg.Auth.ProfileCacheTTLSeconds = -1
	cfg.Auth.PasswordMinLength = -1
	Normalize(&cfg)
	if cfg.Auth.SessionTTLSeconds != -1 || cfg.Auth.ProfileCacheTTLSeconds != -1 || cfg.Auth.PasswordMinLength != -1 {
		t.Fatalf("Normalize() 不应改写负数认证参数: %+v", cfg.Auth)
	}
}

// TestValidateConfigRejectsNegativeAuthBounds 确保认证 TTL 和密码长度拼错时拒绝启动。
func TestValidateConfigRejectsNegativeAuthBounds(t *testing.T) {
	tests := []struct {
		name string               // name 表示负数配置场景。
		edit func(*config.Config) // edit 写入待校验的负数配置。
		want string               // want 表示错误中必须包含的字段名。
	}{
		{name: "session ttl", edit: func(cfg *config.Config) { cfg.Auth.SessionTTLSeconds = -1 }, want: "session_ttl_seconds"},
		{name: "profile ttl", edit: func(cfg *config.Config) { cfg.Auth.ProfileCacheTTLSeconds = -1 }, want: "profile_cache_ttl_seconds"},
		{name: "password length", edit: func(cfg *config.Config) { cfg.Auth.PasswordMinLength = -1 }, want: "password_min_length"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validBootstrapConfig()
			tt.edit(&cfg)
			Normalize(&cfg)
			if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("期望负数配置返回包含 %q 的错误，实际为 %v", tt.want, err)
			}
		})
	}
}

// TestValidateConfigSkipsDisabledCollectorConfig 确保关闭 Collector 时不校验子配置细节。
func TestValidateConfigSkipsDisabledCollectorConfig(t *testing.T) {
	cfg := validBootstrapConfig()
	cfg.Collector = config.CollectorConfig{
		Enabled: false,
		Kafka: config.CollectorKafkaConfig{
			WriteBatchSize:             -1,
			WriteBatchWaitMilliseconds: 999999,
		},
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("disabled collector should skip child validation: %v", err)
	}
}

// TestValidateConfigRejectsCollectorEnabledWithoutTopic 确保启用 Collector 时必须配置 Topic。
func TestValidateConfigRejectsCollectorEnabledWithoutTopic(t *testing.T) {
	cfg := validBootstrapConfig()
	cfg.Collector.Enabled = true
	cfg.Collector.Kafka.Brokers = []string{"127.0.0.1:9092"}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected collector enabled without topic to be rejected")
	}
}

// TestValidateConfigAcceptsCollectorTaskTopic 确保 Collector 可按 bizType 配置 Topic。
func TestValidateConfigAcceptsCollectorTaskTopic(t *testing.T) {
	cfg := validBootstrapConfig()
	cfg.Collector.Enabled = true
	cfg.Collector.Kafka.Brokers = []string{"127.0.0.1:9092"}
	cfg.Collector.Tasks = map[string]config.CollectorTaskConfig{
		config.CollectorBizTypeAuthSecurity: {Topic: config.CollectorTopicAuthSecurity},
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("valid collector task topic should pass: %v", err)
	}
}

// TestValidateConfigRejectsPublicOpsAllowedIP 确保运维白名单不能误配公网 IP。
func TestValidateConfigRejectsPublicOpsAllowedIP(t *testing.T) {
	cfg := validBootstrapConfig()
	cfg.Ops.ConfigReloadAllowedIPs = []string{"8.8.8.8"}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected public ops allowed IP to be rejected")
	}
}

// TestValidateConfigRejectsNonCanonicalOpsConfig 确保运维鉴权配置不会被静默清洗或去重。
func TestValidateConfigRejectsNonCanonicalOpsConfig(t *testing.T) {
	tests := []struct {
		name string               // 子场景名称
		edit func(*config.Config) // 注入非规范配置
	}{
		{name: "令牌首尾空白", edit: func(cfg *config.Config) { cfg.Ops.ConfigReloadToken = " " + cfg.Ops.ConfigReloadToken }},
		{name: "白名单空项", edit: func(cfg *config.Config) { cfg.Ops.ConfigReloadAllowedIPs = []string{""} }},
		{name: "白名单首尾空白", edit: func(cfg *config.Config) { cfg.Ops.ConfigReloadAllowedIPs = []string{" 127.0.0.1"} }},
		{name: "白名单重复", edit: func(cfg *config.Config) { cfg.Ops.ConfigReloadAllowedIPs = []string{"127.0.0.1", "127.0.0.1"} }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validBootstrapConfig()
			tt.edit(&cfg)
			if err := Validate(cfg); err == nil {
				t.Fatal("expected non-canonical ops config to be rejected")
			}
		})
	}
}

// TestValidateConfigRejectsLarkWithoutWebhook 确保启用 Lark 告警时必须配置发送端点。
func TestValidateConfigRejectsLarkWithoutWebhook(t *testing.T) {
	cfg := validBootstrapConfig()
	cfg.Alert.Lark.Enabled = true
	if err := Validate(cfg); err == nil {
		t.Fatal("expected lark alert without webhook to be rejected")
	}
}

// TestValidateConfigRejectsNegativeLarkOptions 确保 Lark 数值配置不能使用负数。
func TestValidateConfigRejectsNegativeLarkOptions(t *testing.T) {
	cfg := validBootstrapConfig()
	cfg.Alert.Lark.Enabled = true
	cfg.Alert.Lark.WebhookURL = "https://open.larksuite.com/open-apis/bot/v2/hook/test"
	cfg.Alert.Lark.TimeoutSeconds = -1
	if err := Validate(cfg); err == nil {
		t.Fatal("expected negative lark timeout to be rejected")
	}
	cfg.Alert.Lark.TimeoutSeconds = 1
	cfg.Alert.Lark.MaxErrorBytes = -1
	if err := Validate(cfg); err == nil {
		t.Fatal("expected negative lark max_error_bytes to be rejected")
	}
}

// TestValidateConfigRejectsOversizedLarkTimeout 防止启动校验接受的值在运行时又被静默改写。
func TestValidateConfigRejectsOversizedLarkTimeout(t *testing.T) {
	cfg := validBootstrapConfig()
	cfg.Alert.Lark.Enabled = true
	cfg.Alert.Lark.WebhookURL = "https://open.larksuite.com/open-apis/bot/v2/hook/test"
	cfg.Alert.Lark.TimeoutSeconds = config.MaxLarkAlertTimeoutSeconds + 1
	if err := Validate(cfg); err == nil {
		t.Fatal("expected oversized lark timeout to be rejected")
	}
}

// TestValidateConfigAcceptsPrivateOpsCIDR 确保内网 CIDR 白名单配置可启动。
func TestValidateConfigAcceptsPrivateOpsCIDR(t *testing.T) {
	cfg := validBootstrapConfig()
	cfg.Ops.ConfigReloadAllowedIPs = []string{"10.0.0.0/8", "127.0.0.1"}
	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

// TestValidateConfigRejectsLargeAuthRateLimit 确保极端限流窗口不能通过启动校验。
func TestValidateConfigRejectsLargeAuthRateLimit(t *testing.T) {
	cfg := validBootstrapConfig()
	cfg.Auth.LoginRateLimit = config.AuthRateLimitConfig{
		Enabled:       true,
		WindowSeconds: maxAuthRateLimitWindowSeconds + 1,
		MaxAttempts:   5,
		LockSeconds:   300,
	}
	if err := Validate(cfg); err == nil {
		t.Fatal("expected oversized auth rate limit window to be rejected")
	}
}

// TestValidateConfigRejectsNegativeAuthRateLimit 确保负数不会被运行时默认值掩盖。
func TestValidateConfigRejectsNegativeAuthRateLimit(t *testing.T) {
	for _, edit := range []func(*config.AuthRateLimitConfig){
		func(item *config.AuthRateLimitConfig) { item.WindowSeconds = -1 },
		func(item *config.AuthRateLimitConfig) { item.MaxAttempts = -1 },
		func(item *config.AuthRateLimitConfig) { item.LockSeconds = -1 },
	} {
		cfg := validBootstrapConfig()
		cfg.Auth.LoginRateLimit = config.AuthRateLimitConfig{Enabled: true, WindowSeconds: 60, MaxAttempts: 5, LockSeconds: 300}
		edit(&cfg.Auth.LoginRateLimit)
		if err := Validate(cfg); err == nil {
			t.Fatalf("expected negative auth rate limit to be rejected: %+v", cfg.Auth.LoginRateLimit)
		}
	}
}

// TestValidateConfigRejectsProductionPlaceholderJWTSecret 确保生产环境不能使用示例 JWT 密钥。
func TestValidateConfigRejectsProductionPlaceholderJWTSecret(t *testing.T) {
	cfg := validProductionBootstrapConfig()
	cfg.JwtSecret = "replace-with-strong-secret"
	if err := Validate(cfg); err == nil {
		t.Fatal("expected production placeholder jwt_secret to be rejected")
	}
}

// TestValidateConfigChecksProductionBeforeSecurityFiles 确保纯配置错误不会触发密钥文件读取。
func TestValidateConfigChecksProductionBeforeSecurityFiles(t *testing.T) {
	cfg := validProductionBootstrapConfig()
	cfg.JwtSecret = "replace-with-strong-secret"
	cfg.Security.SecretKey = validSecuritySecretKey(t, "v1")
	cfg.Security.SecretKey.Versions[0].AESKey = ""
	cfg.Security.SecretKey.Versions[0].AESKeyRef = "/missing/security-key"
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "jwt_secret") {
		t.Fatalf("Validate() error = %v, want production jwt_secret error", err)
	}
}

// TestValidateConfigRejectsProductionMissingOpsToken 确保生产环境必须配置热加载运维令牌。
func TestValidateConfigRejectsProductionMissingOpsToken(t *testing.T) {
	cfg := validProductionBootstrapConfig()
	cfg.Ops.ConfigReloadToken = ""
	if err := Validate(cfg); err == nil {
		t.Fatal("expected missing production ops token to be rejected")
	}
}

// TestValidateConfigRejectsProductionRedisTLSInsecure 确保生产环境不能跳过 Redis TLS 校验。
func TestValidateConfigRejectsProductionRedisTLSInsecure(t *testing.T) {
	cfg := validProductionBootstrapConfig()
	cfg.Redis.TLSInsecureSkipVerify = true
	if err := Validate(cfg); err == nil {
		t.Fatal("expected production redis tls insecure skip verify to be rejected")
	}
}

// TestValidateConfigRejectsProductionDisabledLoginRateLimit 确保生产环境必须启用登录限流。
func TestValidateConfigRejectsProductionDisabledLoginRateLimit(t *testing.T) {
	cfg := validProductionBootstrapConfig()
	cfg.Auth.LoginRateLimit.Enabled = false
	if err := Validate(cfg); err == nil {
		t.Fatal("expected production disabled login rate limit to be rejected")
	}
}

// TestValidateConfigRejectsProductionRegisterWithoutRateLimit 确保生产开放注册时必须启用注册限流。
func TestValidateConfigRejectsProductionRegisterWithoutRateLimit(t *testing.T) {
	cfg := validProductionBootstrapConfig()
	cfg.Auth.RegisterEnabled = true
	cfg.Auth.RegisterRateLimit.Enabled = false
	if err := Validate(cfg); err == nil {
		t.Fatal("expected production register without rate limit to be rejected")
	}
}

// TestValidateConfigAcceptsProductionSafeConfig 确保生产安全配置可以通过启动校验。
func TestValidateConfigAcceptsProductionSafeConfig(t *testing.T) {
	cfg := validProductionBootstrapConfig()
	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

// TestValidateConfigRejectsProductionWildcardInternalHost 确保生产内网监听器不能退化为全网卡监听。
func TestValidateConfigRejectsProductionWildcardInternalHost(t *testing.T) {
	cfg := validProductionBootstrapConfig()
	cfg.InternalServer.Host = "0.0.0.0"
	if err := Validate(cfg); err == nil {
		t.Fatal("期望生产 internal_server 通配监听被拒绝，实际为 nil")
	}
}

// TestValidateConfigRequiresMTLSForPrivateInternalHost 确保生产跨主机内网监听必须配置完整 mTLS。
func TestValidateConfigRequiresMTLSForPrivateInternalHost(t *testing.T) {
	cfg := validProductionBootstrapConfig()
	cfg.InternalServer.Host = "10.0.0.10"
	if err := Validate(cfg); err == nil {
		t.Fatal("期望生产私网监听缺少 mTLS 被拒绝，实际为 nil")
	}
	cfg.InternalServer.CertFile = "/etc/tls/server.crt"
	cfg.InternalServer.KeyFile = "/etc/tls/server.key"
	cfg.InternalServer.ClientCAFile = "/etc/tls/client-ca.crt"
	if err := Validate(cfg); err != nil {
		t.Fatalf("完整 mTLS 私网监听配置应通过校验: %v", err)
	}
}

// TestValidateConfigRejectsWhitespaceMTLSPath 确保 mTLS 文件不会按 trim 后的第二条路径加载。
func TestValidateConfigRejectsWhitespaceMTLSPath(t *testing.T) {
	cfg := validBootstrapConfig()
	cfg.InternalServer.CertFile = " /etc/tls/server.crt"
	cfg.InternalServer.KeyFile = "/etc/tls/server.key"
	cfg.InternalServer.ClientCAFile = "/etc/tls/client-ca.crt"
	if err := Validate(cfg); err == nil || !strings.Contains(err.Error(), "TLS 文件路径") {
		t.Fatalf("期望带空白的 mTLS 路径被拒绝，实际为 %v", err)
	}
}

// TestValidateConfigRejectsPartialInternalMTLS 确保任意环境都不能接受半配置的 mTLS。
func TestValidateConfigRejectsPartialInternalMTLS(t *testing.T) {
	cfg := validBootstrapConfig()
	cfg.InternalServer.CertFile = "/etc/tls/server.crt"
	if err := Validate(cfg); err == nil {
		t.Fatal("期望不完整 mTLS 配置被拒绝，实际为 nil")
	}
}

// TestValidateConfigRejectsCIDRThatExtendsIntoPublicSpace 确保白名单 CIDR 不能仅因起始地址私有而覆盖公网。
func TestValidateConfigRejectsCIDRThatExtendsIntoPublicSpace(t *testing.T) {
	cfg := validBootstrapConfig()
	cfg.Ops.ConfigReloadAllowedIPs = []string{"10.0.0.0/7"}
	if err := Validate(cfg); err == nil {
		t.Fatal("期望跨出私网范围的 CIDR 被拒绝，实际为 nil")
	}
}

// validBootstrapConfig 返回满足默认启动校验的 API 测试配置。
func validBootstrapConfig() config.Config {
	cfg := config.Config{
		RestConf:     rest.RestConf{Host: "127.0.0.1", Port: 8890},
		AppID:        "1",
		AppKey:       "test-app-key-0123456789",
		JwtSecret:    "test-secret-please-change",
		JwtExpiresIn: config.DefaultJWTExpiresInSeconds,
		Snowflake: config.SnowflakeConfig{
			WorkerID: int64Ptr(1),
		},
		Auth: config.AuthConfig{
			Issuer:                 "api",
			PasswordMinLength:      8,
			ProfileCacheTTLSeconds: config.DefaultProfileCacheTTLSeconds,
		},
		Redis: config.RedisConfig{
			Type:     "single",
			Addrs:    []string{"127.0.0.1:6379"},
			PoolSize: 1,
		},
		MySQL: validMySQLConfig("api"),
		Ops: config.OpsConfig{
			ConfigReloadToken: "test-ops-token-0123456789",
		},
		InternalServer: config.InternalServerConfig{
			Host: "127.0.0.1",
			Port: 8891,
		},
	}
	cfg.Mode = config.ModeDevelopment
	return cfg
}

// validMySQLConfig 返回供纯配置校验使用的有界连接池参数。
func validMySQLConfig(database string) config.MySQLConfig {
	return config.MySQLConfig{
		WriteDataSource: "user:password@tcp(127.0.0.1:3306)/" + database,
		MaxOpenConns:    20,
		MaxIdleConns:    10,
		ConnMaxLifetime: 300,
	}
}

// validProductionBootstrapConfig 返回满足生产模式校验的 API 测试配置。
func validProductionBootstrapConfig() config.Config {
	cfg := validBootstrapConfig()
	cfg.Mode = "pro"
	cfg.AppKey = "prod-app-key-9f3b6e1c7a2d4f0b"
	cfg.JwtSecret = "prod-jwt-9f3b6e1c7a2d4f0b8c5e6a1d2f3c4b5a"
	cfg.Auth.LoginRateLimit = config.AuthRateLimitConfig{
		Enabled:       true,
		WindowSeconds: 60,
		MaxAttempts:   5,
		LockSeconds:   300,
	}
	cfg.Auth.RegisterRateLimit = config.AuthRateLimitConfig{
		Enabled:       true,
		WindowSeconds: 60,
		MaxAttempts:   3,
		LockSeconds:   600,
	}
	cfg.Ops.ConfigReloadToken = "prod-ops-9f3b6e1c7a2d4f0b"
	// Collector 生产测试配置接通认证风控固定 Kafka 路由。
	cfg.Collector = config.CollectorConfig{
		Enabled: true,
		Kafka: config.CollectorKafkaConfig{
			Brokers: []string{"127.0.0.1:9092"},
		},
		Tasks: map[string]config.CollectorTaskConfig{
			config.CollectorBizTypeAuthSecurity: {Topic: config.CollectorTopicAuthSecurity},
		},
	}
	return cfg
}

// int64Ptr 返回 int64 指针，便于构造可选配置。
func int64Ptr(value int64) *int64 {
	return &value
}
