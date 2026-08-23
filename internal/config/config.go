package config

import "github.com/zeromicro/go-zero/rest"

const (
	// ModeDevelopment 表示本地开发运行模式。
	ModeDevelopment = "dev"
	// ModeTest 表示自动化测试运行模式。
	ModeTest = "test"
	// ModeRuntimeTest 表示运行时联调环境。
	ModeRuntimeTest = "rt"
	// ModePreRelease 表示预发布环境。
	ModePreRelease = "pre"
	// ModeProduction 表示生产环境；其它拼写不会触发生产安全策略。
	ModeProduction = "pro"
	// DefaultJWTExpiresInSeconds 是未配置 jwt_expires_in 时使用的 24 小时会话周期。
	DefaultJWTExpiresInSeconds int64 = 24 * 60 * 60
	// MaxJWTExpiresInSeconds 把前台 JWT 和 Redis 登录态的最长有效期限制为 30 天。
	MaxJWTExpiresInSeconds int64 = 30 * 24 * 60 * 60
	// MaxMySQLReadDataSourceCount 限制单个逻辑库的读副本数量和串行启动探测时长。
	MaxMySQLReadDataSourceCount = 8
	// MaxMySQLOpenConns 限制单个逻辑库连接池对 MySQL 的最大连接占用。
	MaxMySQLOpenConns = 1000
	// MaxMySQLConnLifetimeSeconds 限制连接最长 24 小时后重建，避免长期持有失效连接。
	MaxMySQLConnLifetimeSeconds = 24 * 60 * 60
	// MaxRedisAddressCount 限制 Cluster 启动时串行探测的节点数量。
	MaxRedisAddressCount = 32
	// MaxRedisAddressMapCount 限制 Cluster 地址改写表大小，避免错误配置放大启动开销。
	MaxRedisAddressMapCount = 256
	// MaxRedisPoolSize 限制单节点连接池占用；Cluster 模式会对每个节点分别应用该上限。
	MaxRedisPoolSize = 1000
	// DefaultProfileCacheTTLSeconds 是用户资料缓存未配置时使用的 5 分钟 TTL。
	DefaultProfileCacheTTLSeconds int64 = 5 * 60
	// MaxProfileCacheTTLSeconds 把用户资料缓存最长生命周期限制为 24 小时。
	MaxProfileCacheTTLSeconds int64 = 24 * 60 * 60
	// MaxSlowThresholdMilliseconds 限制 SQL 与 Redis 慢操作阈值最长为 60 秒；0 表示关闭慢日志。
	MaxSlowThresholdMilliseconds int64 = 60 * 1000
	// MaxLarkAlertTimeoutSeconds 限制单次 Lark 告警 HTTP 请求最长阻塞 30 秒。
	MaxLarkAlertTimeoutSeconds = 30
)

// ValidMode 判断服务运行模式是否为项目声明的规范值，避免拼写错误绕过环境安全策略。
func ValidMode(mode string) bool {
	switch mode {
	case ModeDevelopment, ModeTest, ModeRuntimeTest, ModePreRelease, ModeProduction:
		return true
	default:
		return false
	}
}

// IsProductionMode 判断服务是否运行在唯一的生产模式 pro。
func IsProductionMode(mode string) bool {
	return mode == ModeProduction
}

// JWTExpiresInSeconds 返回直接装配时的安全 TTL；正式配置仍须通过启动校验拒绝越界值。
func JWTExpiresInSeconds(configured int64) int64 {
	if configured <= 0 {
		return DefaultJWTExpiresInSeconds
	}
	if configured > MaxJWTExpiresInSeconds {
		return MaxJWTExpiresInSeconds
	}
	return configured
}

// ProfileCacheTTLSeconds 返回直接装配时的安全缓存 TTL；正式配置仍须通过启动校验拒绝越界值。
func ProfileCacheTTLSeconds(configured int64) int64 {
	if configured <= 0 {
		return DefaultProfileCacheTTLSeconds
	}
	if configured > MaxProfileCacheTTLSeconds {
		return MaxProfileCacheTTLSeconds
	}
	return configured
}

