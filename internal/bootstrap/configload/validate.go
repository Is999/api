package configload

import (
	"net"
	"net/netip"
	"sort"
	"strconv"
	"strings"

	"api/common/idgen"
	"api/internal/bootstrap/configload/validators"
	"api/internal/config"
	"api/internal/security"
	"api/internal/sharding"

	utils "github.com/Is999/go-utils"
	"github.com/Is999/go-utils/errors"
)

// 启动配置校验边界常量。
const (
	minJWTSecretLength            = 16    // JWT 密钥最小长度，避免明显弱配置启动
	minAppKeyLength               = 16    // 持久数据根密钥最小长度，所有模式均用于联系方式加密与查询哈希
	minOpsTokenLength             = 16    // 运维令牌生产环境最小长度
	minPasswordLength             = 6     // 前台密码最小允许长度下限
	maxPasswordBytes              = 72    // bcrypt 接受的密码最大字节数
	maxConfigKeySegmentBytes      = 64    // Redis key 配置片段最大长度
	maxHotReloadIntervalSeconds   = 3600  // 配置文件轮询最大间隔
	defaultUserRouteShardCount    = 1     // 业务用户默认保持单张物理表
	maxAuthRateLimitWindowSeconds = 3600  // 认证限流最大统计窗口
	maxAuthRateLimitLockSeconds   = 86400 // 认证限流最大锁定时长
	maxAuthRateLimitAttempts      = 1000  // 认证限流最大尝试次数
	maxSiteMySQLCount             = 16    // 命名扩展库数量上限，限制启动串行探测和连接池总规模
)

// Validate 校验启动必填配置，避免服务以明显错误状态启动。
func Validate(c config.Config) error {
	_, err := validateAndCompileSecurity(c)
	return errors.Tag(err)
}

// validateAndCompileSecurity 返回通过全部配置校验的只读密钥注册表。
func validateAndCompileSecurity(c config.Config) (*security.KeyRegistry, error) {
	// 先完成无文件 I/O 的校验，避免明显配置错误触发密钥读取。
	if err := validateBeforeSecurity(c); err != nil {
		return nil, errors.Tag(err)
	}
	if err := validators.ValidateProduction(c); err != nil {
		return nil, errors.Tag(err)
	}
	securityKeys, err := validators.CompileSecurityRegistry(c)
	if err != nil {
		return nil, errors.Tag(err)
	}
	return securityKeys, nil
}

