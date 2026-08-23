package redisx

import (
	"context"
	"crypto/tls"
	"net"
	"strings"
	"time"

	"api/internal/config"
	"api/internal/infra/loggerx"

	"github.com/Is999/go-utils/errors"
	"github.com/redis/go-redis/v9"
	"github.com/redis/go-redis/v9/maintnotifications"
	"github.com/zeromicro/go-zero/core/logx"
)

// Redis 命令和脚本缓存错误常量。
const (
	redisCommandEvalSHA   = "evalsha"    // EVALSHA 命令名，用于识别脚本缓存未命中
	redisCommandEvalSHARO = "evalsha_ro" // 只读 EVALSHA 命令名
	redisNoScriptPrefix   = "NOSCRIPT"   // Redis 脚本缓存未命中错误前缀
)

// New 创建 Redis 客户端，并注册统一的命令耗时观测 hook。
func New(ctx context.Context, cfg config.RedisConfig, obs config.ObservabilityConfig) (redis.UniversalClient, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, errors.Tag(err)
	}
	addrs, err := resolveAddrs(cfg.Addrs)
	if err != nil {
		return nil, errors.Tag(err)
	}
	addrMap, err := resolveAddrMap(cfg.AddrMap)
	if err != nil {
		return nil, errors.Tag(err)
	}
	// 逐个探测声明地址，避免客户端仅因单个种子可用而掩盖错误节点。
	if err := pingConfiguredAddrs(ctx, cfg, addrs, addrMap); err != nil {
		return nil, errors.Tag(err)
	}

	var rdb redis.UniversalClient
	if !isClusterMode(cfg) {
		option := &redis.Options{
			Addr:                  addrs[0],
			Password:              cfg.Password,
			DB:                    cfg.DB,
			PoolSize:              cfg.PoolSize,
			MinIdleConns:          cfg.PoolSize / 5,
			DisableIdentity:       true,
			ContextTimeoutEnabled: true,
			Protocol:              2,
			MaintNotificationsConfig: &maintnotifications.Config{
				Mode: maintnotifications.ModeDisabled,
			},
		}
		applyTLSConfig(option, cfg)
		rdb = redis.NewClient(option)
	} else {
		clusterOpts := &redis.ClusterOptions{
			Addrs:                 addrs,
			Password:              cfg.Password,
			PoolSize:              cfg.PoolSize,
			MinIdleConns:          cfg.PoolSize / 5,
			DisableIdentity:       true,
			ContextTimeoutEnabled: true,
			Protocol:              2,
			MaintNotificationsConfig: &maintnotifications.Config{
				Mode: maintnotifications.ModeDisabled,
			},
		}
		applyClusterTLSConfig(clusterOpts, cfg)
		if len(addrMap) > 0 {
			clusterOpts.NewClient = func(opt *redis.Options) *redis.Client {
				// 每个节点复制连接参数后改写地址，避免共享可变 Option。
				cloned := *opt
				cloned.Addr = rewriteClusterAddr(opt.Addr, addrMap)
				return redis.NewClient(&cloned)
			}
		}
		rdb = redis.NewClusterClient(clusterOpts)
	}

	// hook 在正式健康检查前注册，使首条客户端命令进入统一观测。
	rdb.AddHook(newHook(time.Duration(obs.RedisSlowMs) * time.Millisecond))
	// 再用正式客户端验证最终连接配置。
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		return nil, errors.Tag(err)
	}
	return rdb, nil
}

// validateConfig 为直接调用 Redis 基础设施的入口执行严格启动校验，禁止绕过 bootstrap 后静默改写配置。
func validateConfig(cfg config.RedisConfig) error {
	if cfg.Type != "single" && cfg.Type != "cluster" {
		return errors.Errorf("redis.type 仅支持 single/cluster，且不能为空")
	}
	if cfg.PoolSize <= 0 || cfg.PoolSize > config.MaxRedisPoolSize {
		return errors.Errorf("redis.pool_size 必须在 1-%d 之间", config.MaxRedisPoolSize)
	}
	if len(cfg.Addrs) > config.MaxRedisAddressCount {
		return errors.Errorf("redis.addrs 不能超过 %d 个地址", config.MaxRedisAddressCount)
	}
	if len(cfg.AddrMap) > config.MaxRedisAddressMapCount {
		return errors.Errorf("redis.addr_map 不能超过 %d 项", config.MaxRedisAddressMapCount)
	}
	if cfg.Type == "single" {
		if len(cfg.Addrs) != 1 {
			return errors.Errorf("redis.type=single 时 addrs 必须且只能配置一个地址")
		}
		if len(cfg.AddrMap) > 0 {
			return errors.Errorf("redis.addr_map 仅支持 cluster 模式")
		}
	}
	if cfg.Type == "cluster" && cfg.DB != 0 {
		return errors.Errorf("redis.type=cluster 时 db 必须为 0")
	}
	if cfg.DB < 0 {
		return errors.Errorf("redis.db 不能小于 0")
	}
	return nil
}