// MySQLConfig 定义关系数据库连接与连接池参数。
type MySQLConfig struct {
	WriteDataSource string   `json:"write_data_source"`          // 写库 DSN（必填）
	ReadDataSources []string `json:"read_data_sources,optional"` // 读库 DSN 列表，最多 8 个且不得与写库重复
	MaxOpenConns    int      `json:"max_open_conns"`             // 最大打开连接数，范围 1-1000
	MaxIdleConns    int      `json:"max_idle_conns"`             // 最大空闲连接数，不得超过 max_open_conns
	ConnMaxLifetime int      `json:"conn_max_lifetime"`          // 连接最大生命周期，单位秒，范围 1-86400
	Debug           bool     `json:"debug"`                      // 是否开启 GORM 调试模式
}

// SiteMySQLConfig 定义可选命名扩展库配置。
type SiteMySQLConfig map[string]MySQLConfig

// RedisConfig 定义 Redis 连接与连接池参数。
type RedisConfig struct {
	Type                  string            `json:"type"`                              // Redis 模式：single 或 cluster，不接受别名或空值推断
	Addrs                 []string          `json:"addrs"`                             // Redis 地址列表，最多 32 个且不得重复
	AddrMap               map[string]string `json:"addr_map,optional"`                 // 集群地址改写表，最多 256 项
	Password              string            `json:"password"`                          // 密码
	DB                    int               `json:"db"`                                // 数据库编号必须非负；cluster 模式只允许0。
	PoolSize              int               `json:"pool_size"`                         // 单节点连接池大小，范围 1-1000
	TLS                   bool              `json:"tls,optional"`                      // 是否启用 TLS
	TLSInsecureSkipVerify bool              `json:"tls_insecure_skip_verify,optional"` // 是否跳过 TLS 证书校验
}

// SnowflakeRedisConfig 定义雪花 node_id 的 Redis 租约分配配置。
type SnowflakeRedisConfig struct {
	Enabled              bool                                     `json:"enabled,optional"`                // 是否使用 Redis 租约自动分配 node_id
	Scope                string                                   `json:"scope,optional"`                  // node_id 池部署级作用域，同一套 api/admin 部署必须一致
	LeaseSeconds         int                                      `json:"lease_seconds,optional"`          // node_id 租约 TTL，单位秒
	RenewIntervalSeconds int                                      `json:"renew_interval_seconds,optional"` // node_id 续约间隔，单位秒
	Namespaces           map[string]SnowflakeRedisNamespaceConfig `json:"namespaces,optional"`             // 按业务 namespace 覆盖 node_id 池大小
}

// SnowflakeRedisNamespaceConfig 定义单个业务命名空间的雪花 node_id 池策略。
type SnowflakeRedisNamespaceConfig struct {
	NodeCount int `json:"node_count,optional"` // 当前 namespace 可竞争的 node_id 数量；0 表示使用完整 0-1023 池
}

// IDSegmentNamespaceConfig 定义单个业务命名空间的 Redis 号段取号策略。
type IDSegmentNamespaceConfig struct {
	Enabled           bool  `json:"enabled,optional"`            // 是否让该 namespace 使用 Segment 策略
	Step              int64 `json:"step,optional"`               // 每次从 Redis 申请的号段大小
	PrefetchThreshold int64 `json:"prefetch_threshold,optional"` // 当前号段剩余小于等于该值时预取下一段
	Start             int64 `json:"start,optional"`              // Redis key 首次初始化的高水位，默认 0
}

// IDSegmentConfig 定义高吞吐业务使用的 Redis 号段本地缓存策略。
type IDSegmentConfig struct {
	Enabled                bool                                `json:"enabled,optional"`                  // 是否启用 Segment 策略
	Scope                  string                              `json:"scope,optional"`                    // 号段高水位部署级作用域，同一套 api/admin 部署必须一致
	AllocateTimeoutSeconds int                                 `json:"allocate_timeout_seconds,optional"` // 单次 Redis 号段申请超时，单位秒
	Namespaces             map[string]IDSegmentNamespaceConfig `json:"namespaces,optional"`               // 按业务 namespace 配置 Segment 策略
}

// SnowflakeConfig 定义业务 ID 生成配置，默认使用雪花，指定 namespace 可切换 Redis Segment。
type SnowflakeConfig struct {
	WorkerID *int64               `json:"worker_id,optional"` // 手动指定 node_id，范围 0-1023；多实例优先使用 Redis 租约
	Redis    SnowflakeRedisConfig `json:"redis,optional"`     // Redis 租约 node_id 分配配置
	Segment  IDSegmentConfig      `json:"segment,optional"`   // 高吞吐业务号段配置；启用的 namespace 不再使用雪花位格式
}