// validateBeforeSecurity 校验不读取密钥文件的配置。
func validateBeforeSecurity(c config.Config) error {
	if c.Host == "" || c.Host != strings.TrimSpace(c.Host) {
		return errors.Errorf("Host 必须配置且不能包含首尾空白")
	}
	if c.Port < 1 || c.Port > 65535 {
		return errors.Errorf("Port 必须在 1-65535 之间")
	}
	if !config.ValidMode(c.Mode) {
		return errors.Errorf("Mode 仅支持 dev/test/rt/pre/pro，且必须使用小写规范值")
	}
	if c.JwtSecret != strings.TrimSpace(c.JwtSecret) || len(c.JwtSecret) < minJWTSecretLength {
		return errors.Errorf("jwt_secret 长度不能小于 %d", minJWTSecretLength)
	}
	if c.AppKey != strings.TrimSpace(c.AppKey) || len(c.AppKey) < minAppKeyLength {
		return errors.Errorf("app_key 长度不能小于 %d，且不能包含首尾空白", minAppKeyLength)
	}
	if c.JwtExpiresIn <= 0 || c.JwtExpiresIn > config.MaxJWTExpiresInSeconds {
		return errors.Errorf("jwt_expires_in 必须在 1-%d 秒之间", config.MaxJWTExpiresInSeconds)
	}
	if strings.TrimSpace(c.AppID) == "" {
		return errors.Errorf("app_id 不能为空")
	}
	if !validConfigKeySegment(c.AppID) {
		return errors.Errorf("app_id 只能包含字母、数字、点、下划线或短横线，且长度不能超过 %d", maxConfigKeySegmentBytes)
	}
	if c.InstanceID != "" && !validConfigKeySegment(c.InstanceID) {
		return errors.Errorf("instance_id 只能包含字母、数字、点、下划线或短横线，且长度不能超过 %d", maxConfigKeySegmentBytes)
	}
	// 公共库会忽略空白项和重复项，先按项目契约拒绝。
	if err := validateTrustedProxies(c.TrustedProxies); err != nil {
		return errors.Tag(err)
	}
	if _, err := utils.NewTrustedProxies(c.TrustedProxies...); err != nil {
		return errors.Wrap(err, "trusted_proxies 配置非法")
	}
	// 认证标识与会话期限在中间件初始化前完成校验。
	if c.Auth.Issuer == "" || c.Auth.Issuer != strings.TrimSpace(c.Auth.Issuer) {
		return errors.Errorf("auth.issuer 不能为空或包含首尾空白")
	}
	if c.Auth.SessionTTLSeconds < 0 {
		return errors.Errorf("auth.session_ttl_seconds 不能小于 0")
	}
	// Redis 部署模式在会话存储创建前完成校验。
	if err := validateRedisConfig(c.Redis); err != nil {
		return errors.Tag(err)
	}
	if c.Auth.PasswordMinLength < minPasswordLength || c.Auth.PasswordMinLength > maxPasswordBytes {
		return errors.Errorf("auth.password_min_length 必须在 %d-%d 之间", minPasswordLength, maxPasswordBytes)
	}
	// 热加载轮询参数只做边界校验，不在此处启动 watcher。
	if interval := c.HotReload.CheckIntervalSeconds; interval < 0 || interval > maxHotReloadIntervalSeconds {
		return errors.Errorf("hot_reload.check_interval_seconds 必须为 0 或 1-%d", maxHotReloadIntervalSeconds)
	}
	// 可观测参数在 trace 与慢调用组件初始化前完成校验。
	if ratio := c.Observability.SampleRatio; ratio < 0 || ratio > 1 {
		return errors.Errorf("observability.sample_ratio 必须在 0-1 之间")
	}
	if name := c.Observability.ServiceName; name != strings.TrimSpace(name) {
		return errors.Errorf("observability.service_name 不能包含首尾空白")
	}
	if protocol := c.Observability.OTLPProtocol; protocol != "" && protocol != "grpc" && protocol != "http" {
		return errors.Errorf("observability.otlp_protocol 仅支持 grpc/http，且必须使用小写规范值")
	}
	if err := validateOTLPEndpoint(c.Observability.OTLPEndpoint); err != nil {
		return errors.Tag(err)
	}
	if threshold := c.Observability.SlowSQLMs; threshold < 0 || threshold > config.MaxSlowThresholdMilliseconds {
		return errors.Errorf("observability.slow_sql_ms 必须为 0 或 1-%d 毫秒", config.MaxSlowThresholdMilliseconds)
	}
	if threshold := c.Observability.RedisSlowMs; threshold < 0 || threshold > config.MaxSlowThresholdMilliseconds {
		return errors.Errorf("observability.redis_slow_ms 必须为 0 或 1-%d 毫秒", config.MaxSlowThresholdMilliseconds)
	}
	if ttl := c.Auth.ProfileCacheTTLSeconds; ttl <= 0 || ttl > config.MaxProfileCacheTTLSeconds {
		return errors.Errorf("auth.profile_cache_ttl_seconds 必须在 1-%d 秒之间", config.MaxProfileCacheTTLSeconds)
	}
	// 数据库配置在连接池创建前完成校验。
	if err := validateMySQLConfigs(c.MySQL, c.SiteMySQL); err != nil {
		return errors.Tag(err)
	}
	// 雪花参数在节点租约申请前完成校验。
	if err := validateSnowflakeConfig(c.Snowflake); err != nil {
		return errors.Tag(err)
	}
	// 用户分片配置在路由表创建前完成校验。
	if err := validateUserConfig(c.User); err != nil {
		return errors.Tag(err)
	}
	if err := validateAuthRateLimitConfig("auth.login_rate_limit", c.Auth.LoginRateLimit); err != nil {
		return errors.Tag(err)
	}
	if err := validateAuthRateLimitConfig("auth.register_rate_limit", c.Auth.RegisterRateLimit); err != nil {
		return errors.Tag(err)
	}
	// Collector 路由与 writer 参数在组件注册前完成校验。
	if err := validators.ValidateCollector(c); err != nil {
		return errors.Tag(err)
	}
	// 运维鉴权参数在内网路由注册前完成校验。
	if err := validateOpsConfig(c.Ops); err != nil {
		return errors.Tag(err)
	}
	// 内网监听地址在第二监听器创建前完成校验。
	if err := validateInternalServer(c.RestConf, c.InternalServer, c.Mode); err != nil {
		return errors.Tag(err)
	}
	// 告警参数在运行时 hook 注册前完成校验。
	if err := validateAlertConfig(c.Alert); err != nil {
		return errors.Tag(err)
	}
	return nil
}