// resolveAddrs 校验 Redis 地址列表，任何空白或重复值都必须阻断启动。
func resolveAddrs(addrs []string) ([]string, error) {
	if len(addrs) == 0 {
		return nil, errors.Errorf("缺少 Redis 地址配置")
	}
	result := make([]string, 0, len(addrs))
	seen := make(map[string]struct{}, len(addrs))
	for _, addr := range addrs {
		trimmed := strings.TrimSpace(addr)
		if trimmed == "" || trimmed != addr {
			return nil, errors.Errorf("Redis 地址不能包含空值或首尾空白")
		}
		if _, ok := seen[trimmed]; ok {
			return nil, errors.Errorf("Redis 地址不能重复: %s", trimmed)
		}
		seen[trimmed] = struct{}{}
		result = append(result, trimmed)
	}
	return result, nil
}

// pingConfiguredAddrs 在启动期探测声明的 Redis 地址，提前暴露不可达配置。
func pingConfiguredAddrs(ctx context.Context, cfg config.RedisConfig, addrs []string, addrMap map[string]string) error {
	// Cluster 不支持按 DB 编号隔离，预探测与正式客户端都固定使用 DB 0。
	db := cfg.DB
	if isClusterMode(cfg) {
		db = 0
	}
	for idx, addr := range addrs {
		pingAddr := addr
		if isClusterMode(cfg) {
			pingAddr = rewriteClusterAddr(addr, addrMap)
		}
		option := &redis.Options{
			Addr:                  pingAddr,
			Password:              cfg.Password,
			DB:                    db,
			PoolSize:              1,
			DisableIdentity:       true,
			ContextTimeoutEnabled: true,
			Protocol:              2,
			MaintNotificationsConfig: &maintnotifications.Config{
				Mode: maintnotifications.ModeDisabled,
			},
		}
		applyTLSConfig(option, cfg)
		// 单地址探测结束立即关闭，不让短连接占用正式客户端连接池。
		client := redis.NewClient(option)
		err := client.Ping(ctx).Err()
		_ = client.Close()
		if err != nil {
			return errors.Wrapf(err, "探测 Redis 地址[%d]=%s 失败", idx, pingAddr)
		}
	}
	return nil
}

// resolveAddrMap 校验 Redis Cluster 地址改写表，不接受运行期裁剪或丢弃错误项。
func resolveAddrMap(raw map[string]string) (map[string]string, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	result := make(map[string]string, len(raw))
	for key, value := range raw {
		trimmedKey := strings.TrimSpace(key)
		trimmedValue := strings.TrimSpace(value)
		if trimmedKey == "" || trimmedValue == "" || trimmedKey != key || trimmedValue != value {
			return nil, errors.Errorf("Redis 地址改写表不能包含空值或首尾空白")
		}
		result[trimmedKey] = trimmedValue
	}
	return result, nil
}

// isClusterMode 只按经过校验的规范 type 判断 Redis 模式。
func isClusterMode(cfg config.RedisConfig) bool {
	return cfg.Type == "cluster"
}

// applyTLSConfig 为单机 Redis 客户端应用 TLS 配置。
func applyTLSConfig(option *redis.Options, cfg config.RedisConfig) {
	if option == nil || !cfg.TLS {
		return
	}
	option.TLSConfig = &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: cfg.TLSInsecureSkipVerify,
	}
}

// applyClusterTLSConfig 为 Redis Cluster 客户端应用 TLS 配置。
func applyClusterTLSConfig(option *redis.ClusterOptions, cfg config.RedisConfig) {
	if option == nil || !cfg.TLS {
		return
	}
	option.TLSConfig = &tls.Config{
		MinVersion:         tls.VersionTLS12,
		InsecureSkipVerify: cfg.TLSInsecureSkipVerify,
	}
}