// SecuritySecretKeyVersionConfig 定义配置文件中的单个秘钥版本材料。
type SecuritySecretKeyVersionConfig struct {
	KeyVersion             string `json:"key_version"`                         // 秘钥版本号
	AESKey                 string `json:"aes_key,optional"`                    // AES/HMAC 主密钥明文，长度为 16、24 或 32 字节；与 aes_key_ref 二选一
	AESKeyRef              string `json:"aes_key_ref,optional"`                // AES/HMAC 主密钥文件路径；与 aes_key 二选一，修改后必须重启
	RSAPublicKeyUser       string `json:"rsa_public_key_user,optional"`        // 用户 RSA 公钥 PEM 文本，至少 2048 位；与 rsa_public_key_user_ref 二选一
	RSAPublicKeyUserRef    string `json:"rsa_public_key_user_ref,optional"`    // 用户 RSA 公钥 PEM 文件路径，内容至少 2048 位；与 rsa_public_key_user 二选一
	RSAPublicKeyServer     string `json:"rsa_public_key_server,optional"`      // 服务端 RSA 公钥 PEM 文本，至少 2048 位且必须匹配私钥；与 rsa_public_key_server_ref 二选一
	RSAPublicKeyServerRef  string `json:"rsa_public_key_server_ref,optional"`  // 服务端 RSA 公钥 PEM 文件路径，内容至少 2048 位且必须匹配私钥；与 rsa_public_key_server 二选一
	RSAPrivateKeyServer    string `json:"rsa_private_key_server,optional"`     // 服务端 RSA 私钥 PEM 文本，至少 2048 位；与 rsa_private_key_server_ref 二选一
	RSAPrivateKeyServerRef string `json:"rsa_private_key_server_ref,optional"` // 服务端 RSA 私钥 PEM 文件路径，内容至少 2048 位；与 rsa_private_key_server 二选一
	Remark                 string `json:"remark,optional"`                     // 版本备注
}

// SecuritySecretKeyConfig 定义当前 app_id 的签名验签和加解密秘钥配置。
type SecuritySecretKeyConfig struct {
	SignStatus    int                              `json:"sign_status,optional"`    // 签名验签状态：1启用，0停用；空安全段保持关闭
	CryptoStatus  int                              `json:"crypto_status,optional"`  // 加密解密状态：1启用，0停用；空安全段保持关闭
	StableVersion string                           `json:"stable_version,optional"` // 稳定版本；配置安全材料时必须存在于 versions
	GrayVersion   string                           `json:"gray_version,optional"`   // 灰度版本
	GrayPercent   int                              `json:"gray_percent,optional"`   // 灰度比例，0-100
	GraySalt      string                           `json:"gray_salt,optional"`      // 灰度哈希盐值
	Versions      []SecuritySecretKeyVersionConfig `json:"versions,optional"`       // 多版本材料列表
}

// SecurityConfig 聚合前台接口安全链路配置。
type SecurityConfig struct {
	SecretKey SecuritySecretKeyConfig `json:"secret_key,optional"` // 当前 app_id 的秘钥版本和材料配置
}

// HotReloadConfig 定义 config.yaml 热加载监听参数。
type HotReloadConfig struct {
	Enabled              bool `json:"enabled,optional"`                // 是否启用配置热加载
	CheckIntervalSeconds int  `json:"check_interval_seconds,optional"` // 后续轮询秒数；0使用5秒，上限3600，首轮立即检查。
}

// ConfigFilesConfig 定义可选外部配置文件入口。
type ConfigFilesConfig struct {
	Runtime string `json:"runtime,optional"` // 运行期配置文件路径
}

const (
	// CollectorBizTypeAuthSecurity 表示认证风控事件业务类型。
	CollectorBizTypeAuthSecurity = "auth.security"
	// CollectorTopicAuthSecurity 表示 API 与 Admin 共享的认证风控 Kafka Topic。
	CollectorTopicAuthSecurity = "api_collector_auth_security_events"
	// MaxCollectorTaskCount 限制单实例可注册的业务路由数，防止错误配置无界放大路由表。
	MaxCollectorTaskCount = 256
	// MaxCollectorTopicCount 限制单实例 Kafka Writer 数；每个不同 Topic 会持有独立连接资源。
	MaxCollectorTopicCount = 64
)