// validateOTLPEndpoint 固定 host:port 形态，避免同一配置同时按 URL、路径或裁剪后地址解释。
func validateOTLPEndpoint(endpoint string) error {
	if endpoint == "" {
		return nil
	}
	if endpoint != strings.TrimSpace(endpoint) {
		return errors.Errorf("observability.otlp_endpoint 不能包含首尾空白")
	}
	host, portText, err := net.SplitHostPort(endpoint)
	if err != nil || host == "" {
		return errors.Errorf("observability.otlp_endpoint 必须使用 host:port 形态")
	}
	port, err := strconv.Atoi(portText)
	if err != nil || port < 1 || port > 65535 {
		return errors.Errorf("observability.otlp_endpoint 端口必须在 1-65535 之间")
	}
	return nil
}

// validateTrustedProxies 拒绝公共库会静默忽略的空项、首尾空白和重复规则。
func validateTrustedProxies(values []string) error {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value == "" || value != strings.TrimSpace(value) {
			return errors.Errorf("trusted_proxies 不能包含空项或首尾空白")
		}
		if _, exists := seen[value]; exists {
			return errors.Errorf("trusted_proxies 不能包含重复规则: %s", value)
		}
		seen[value] = struct{}{}
	}
	return nil
}

// validateRedisConfig 校验 Redis 模式与地址语义，禁止拼写错误时静默切换客户端类型。
func validateRedisConfig(cfg config.RedisConfig) error {
	if len(cfg.Addrs) > config.MaxRedisAddressCount {
		return errors.Errorf("redis.addrs 不能超过 %d 个地址", config.MaxRedisAddressCount)
	}
	// 地址按配置原值去重，禁止客户端静默裁剪或重复建连。
	seenAddresses := make(map[string]struct{}, len(cfg.Addrs))
	for _, rawAddress := range cfg.Addrs {
		address := strings.TrimSpace(rawAddress)
		if address == "" || address != rawAddress {
			return errors.Errorf("redis.addrs 不能包含空地址或首尾空白")
		}
		if _, exists := seenAddresses[address]; exists {
			return errors.Errorf("redis.addrs 不能包含重复地址: %s", address)
		}
		seenAddresses[address] = struct{}{}
	}
	if len(cfg.Addrs) == 0 {
		return errors.Errorf("redis.addrs 不能为空")
	}
	// 连接池和地址映射限制启动后的连接与路由规模。
	if cfg.PoolSize <= 0 || cfg.PoolSize > config.MaxRedisPoolSize {
		return errors.Errorf("redis.pool_size 必须在 1-%d 之间", config.MaxRedisPoolSize)
	}
	if len(cfg.AddrMap) > config.MaxRedisAddressMapCount {
		return errors.Errorf("redis.addr_map 不能超过 %d 项", config.MaxRedisAddressMapCount)
	}
	// 单机与集群使用不同的数据库和地址映射语义。
	switch cfg.Type {
	case "single":
		if len(cfg.Addrs) != 1 {
			return errors.Errorf("redis.type=single 时 addrs 必须且只能配置一个地址")
		}
		if len(cfg.AddrMap) > 0 {
			return errors.Errorf("redis.addr_map 仅支持 cluster 模式")
		}
	case "cluster":
		if cfg.DB != 0 {
			return errors.Errorf("redis.type=cluster 时 db 必须为 0")
		}
	default:
		return errors.Errorf("redis.type 仅支持 single/cluster，且必须使用小写规范值")
	}
	if cfg.DB < 0 {
		return errors.Errorf("redis.db 不能小于 0")
	}
	return nil
}