// rewriteClusterAddr 按地址改写表替换 Redis Cluster 节点地址。
func rewriteClusterAddr(addr string, addrMap map[string]string) string {
	if len(addrMap) == 0 {
		return addr
	}
	if mapped, ok := addrMap[addr]; ok && mapped != "" {
		// 完整 host:port 映射优先于下方仅替换主机的规则。
		return mapped
	}
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return addr
	}
	mappedHost, ok := addrMap[host]
	if !ok || mappedHost == "" {
		return addr
	}
	// 裸 IPv6 自带冒号，只有完整 host:port 才能替换原端口。
	if _, _, err := net.SplitHostPort(mappedHost); err == nil {
		return mappedHost
	}
	return net.JoinHostPort(mappedHost, port)
}

// hook 负责把 go-redis 命令耗时转成结构化日志。
type hook struct {
	slowThreshold time.Duration // 慢 Redis 命令阈值
}

// newHook 创建 Redis 命令日志 hook。
func newHook(slowThreshold time.Duration) hook {
	return hook{slowThreshold: slowThreshold}
}

// DialHook 保持默认拨号流程，仅满足 go-redis Hook 接口。
func (h hook) DialHook(next redis.DialHook) redis.DialHook {
	return next
}

// ProcessHook 记录单条 Redis 命令耗时，错误原样交给调用方处理。
func (h hook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		begin := time.Now()
		err := next(ctx, cmd)
		h.logSlowProcess(ctx, time.Since(begin), err, cmd)
		// hook 只做观测，必须原样透传 redis.Nil 等哨兵错误。
		return err
	}
}

// ProcessPipelineHook 记录 Redis Pipeline 耗时，错误原样交给调用方处理。
func (h hook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return func(ctx context.Context, cmds []redis.Cmder) error {
		begin := time.Now()
		err := next(ctx, cmds)
		if err != nil {
			// pipeline hook 不能改写底层错误语义，避免上层依赖的直接比较失效。
			return err
		}
		if h.slowThreshold > 0 && time.Since(begin) > h.slowThreshold {
			fields := []logx.LogField{
				logx.Field("latency_ms", time.Since(begin).Milliseconds()),
				logx.Field("commands", pipelineNames(cmds)),
			}
			loggerx.Sloww(ctx, "缓存 管道耗时较高", fields...)
		}
		return nil
	}
}

// logSlowProcess 按慢阈值输出单条 Redis 命令日志。
func (h hook) logSlowProcess(ctx context.Context, duration time.Duration, err error, cmd redis.Cmder) {
	// 仅记录命令名和参数数量，缓存值与凭据不进入慢日志。
	fields := []logx.LogField{
		logx.Field("latency_ms", duration.Milliseconds()),
		logx.Field("cmd", cmd.FullName()),
		logx.Field("arg_count", max(len(cmd.Args())-1, 0)),
	}
	switch {
	case isRedisScriptCacheMiss(err, cmd):
		// Script.Run 会继续 EVAL，NOSCRIPT 不是本次业务执行的最终结果。
		return
	case h.slowThreshold > 0 && duration > h.slowThreshold:
		loggerx.Sloww(ctx, "缓存 命令耗时较高", fields...)
	}
}

// isRedisScriptCacheMiss 判断 EVALSHA 脚本缓存未命中，避免误记为异常。
func isRedisScriptCacheMiss(err error, cmd redis.Cmder) bool {
	if err == nil || cmd == nil {
		return false
	}
	switch strings.ToLower(strings.TrimSpace(cmd.FullName())) {
	case redisCommandEvalSHA, redisCommandEvalSHARO:
	default:
		return false
	}
	if errors.Is(err, redis.ErrNoScript) || redis.HasErrorPrefix(err, redisNoScriptPrefix) {
		return true
	}
	message := err.Error()
	return strings.HasPrefix(message, redisNoScriptPrefix)
}

// pipelineNames 提取 Pipeline 命令名称，避免日志记录完整参数。
func pipelineNames(cmds []redis.Cmder) []string {
	names := make([]string, 0, len(cmds))
	for _, cmd := range cmds {
		names = append(names, cmd.FullName())
	}
	return names
}