// CollectorKafkaConfig 定义通用收集器 Kafka 投递配置。
type CollectorKafkaConfig struct {
	Brokers                    []string `json:"brokers,optional"`                       // Kafka broker 地址
	WriteBatchSize             int      `json:"write_batch_size,optional"`              // Producer 写入批次大小
	WriteBatchWaitMilliseconds int      `json:"write_batch_wait_milliseconds,optional"` // Producer 写入批次等待时间，单位毫秒
	WriteTimeout               int      `json:"write_timeout,optional"`                 // Producer 写入超时时间，单位秒
}

// CollectorTaskConfig 定义单个 Collector 任务的 Kafka 路由。
type CollectorTaskConfig struct {
	Topic string `json:"topic,optional"` // 当前 bizType 投递的 Kafka Topic
}

// CollectorConfig 定义通用收集器配置。
type CollectorConfig struct {
	Enabled bool                           `json:"enabled,optional"` // 是否启用通用收集器
	Kafka   CollectorKafkaConfig           `json:"kafka,optional"`   // Kafka 投递链路配置
	Tasks   map[string]CollectorTaskConfig `json:"tasks,optional"`   // 按 bizType 配置的 Kafka 路由，最多 256 项
}

// ObservabilityConfig 聚合日志、链路追踪相关配置。
type ObservabilityConfig struct {
	ServiceName  string  `json:"service_name,optional"`           // 服务名
	Environment  string  `json:"-"`                               // 观测环境仅由顶层 Mode 派生，不接受独立配置
	TraceEnabled bool    `json:"trace_enabled,optional"`          // 是否启用 trace 采样/上报
	OTLPProtocol string  `json:"otlp_protocol,optional"`          // OTLP 协议：grpc/http
	OTLPEndpoint string  `json:"otlp_endpoint,optional"`          // OTLP 上报地址，仅接受 host:port
	OTLPInsecure bool    `json:"otlp_insecure,optional"`          // OTLP 是否明文
	SampleRatio  float64 `json:"sample_ratio,optional,default=1"` // trace 采样率 0~1
	SlowSQLMs    int64   `json:"slow_sql_ms,optional"`            // 慢 SQL 阈值，毫秒；0 关闭，最大 60000
	RedisSlowMs  int64   `json:"redis_slow_ms,optional"`          // 慢 Redis 阈值，毫秒；0 关闭，最大 60000
}

// LarkAlertConfig 定义 Lark 群机器人告警配置。
type LarkAlertConfig struct {
	Enabled        bool   `json:"enabled,optional"`         // 是否启用 Lark 告警
	WebhookURL     string `json:"webhook_url,optional"`     // Lark 机器人 webhook URL；与 webhook_url_ref 二选一
	WebhookURLRef  string `json:"webhook_url_ref,optional"` // webhook URL 文件路径；与 webhook_url 二选一
	Secret         string `json:"secret,optional"`          // Lark 签名密钥；与 secret_ref 二选一
	SecretRef      string `json:"secret_ref,optional"`      // Lark 签名密钥文件路径；与 secret 二选一
	TimeoutSeconds int    `json:"timeout_seconds,optional"` // HTTP 请求超时，单位秒，默认 5 秒
	AtAll          bool   `json:"at_all,optional"`          // 是否在告警中 @所有人
	MaxErrorBytes  int    `json:"max_error_bytes,optional"` // 错误摘要最大字节数，默认 800
}

// AlertConfig 聚合外部告警通道配置。
type AlertConfig struct {
	Lark LarkAlertConfig `json:"lark,optional"` // Lark 群机器人告警配置
}

// AuthConfig 定义前台用户登录态运行参数。
type AuthConfig struct {
	RegisterEnabled        bool                `json:"register_enabled,optional"`              // 是否开放注册接口
	Issuer                 string              `json:"issuer,optional"`                        // JWT 发行方
	SessionTTLSeconds      int64               `json:"session_ttl_seconds,optional"`           // Redis 会话 TTL；0 或超过 JWT 时使用 jwt_expires_in，负数非法
	ProfileCacheTTLSeconds int64               `json:"profile_cache_ttl_seconds,optional"`     // 用户资料缓存 TTL，单位秒，范围 1-86400
	PasswordMinLength      int                 `json:"password_min_length,optional,default=8"` // 密码最少 Unicode 字符数；0 默认 8，范围 6-72，另受 bcrypt 的 72 字节上限约束。
	LoginRateLimit         AuthRateLimitConfig `json:"login_rate_limit,optional"`              // 登录限流配置
	RegisterRateLimit      AuthRateLimitConfig `json:"register_rate_limit,optional"`           // 注册限流配置
}