// validateMySQLConfigs 校验默认库和命名扩展库，所有连接都必须使用有界连接池与规范名称。
func validateMySQLConfigs(main config.MySQLConfig, sites config.SiteMySQLConfig) error {
	if err := validateMySQLConfig("mysql", main); err != nil {
		return errors.Tag(err)
	}
	if len(sites) > maxSiteMySQLCount {
		return errors.Errorf("site_mysql 不能超过 %d 个命名扩展库", maxSiteMySQLCount)
	}

	names := make([]string, 0, len(sites))
	for name := range sites {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if name != strings.ToLower(name) || strings.EqualFold(name, "main") || !validConfigKeySegment(name) {
			return errors.Errorf("site_mysql.%s 名称必须使用无首尾空白的小写安全字符，且长度不能超过 %d", name, maxConfigKeySegmentBytes)
		}
		if err := validateMySQLConfig("site_mysql."+name, sites[name]); err != nil {
			return errors.Tag(err)
		}
	}
	return nil
}

// validateMySQLConfig 校验单个逻辑库的 DSN 列表和连接池硬边界，禁止运行时静默降级为无限连接。
func validateMySQLConfig(name string, cfg config.MySQLConfig) error {
	// 写库 DSN 必须是唯一规范源，读库不能通过空白差异形成重复配置。
	writeDSN := strings.TrimSpace(cfg.WriteDataSource)
	if writeDSN == "" {
		return errors.Errorf("%s.write_data_source 不能为空", name)
	}
	if writeDSN != cfg.WriteDataSource {
		return errors.Errorf("%s.write_data_source 不能包含首尾空白", name)
	}
	if len(cfg.ReadDataSources) > config.MaxMySQLReadDataSourceCount {
		return errors.Errorf("%s.read_data_sources 不能超过 %d 个", name, config.MaxMySQLReadDataSourceCount)
	}
	// 读库逐项去重，避免同一实例被重复加入随机选路池。
	seen := make(map[string]struct{}, len(cfg.ReadDataSources))
	for index, rawDSN := range cfg.ReadDataSources {
		dsn := strings.TrimSpace(rawDSN)
		if dsn == "" || dsn != rawDSN {
			return errors.Errorf("%s.read_data_sources[%d] 不能为空或包含首尾空白", name, index)
		}
		if dsn == writeDSN {
			return errors.Errorf("%s.read_data_sources[%d] 不能与写库 DSN 重复", name, index)
		}
		if _, exists := seen[dsn]; exists {
			return errors.Errorf("%s.read_data_sources[%d] 与其它读库 DSN 重复", name, index)
		}
		seen[dsn] = struct{}{}
	}
	// 连接数和生命周期都必须有硬上限，防止错误配置耗尽数据库资源。
	if cfg.MaxOpenConns <= 0 || cfg.MaxOpenConns > config.MaxMySQLOpenConns {
		return errors.Errorf("%s.max_open_conns 必须在 1-%d 之间", name, config.MaxMySQLOpenConns)
	}
	if cfg.MaxIdleConns < 0 || cfg.MaxIdleConns > cfg.MaxOpenConns {
		return errors.Errorf("%s.max_idle_conns 必须在 0-max_open_conns 之间", name)
	}
	if cfg.ConnMaxLifetime <= 0 || cfg.ConnMaxLifetime > config.MaxMySQLConnLifetimeSeconds {
		return errors.Errorf("%s.conn_max_lifetime 必须在 1-%d 秒之间", name, config.MaxMySQLConnLifetimeSeconds)
	}
	return nil
}