// AuthRateLimitConfig 定义前台认证入口的 Redis 限流参数。
type AuthRateLimitConfig struct {
	Enabled       bool `json:"enabled,optional"`        // 是否启用限流
	WindowSeconds int  `json:"window_seconds,optional"` // 统计窗口，单位秒
	MaxAttempts   int  `json:"max_attempts,optional"`   // 窗口内最大尝试次数
	LockSeconds   int  `json:"lock_seconds,optional"`   // 超限后的锁定时间，单位秒
}

// UserConfig 定义业务用户物理路由配置。
type UserConfig struct {
	RouteShardCount int `json:"route_shard_count,optional,default=1"` // 用户物理分片数量，由当前部署方案校验允许值
}

// OpsConfig 定义运维级接口保护配置。
type OpsConfig struct {
	ConfigReloadToken      string   `json:"config_reload_token,optional"`       // 配置热加载接口运维令牌
	ConfigReloadAllowedIPs []string `json:"config_reload_allowed_ips,optional"` // 配置热加载允许的内网 IP 或 CIDR
}

// InternalServerConfig 定义只注册内网路由的独立监听器。
type InternalServerConfig struct {
	Host         string `json:"host"`                    // 监听 IP；生产环境必须为回环或私有地址
	Port         int    `json:"port"`                    // 独立监听端口，不得与公网端口相同
	CertFile     string `json:"cert_file,optional"`      // mTLS 服务端证书文件
	KeyFile      string `json:"key_file,optional"`       // mTLS 服务端私钥文件
	ClientCAFile string `json:"client_ca_file,optional"` // mTLS 客户端 CA 文件
}

// Config 是前台 API 服务总配置。
type Config struct {
	rest.RestConf                       // go-zero HTTP 服务配置
	AppID          string               `json:"app_id,optional"`                       // 站点/应用 ID
	AppKey         string               `json:"app_key,optional"`                      // 持久数据根密钥，用于用户敏感字段加解密与查询哈希
	InstanceID     string               `json:"instance_id,optional"`                  // 当前实例 ID；为空时使用主机名
	TrustedProxies []string             `json:"trusted_proxies,optional"`              // 允许提供 X-Forwarded-For 的反向代理 IP/CIDR
	Snowflake      SnowflakeConfig      `json:"snowflake,optional"`                    // 分布式雪花 ID 配置
	JwtSecret      string               `json:"jwt_secret"`                            // JWT 签名密钥
	JwtExpiresIn   int64                `json:"jwt_expires_in,optional,default=86400"` // JWT 过期秒数；0 使用 24 小时默认值，最大 30 天
	Auth           AuthConfig           `json:"auth,optional"`                         // 前台用户认证配置
	User           UserConfig           `json:"user,optional"`                         // 业务用户写入路由配置
	HotReload      HotReloadConfig      `json:"hot_reload,optional"`                   // 配置热加载配置
	ConfigFiles    ConfigFilesConfig    `json:"config_files,optional"`                 // 外部配置文件入口
	Security       SecurityConfig       `json:"security,optional"`                     // 签名验签和加解密配置
	Collector      CollectorConfig      `json:"collector,optional"`                    // 通用收集器配置
	Ops            OpsConfig            `json:"ops,optional"`                          // 运维级接口保护配置
	InternalServer InternalServerConfig `json:"internal_server,optional"`              // 内网路由独立监听配置
	Observability  ObservabilityConfig  `json:"observability,optional"`                // 日志与链路追踪配置
	Alert          AlertConfig          `json:"alert,optional"`                        // 外部运行异常告警配置
	MySQL          MySQLConfig          `json:"mysql,optional"`                        // 默认主库 MySQL 配置
	SiteMySQL      SiteMySQLConfig      `json:"site_mysql,optional"`                   // 可选命名扩展库配置
	Redis          RedisConfig          `json:"redis"`                                 // Redis 连接与连接池配置
}