// validConfigKeySegment 校验会参与 Redis key 或运行时注册表的短标识。
func validConfigKeySegment(value string) bool {
	trimmed := strings.TrimSpace(value)
	if value != trimmed || trimmed == "" || len(trimmed) > maxConfigKeySegmentBytes {
		return false
	}
	for _, char := range trimmed {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') ||
			(char >= '0' && char <= '9') || char == '.' || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}

// validateAlertConfig 校验外部告警通道的最小可用配置。
func validateAlertConfig(cfg config.AlertConfig) error {
	if !cfg.Lark.Enabled {
		return nil
	}
	if err := validateExclusiveConfigValue("alert.lark.webhook_url", cfg.Lark.WebhookURL, "alert.lark.webhook_url_ref", cfg.Lark.WebhookURLRef, true); err != nil {
		return errors.Tag(err)
	}
	if err := validateExclusiveConfigValue("alert.lark.secret", cfg.Lark.Secret, "alert.lark.secret_ref", cfg.Lark.SecretRef, false); err != nil {
		return errors.Tag(err)
	}
	if cfg.Lark.TimeoutSeconds < 0 || cfg.Lark.TimeoutSeconds > config.MaxLarkAlertTimeoutSeconds {
		return errors.Errorf("alert.lark.timeout_seconds 必须为 0 或 1-%d", config.MaxLarkAlertTimeoutSeconds)
	}
	if cfg.Lark.MaxErrorBytes < 0 {
		return errors.Errorf("alert.lark.max_error_bytes 不能小于 0")
	}
	return nil
}

// validateExclusiveConfigValue 校验明文值和文件引用只能选择一种来源，避免静默优先级形成双轨配置。
func validateExclusiveConfigValue(valueName string, value string, refName string, ref string, required bool) error {
	if value != strings.TrimSpace(value) {
		return errors.Errorf("%s 不能包含首尾空白", valueName)
	}
	if ref != strings.TrimSpace(ref) {
		return errors.Errorf("%s 不能包含首尾空白", refName)
	}
	if value != "" && ref != "" {
		return errors.Errorf("%s 与 %s 只能配置一个", valueName, refName)
	}
	if required && value == "" && ref == "" {
		return errors.Errorf("%s 与 %s 必须配置一个", valueName, refName)
	}
	return nil
}

// validateSnowflakeConfig 校验分布式雪花 ID worker 配置。
func validateSnowflakeConfig(cfg config.SnowflakeConfig) error {
	if cfg.Redis.Enabled {
		if err := validateSnowflakeRedisConfig(cfg); err != nil {
			return errors.Tag(err)
		}
	} else if _, err := resolveSnowflakeWorkerID(cfg); err != nil {
		return errors.Tag(err)
	}
	if err := validateIDSegmentConfig(cfg); err != nil {
		return errors.Tag(err)
	}
	return nil
}

// validateSnowflakeRedisConfig 校验 Redis 租约 node_id 分配参数。
func validateSnowflakeRedisConfig(cfg config.SnowflakeConfig) error {
	if cfg.WorkerID != nil {
		return errors.Errorf("snowflake.redis.enabled=true 时不能同时配置 snowflake.worker_id")
	}
	redisCfg := normalizeSnowflakeRedisConfig(cfg.Redis)
	if !validConfigKeySegment(redisCfg.Scope) {
		return errors.Errorf("snowflake.redis.scope 只能包含安全 key 字符且长度不能超过 %d", maxConfigKeySegmentBytes)
	}
	if redisCfg.LeaseSeconds < minSnowflakeRedisLeaseSeconds || redisCfg.LeaseSeconds > maxSnowflakeRedisLeaseSeconds {
		return errors.Errorf("snowflake.redis.lease_seconds 必须在 %d-%d 之间", minSnowflakeRedisLeaseSeconds, maxSnowflakeRedisLeaseSeconds)
	}
	if redisCfg.RenewIntervalSeconds <= 0 || redisCfg.RenewIntervalSeconds > maxSnowflakeRedisLeaseSeconds {
		return errors.Errorf("snowflake.redis.renew_interval_seconds 必须在 1-%d 之间", maxSnowflakeRedisLeaseSeconds)
	}
	if redisCfg.RenewIntervalSeconds >= redisCfg.LeaseSeconds-redisCfg.RenewIntervalSeconds {
		return errors.Errorf("snowflake.redis.renew_interval_seconds 必须小于 lease_seconds 的一半")
	}
	if err := validateSnowflakeRedisNamespaces(cfg.Redis.Namespaces); err != nil {
		return errors.Tag(err)
	}
	return nil
}

// validateSnowflakeRedisNamespaces 校验业务 namespace 的 node_id 池覆盖配置。
func validateSnowflakeRedisNamespaces(items map[string]config.SnowflakeRedisNamespaceConfig) error {
	for namespace, item := range items {
		if namespace == "" {
			return errors.Errorf("snowflake.redis.namespaces 包含空 namespace")
		}
		if namespace != strings.ToLower(namespace) {
			return errors.Errorf("snowflake.redis.namespaces.%s 必须使用无首尾空白的小写名称", namespace)
		}
		if !validConfigKeySegment(namespace) {
			return errors.Errorf("snowflake.redis.namespaces.%s 包含非法 key 字符", namespace)
		}
		if item.NodeCount < 0 || item.NodeCount > int(idgen.SnowflakeMaxWorkerID+1) {
			return errors.Errorf("snowflake.redis.namespaces.%s.node_count 必须在 0-%d 之间", namespace, idgen.SnowflakeMaxWorkerID+1)
		}
	}
	return nil
}

// validateIDSegmentConfig 校验高吞吐业务 Redis Segment 号段配置。
func validateIDSegmentConfig(cfg config.SnowflakeConfig) error {
	if !cfg.Segment.Enabled {
		return nil
	}
	// 默认值归一化后再校验，确保启动校验与运行时消费使用同一语义。
	segmentCfg := normalizeIDSegmentConfig(cfg.Segment, cfg.Redis)
	if !validConfigKeySegment(segmentCfg.Scope) {
		return errors.Errorf("snowflake.segment.scope 只能包含安全 key 字符且长度不能超过 %d", maxConfigKeySegmentBytes)
	}
	if segmentCfg.AllocateTimeoutSeconds <= 0 || segmentCfg.AllocateTimeoutSeconds > maxIDSegmentAllocateTimeoutSeconds {
		return errors.Errorf("snowflake.segment.allocate_timeout_seconds 必须在 1-%d 之间", maxIDSegmentAllocateTimeoutSeconds)
	}
	// namespace 必须规范且独立满足步长、预取阈值和起始值边界。
	enabledNamespaces := 0
	for namespace, rawItem := range cfg.Segment.Namespaces {
		if namespace == "" {
			return errors.Errorf("snowflake.segment.namespaces 包含空 namespace")
		}
		if namespace != strings.ToLower(namespace) {
			return errors.Errorf("snowflake.segment.namespaces.%s 必须使用无首尾空白的小写名称", namespace)
		}
		if !validConfigKeySegment(namespace) {
			return errors.Errorf("snowflake.segment.namespaces.%s 包含非法 key 字符", namespace)
		}
		item := normalizeIDSegmentNamespaceConfig(rawItem)
		if !item.Enabled {
			continue
		}
		enabledNamespaces++
		if item.Step <= 0 || item.Step > maxIDSegmentStep {
			return errors.Errorf("snowflake.segment.namespaces.%s.step 必须在 1-%d 之间", namespace, maxIDSegmentStep)
		}
		if item.PrefetchThreshold < 0 || item.PrefetchThreshold >= item.Step {
			return errors.Errorf("snowflake.segment.namespaces.%s.prefetch_threshold 必须小于 step 且不能为负数", namespace)
		}
		if item.Start < 0 {
			return errors.Errorf("snowflake.segment.namespaces.%s.start 不能小于 0", namespace)
		}
	}
	// 总开关开启但没有可用业务空间时拒绝启动。
	if enabledNamespaces == 0 {
		return errors.Errorf("snowflake.segment.enabled=true 时必须至少启用一个 namespace")
	}
	return nil
}

// resolveSnowflakeWorkerID 解析配置文件中的显式 worker_id。
func resolveSnowflakeWorkerID(cfg config.SnowflakeConfig) (int64, error) {
	if cfg.WorkerID == nil {
		return idgen.ResolveWorkerID(idgen.SnowflakeWorkerIDUnset)
	}
	return idgen.ResolveWorkerID(*cfg.WorkerID)
}

// ConfigureSnowflakeWorkerID 发布当前进程使用的雪花 ID worker 配置。
func ConfigureSnowflakeWorkerID(cfg config.SnowflakeConfig) error {
	if cfg.Redis.Enabled {
		return errors.New("snowflake.redis.enabled=true 时必须通过 Redis 租约配置雪花 node_id")
	}
	workerID, err := resolveSnowflakeWorkerID(cfg)
	if err != nil {
		return errors.Tag(err)
	}
	return idgen.ConfigureWorkerID(workerID)
}

// validateUserConfig 校验用户物理分片数是否支持平滑二分。
func validateUserConfig(cfg config.UserConfig) error {
	if cfg.RouteShardCount == 0 || sharding.ValidCount(cfg.RouteShardCount) {
		return nil
	}
	return errors.Errorf("user.route_shard_count 仅支持 1/2/4/8/16/32/64/128/256/512/1024")
}

// validateAuthRateLimitConfig 校验认证限流参数是否在可控范围内。
func validateAuthRateLimitConfig(name string, cfg config.AuthRateLimitConfig) error {
	if !cfg.Enabled {
		return nil
	}
	if cfg.WindowSeconds < 0 {
		return errors.Errorf("%s.window_seconds 不能小于 0", name)
	}
	if cfg.WindowSeconds > maxAuthRateLimitWindowSeconds {
		return errors.Errorf("%s.window_seconds 不能大于 %d", name, maxAuthRateLimitWindowSeconds)
	}
	if cfg.MaxAttempts < 0 {
		return errors.Errorf("%s.max_attempts 不能小于 0", name)
	}
	if cfg.MaxAttempts > maxAuthRateLimitAttempts {
		return errors.Errorf("%s.max_attempts 不能大于 %d", name, maxAuthRateLimitAttempts)
	}
	if cfg.LockSeconds < 0 {
		return errors.Errorf("%s.lock_seconds 不能小于 0", name)
	}
	if cfg.LockSeconds > maxAuthRateLimitLockSeconds {
		return errors.Errorf("%s.lock_seconds 不能大于 %d", name, maxAuthRateLimitLockSeconds)
	}
	return nil
}

// validateOpsConfig 校验运维白名单，防止配置层误放行公网来源。
func validateOpsConfig(cfg config.OpsConfig) error {
	// 重载令牌禁止隐式修剪，避免调用方与服务端使用不同字节序列。
	if cfg.ConfigReloadToken != strings.TrimSpace(cfg.ConfigReloadToken) {
		return errors.Errorf("ops.config_reload_token 不能包含首尾空白")
	}
	if len(cfg.ConfigReloadToken) < minOpsTokenLength {
		return errors.Errorf("ops.config_reload_token 长度不能小于 %d", minOpsTokenLength)
	}
	// 白名单按原始配置去重，并分别验证 CIDR 与单 IP。
	seen := make(map[string]struct{}, len(cfg.ConfigReloadAllowedIPs))
	for index, item := range cfg.ConfigReloadAllowedIPs {
		if item == "" || item != strings.TrimSpace(item) {
			return errors.Errorf("ops.config_reload_allowed_ips[%d] 不能为空或包含首尾空白", index)
		}
		if _, exists := seen[item]; exists {
			return errors.Errorf("ops.config_reload_allowed_ips 存在重复地址: %s", item)
		}
		seen[item] = struct{}{}
		if strings.Contains(item, "/") {
			prefix, err := netip.ParsePrefix(item)
			if err != nil {
				return errors.Wrapf(err, "ops.config_reload_allowed_ips CIDR 非法: %s", item)
			}
			if !isInternalConfigPrefix(prefix) {
				return errors.Errorf("ops.config_reload_allowed_ips 不能配置公网 CIDR: %s", item)
			}
			continue
		}
		// 单地址同样只允许内网或本机，禁止误放公网来源。
		addr, err := netip.ParseAddr(item)
		if err != nil {
			return errors.Wrapf(err, "ops.config_reload_allowed_ips IP 非法: %s", item)
		}
		if !isInternalConfigAddr(addr) {
			return errors.Errorf("ops.config_reload_allowed_ips 不能配置公网 IP: %s", item)
		}
	}
	return nil
}

// isInternalConfigAddr 判断配置中的地址是否属于内网或本机。
func isInternalConfigAddr(addr netip.Addr) bool {
	addr = addr.Unmap()
	return addr.IsLoopback() || addr.IsPrivate()
}

// internalConfigPrefixes 使用类型安全构造保存配置校验允许的回环和私网网段。
var internalConfigPrefixes = []netip.Prefix{
	netip.PrefixFrom(netip.AddrFrom4([4]byte{127, 0, 0, 0}), 8),
	netip.PrefixFrom(netip.AddrFrom4([4]byte{10, 0, 0, 0}), 8),
	netip.PrefixFrom(netip.AddrFrom4([4]byte{172, 16, 0, 0}), 12),
	netip.PrefixFrom(netip.AddrFrom4([4]byte{192, 168, 0, 0}), 16),
	netip.PrefixFrom(netip.AddrFrom16([16]byte{15: 1}), 128),
	netip.PrefixFrom(netip.AddrFrom16([16]byte{0xfc}), 7),
}

// isInternalConfigPrefix 确保 CIDR 的完整地址范围都落在回环或私网网段。
func isInternalConfigPrefix(prefix netip.Prefix) bool {
	prefix = prefix.Masked()
	for _, allowed := range internalConfigPrefixes {
		if prefix.Addr().BitLen() == allowed.Addr().BitLen() &&
			prefix.Bits() >= allowed.Bits() &&
			allowed.Contains(prefix.Addr()) {
			return true
		}
	}
	return false
}
