package configload

import (
	"context"
	_ "embed"
	"fmt"
	"hash/crc32"
	"os"
	"strings"
	"sync"
	"time"

	"api/common/embedasset"
	"api/common/idgen"
	keys "api/common/rediskeys"
	"api/common/secureid"
	"api/internal/config"

	"github.com/Is999/go-utils/errors"
	"github.com/redis/go-redis/v9"
	"github.com/zeromicro/go-zero/core/logx"
)

const (
	defaultSnowflakeRedisScope                = "default" // 默认部署级 node_id 池作用域
	defaultSnowflakeRedisLeaseSeconds         = 120       // 默认 node_id 租约 TTL
	defaultSnowflakeRedisRenewIntervalSeconds = 30        // 默认 node_id 续约间隔
	minSnowflakeRedisLeaseSeconds             = 10        // 最小租约 TTL，避免续约抖动导致频繁失效
	maxSnowflakeRedisLeaseSeconds             = 86400     // 最大租约 TTL，避免极端整数转换溢出并限制故障恢复窗口
	snowflakeLeaseOwnerRandomBytes            = 8         // 租约 owner 随机后缀字节数
)

var (
	// snowflakeLeaseRenewScript 在 Redis 内原子比较 owner 并刷新 node_id 租约 TTL。
	snowflakeLeaseRenewScript = redis.NewScript(embedasset.StripLeadingLineComments(snowflakeLeaseRenewScriptText, "--"))
	// snowflakeLeaseRollbackScript 只在本地 worker 尚未激活的失败回滚中删除 Redis 预占租约。
	snowflakeLeaseRollbackScript = redis.NewScript(embedasset.StripLeadingLineComments(snowflakeLeaseRollbackScriptText, "--"))
)

// snowflakeLeaseRenewScriptText 保存雪花 node_id 租约续期 Lua 脚本源码。
// 脚本只操作单个 node_id 租约 key，满足 Redis Cluster 单 key 执行约束。
//
//go:embed assets/snowflake_node_lease_renew.lua
var snowflakeLeaseRenewScriptText string

// snowflakeLeaseRollbackScriptText 保存未激活 node_id 预占回滚脚本源码。
// 脚本只在 owner 匹配时删除 key；已激活租约停机时必须保留 TTL 隔离，不调用本脚本。
//
//go:embed assets/snowflake_node_lease_rollback.lua
var snowflakeLeaseRollbackScriptText string

// SnowflakeLease 表示 ID 生成器持有的运行期资源。
type SnowflakeLease interface {
	// Ready 在启动和健康检查中验证资源未关闭且后端依赖可用。
	Ready(context.Context) error
	// Close 停止后续取号并收口后台运行资源，重复调用必须保持幂等。
	Close(context.Context) error
}

// idGeneratorRuntimeGroup 聚合雪花租约和 Segment 号段等 ID 生成运行期资源。
type idGeneratorRuntimeGroup struct {
	resources []SnowflakeLease // resources 按创建顺序保存，关闭时也按该顺序释放
}

// Ready 检查当前 ID 生成运行资源是否仍可用。
func (g idGeneratorRuntimeGroup) Ready(ctx context.Context) error {
	for _, resource := range g.resources {
		if resource == nil {
			continue
		}
		if err := resource.Ready(ctx); err != nil {
			return errors.Tag(err)
		}
	}
	return nil
}

// snowflakeRedisLeaseManager 管理当前实例按业务命名空间持有的 Redis node_id 租约。
type snowflakeRedisLeaseManager struct {
	client        redis.UniversalClient           // Redis 客户端，由当前 single/cluster 配置选择实现
	cfg           config.SnowflakeRedisConfig     // 已补齐默认值的 Redis 租约配置
	owner         string                          // 当前实例统一租约 owner
	ctx           context.Context                 // ctx 约束运行期 Redis 租约申请生命周期
	cancel        context.CancelFunc              // cancel 在停机时中断持锁的在途租约申请
	resolverToken uint64                          // idgen 动态解析器绑定 token
	mu            sync.Mutex                      // 保护 leases 和 closed
	leases        map[string]*snowflakeRedisLease // 按业务 namespace 保存当前实例持有的 node_id 租约
	closed        bool                            // closed 表示管理器已关闭，不再分配新 namespace
}

// snowflakeRedisLease 保存当前实例在单个业务命名空间抢到的 Redis node_id 租约。
type snowflakeRedisLease struct {
	client        redis.UniversalClient // Redis 客户端，由当前 single/cluster 配置选择实现
	namespace     string                // 业务命名空间，如 user、recharge.order
	key           string                // 当前 node_id 租约 key
	owner         string                // 当前实例租约 owner
	workerID      int64                 // 当前租约对应的雪花 worker_id
	ttl           time.Duration         // Redis 租约 TTL
	renewInterval time.Duration         // 租约续约间隔
	validUntil    time.Time             // validUntil 从最近成功请求发出时刻计算，保留单调时钟。
	workerLease   *idgen.WorkerLease    // workerLease 限定本次本地绑定，旧回包和关闭不能覆盖新实例。
	renewCtx      context.Context       // renewCtx 约束后台 Redis 续约调用生命周期
	cancelRenew   context.CancelFunc    // cancelRenew 在停机时中断正在执行的续约
	stop          chan struct{}         // 关闭续约循环信号
	done          chan struct{}         // 续约循环退出信号
	closeDone     chan struct{}         // 租约关闭流程完成信号
	closeErr      error                 // 首次关闭和租约隔离续期结果
	closeOnce     sync.Once             // 确保租约只释放一次
	releaseOnce   sync.Once             // 确保本地 worker 状态只释放一次
	onRelease     func(string, int64)   // 本地租约丢失后的管理器回调
}

// ConfigureSnowflakeWorker 配置当前进程默认雪花 worker，并按需启用高吞吐 Segment 号段。
func ConfigureSnowflakeWorker(ctx context.Context, cfg config.SnowflakeConfig, client redis.UniversalClient) (SnowflakeLease, error) {
	// 雪花先完成独立注册，Segment 失败时必须撤回本轮已经建立的资源。
	resources := make([]SnowflakeLease, 0, 2)
	if cfg.Redis.Enabled {
		lease, err := newSnowflakeRedisLeaseManager(ctx, cfg.Redis, client)
		if err != nil {
			return nil, errors.Tag(err)
		}
		resources = append(resources, lease)
	} else if err := ConfigureSnowflakeWorkerID(cfg); err != nil {
		return nil, errors.Tag(err)
	}
	if cfg.Segment.Enabled {
		// Segment 只接管显式启用的 namespace，其余仍使用雪花发号。
		segmentManager, err := newRedisSegmentManager(ctx, cfg.Segment, cfg.Redis, client)
		if err != nil {
			_ = closeIDGeneratorResources(ctx, resources)
			return nil, errors.Tag(err)
		}
		if segmentManager != nil {
			resources = append(resources, segmentManager)
		}
	}
	if len(resources) == 0 {
		return nil, nil
	}
	if len(resources) == 1 {
		return resources[0], nil
	}
	return idGeneratorRuntimeGroup{resources: resources}, nil
}

// Close 释放当前进程 ID 生成器持有的所有运行期资源。
func (g idGeneratorRuntimeGroup) Close(ctx context.Context) error {
	return closeIDGeneratorResources(ctx, g.resources)
}

// closeIDGeneratorResources 按创建顺序释放 ID 运行期资源。
func closeIDGeneratorResources(ctx context.Context, resources []SnowflakeLease) error {
	var closeErr error
	for _, resource := range resources {
		if resource == nil {
			continue
		}
		if err := resource.Close(ctx); err != nil && closeErr == nil {
			closeErr = err
		}
	}
	return errors.Tag(closeErr)
}

// normalizeSnowflakeRedisConfig 补齐 Redis 租约 node_id 分配默认值。
func normalizeSnowflakeRedisConfig(cfg config.SnowflakeRedisConfig) config.SnowflakeRedisConfig {
	if cfg.Scope == "" {
		cfg.Scope = defaultSnowflakeRedisScope
	}
	if cfg.LeaseSeconds == 0 {
		cfg.LeaseSeconds = defaultSnowflakeRedisLeaseSeconds
	}
	if cfg.RenewIntervalSeconds == 0 {
		cfg.RenewIntervalSeconds = defaultSnowflakeRedisRenewIntervalSeconds
	}
	if cfg.Namespaces == nil {
		cfg.Namespaces = map[string]config.SnowflakeRedisNamespaceConfig{}
	}
	return cfg
}

// newSnowflakeRedisLeaseManager 创建按业务 namespace 分配 node_id 的 Redis 租约管理器。
func newSnowflakeRedisLeaseManager(ctx context.Context, cfg config.SnowflakeRedisConfig, client redis.UniversalClient) (*snowflakeRedisLeaseManager, error) {
	if client == nil {
		return nil, errors.New("snowflake.redis.enabled=true 时 Redis 客户端不能为空")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateSnowflakeRedisConfig(config.SnowflakeConfig{Redis: cfg}); err != nil {
		return nil, errors.Tag(err)
	}
	cfg = normalizeSnowflakeRedisConfig(cfg)
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, errors.Wrap(err, "检查 snowflake Redis 租约客户端失败")
	}
	owner, err := snowflakeLeaseOwner()
	if err != nil {
		return nil, errors.Wrap(err, "生成 snowflake Redis 租约 owner 失败")
	}
	runtimeCtx, cancel := context.WithCancel(ctx)
	manager := &snowflakeRedisLeaseManager{
		client: client,
		cfg:    cfg,
		owner:  owner,
		ctx:    runtimeCtx,
		cancel: cancel,
		leases: make(map[string]*snowflakeRedisLease),
	}
	// 连接检查和 owner 创建成功后再发布，失败构建不能替换全局解析器。
	manager.resolverToken = idgen.ConfigureWorkerResolver(manager)
	return manager, nil
}

// SnowflakeWorkerID 返回指定业务 namespace 当前实例持有的 worker_id。
func (m *snowflakeRedisLeaseManager) SnowflakeWorkerID(namespace string) (int64, error) {
	if namespace == "" || strings.TrimSpace(namespace) != namespace {
		return 0, errors.New("雪花 ID namespace 不能为空或包含首尾空白")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return 0, errors.Errorf("雪花 Redis 租约管理器已关闭 namespace=%s", namespace)
	}
	if lease, ok := m.leases[namespace]; ok {
		if !lease.workerLease.Valid() {
			return 0, errors.Errorf("雪花 node_id 租约已过期 namespace=%s", namespace)
		}
		return lease.workerID, nil
	}
	// 租约按业务首次发号时惰性申请，管理锁避免同进程重复抢占同一空间。
	lease, err := acquireSnowflakeRedisLease(m.ctx, m.cfg, m.client, m.owner, namespace, m.resolverToken, m.releaseNamespace)
	if err != nil {
		return 0, errors.Tag(err)
	}
	m.leases[namespace] = lease
	return lease.workerID, nil
}

// Ready 检查租约管理器状态和 Redis 连接。
func (m *snowflakeRedisLeaseManager) Ready(ctx context.Context) error {
	if m == nil {
		return errors.New("雪花 Redis 租约管理器未初始化")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.Lock()
	closed := m.closed
	m.mu.Unlock()
	if closed {
		return errors.New("雪花 Redis 租约管理器已关闭")
	}
	if err := m.client.Ping(ctx).Err(); err != nil {
		return errors.Wrap(err, "检查雪花 Redis 租约连接失败")
	}
	return nil
}

// Close 停止本地发号，并让 Redis 租约保留一个完整 TTL 作为跨主机时钟隔离。
func (m *snowflakeRedisLeaseManager) Close(ctx context.Context) error {
	if m == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// 申请过程持有管理器锁以避免同一 namespace 重复抢占，必须先取消 Redis 调用才能及时取得锁并收口。
	if m.cancel != nil {
		m.cancel()
	}
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	leases := make([]*snowflakeRedisLease, 0, len(m.leases))
	for namespace, lease := range m.leases {
		leases = append(leases, lease)
		delete(m.leases, namespace)
	}
	token := m.resolverToken
	m.mu.Unlock()

	// 逐个等待续约退出和 TTL 隔离，网络 I/O 不占用管理器锁。
	var closeErr error
	for _, lease := range leases {
		if err := lease.Close(ctx); err != nil && closeErr == nil {
			closeErr = err
		}
	}
	// token 只撤销本实例的注册，旧实例关闭不得覆盖新解析器。
	idgen.ClearWorkerResolver(token)
	return errors.Tag(closeErr)
}

// releaseNamespace 清理单个业务 namespace 的本地 worker 状态。
func (m *snowflakeRedisLeaseManager) releaseNamespace(namespace string, workerID int64) {
	m.mu.Lock()
	if lease, ok := m.leases[namespace]; ok && lease.workerID == workerID {
		delete(m.leases, namespace)
	}
	m.mu.Unlock()
	idgen.RecordSnowflakeLeaseEvent(namespace, "released")
}

// acquireSnowflakeRedisLease 从 Redis 指定业务 namespace node_id 池申请当前实例独占的 worker_id。
func acquireSnowflakeRedisLease(ctx context.Context, cfg config.SnowflakeRedisConfig, client redis.UniversalClient, owner string, namespace string, token uint64, onRelease func(string, int64)) (*snowflakeRedisLease, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cfg = normalizeSnowflakeRedisConfig(cfg)
	ttl := time.Duration(cfg.LeaseSeconds) * time.Second
	nodeCount := snowflakeRedisNodeCount(cfg, namespace)
	startSeed := fmt.Sprintf("%s:%s:%s", owner, cfg.Scope, namespace)
	// 用实例和业务散列选择起点，避免所有新实例同时竞争最小 node_id。
	start := int64(crc32.ChecksumIEEE([]byte(startSeed)) % uint32(nodeCount))
	for offset := int64(0); offset < nodeCount; offset++ {
		workerID := (start + offset) % nodeCount
		key := keys.SnowflakeNodeLeaseKey(cfg.Scope, namespace, workerID)
		// 本地期限从发送前起算，不能把 Redis 回包耗时算成剩余租期。
		startedAt := time.Now()
		ok, err := client.SetNX(ctx, key, owner, ttl).Result()
		if err != nil {
			return nil, errors.Wrapf(err, "申请雪花 node_id 失败 namespace=%s node_id=%d", namespace, workerID)
		}
		if !ok {
			continue
		}
		lease, err := activateSnowflakeRedisLease(ctx, client, key, owner, namespace, workerID, token, startedAt, cfg, onRelease)
		if err != nil {
			return nil, errors.Tag(err)
		}
		return lease, nil
	}
	return nil, errors.Errorf("雪花 node_id 池已耗尽 scope=%s namespace=%s range=0-%d", cfg.Scope, namespace, nodeCount-1)
}

// snowflakeRedisNodeCount 返回 namespace 可竞争的 node_id 池大小。
func snowflakeRedisNodeCount(cfg config.SnowflakeRedisConfig, namespace string) int64 {
	if item, ok := cfg.Namespaces[namespace]; ok && item.NodeCount > 0 {
		return int64(item.NodeCount)
	}
	return idgen.SnowflakeMaxWorkerID + 1
}

// activateSnowflakeRedisLease 发布单个业务 namespace 的 worker_id 并启动后台续约。
func activateSnowflakeRedisLease(ctx context.Context, client redis.UniversalClient, key string, owner string, namespace string, workerID int64, token uint64, startedAt time.Time, cfg config.SnowflakeRedisConfig, onRelease func(string, int64)) (*snowflakeRedisLease, error) {
	renewCtx, cancelRenew := context.WithCancel(context.Background())
	lease := &snowflakeRedisLease{
		client:        client,
		namespace:     namespace,
		key:           key,
		owner:         owner,
		workerID:      workerID,
		ttl:           time.Duration(cfg.LeaseSeconds) * time.Second,
		renewInterval: time.Duration(cfg.RenewIntervalSeconds) * time.Second,
		renewCtx:      renewCtx,
		cancelRenew:   cancelRenew,
		stop:          make(chan struct{}),
		done:          make(chan struct{}),
		closeDone:     make(chan struct{}),
		onRelease:     onRelease,
	}
	lease.validUntil = startedAt.Add(lease.maxRenewSilence())
	// Redis 已预占成功；截止过早或解析器已替换时只回滚未发布租约。
	var err error
	lease.workerLease, err = idgen.ConfigureWorkerLeaseForNamespace(namespace, workerID, token, lease.validUntil)
	if err != nil {
		cancelRenew()
		_ = rollbackUnusedSnowflakeRedisLease(ctx, client, key, owner)
		return nil, errors.Tag(err)
	}
	// 本地 worker 已可用，立即启动唯一续约协程，Close 等待 done 收口。
	go lease.renewLoop()
	idgen.RecordSnowflakeLeaseEvent(namespace, "acquired")
	return lease, nil
}

// renewLoop 定期续约租约；确认租约丢失时立即停止本进程该业务发号。
func (l *snowflakeRedisLease) renewLoop() {
	defer close(l.done)
	defer l.releaseLocal()
	defer l.cancelRenew()
	ticker := time.NewTicker(l.renewInterval)
	defer ticker.Stop()
	for {
		select {
		case <-l.stop:
			return
		case <-ticker.C:
			// 网络重试也必须在剩余安全窗口内完成，关闭会同时取消该调用。
			startedAt := time.Now()
			renewCtx, cancel := context.WithDeadline(l.renewCtx, l.validUntil)
			ok, err := l.renew(renewCtx)
			cancel()
			if err != nil {
				if l.renewCtx != nil && l.renewCtx.Err() != nil {
					return
				}
				idgen.RecordSnowflakeLeaseEvent(l.namespace, "renew_failed")
				logx.Errorf("雪花 node_id 租约续约失败: namespace=%s worker_id=%d key=%s err=%v", l.namespace, l.workerID, l.key, err)
				if !time.Now().Before(l.validUntil) {
					idgen.RecordSnowflakeLeaseEvent(l.namespace, "renew_timeout")
					l.releaseLocal()
					logx.Errorf("雪花 node_id 租约续约超时，已停止本进程该业务发号: namespace=%s worker_id=%d key=%s", l.namespace, l.workerID, l.key)
					return
				}
				continue
			}
			// owner 不匹配说明 worker 已被接管，本实例必须释放本地能力。
			if !ok {
				idgen.RecordSnowflakeLeaseEvent(l.namespace, "lost")
				l.releaseLocal()
				logx.Errorf("雪花 node_id 租约已丢失，已停止本进程该业务发号: namespace=%s worker_id=%d key=%s", l.namespace, l.workerID, l.key)
				return
			}
			// 迟到成功不能复活已到期或关闭的绑定；新期限同样扣除回包耗时。
			deadline := startedAt.Add(l.maxRenewSilence())
			if !l.workerLease.Renew(deadline) {
				idgen.RecordSnowflakeLeaseEvent(l.namespace, "renew_timeout")
				return
			}
			l.validUntil = deadline
		}
	}
}

// maxRenewSilence 返回允许连续续约失败的最大时长，必须早于 Redis TTL 到期。
func (l *snowflakeRedisLease) maxRenewSilence() time.Duration {
	window := l.ttl - l.renewInterval
	if half := l.ttl / 2; window < half {
		window = half
	}
	if window < time.Second {
		return time.Second
	}
	return window
}

// renew 仅在 owner 匹配时延长当前 node_id 租约。
func (l *snowflakeRedisLease) renew(ctx context.Context) (bool, error) {
	result, err := snowflakeLeaseRenewScript.Run(ctx, l.client, []string{l.key}, l.owner, int(l.ttl/time.Second)).Int()
	if err != nil {
		return false, errors.Tag(err)
	}
	return result == 1, nil
}

// Close 先停止本地发号和续约协程，再刷新完整 TTL；Redis key 只能自然过期。
func (l *snowflakeRedisLease) Close(ctx context.Context) error {
	if l == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// 首次关闭负责停止续租和本地发号，重复调用只等待同一结果。
	l.closeOnce.Do(func() {
		close(l.stop)
		if l.cancelRenew != nil {
			l.cancelRenew()
		}
		l.releaseLocal()
		go func() {
			// 续租协程退出后再刷新隔离 TTL，避免旧协程延长已释放租约。
			select {
			case <-l.done:
				active, err := l.renew(ctx)
				switch {
				case err != nil:
					l.closeErr = errors.Wrap(err, "刷新雪花租约停机隔离 TTL 失败")
				case active:
					idgen.RecordSnowflakeLeaseEvent(l.namespace, "quarantined")
				}
			case <-ctx.Done():
				l.closeErr = errors.Wrap(ctx.Err(), "等待雪花租约续约协程退出超时")
			}
			close(l.closeDone)
		}()
	})
	// 调用方可超时返回，已启动的清理仍会继续完成。
	select {
	case <-l.closeDone:
		return errors.Tag(l.closeErr)
	case <-ctx.Done():
		return errors.Wrap(ctx.Err(), "关闭雪花 Redis 租约超时")
	}
}

// releaseLocal 释放本地单个业务 namespace 的 worker 状态。
func (l *snowflakeRedisLease) releaseLocal() {
	l.releaseOnce.Do(func() {
		if l.workerLease != nil {
			l.workerLease.Close()
		}
		if l.onRelease != nil {
			l.onRelease(l.namespace, l.workerID)
			return
		}
		idgen.RecordSnowflakeLeaseEvent(l.namespace, "released")
	})
}

// rollbackUnusedSnowflakeRedisLease 仅回滚尚未发布给本地 ID 生成器的 Redis 预占 key。
func rollbackUnusedSnowflakeRedisLease(ctx context.Context, client redis.UniversalClient, key string, owner string) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, err := snowflakeLeaseRollbackScript.Run(ctx, client, []string{key}, owner).Int(); err != nil {
		return errors.Tag(err)
	}
	return nil
}

// snowflakeLeaseOwner 生成可读且进程唯一的租约 owner。
func snowflakeLeaseOwner() (string, error) {
	host, _ := os.Hostname()
	if host == "" {
		host = "unknown-host"
	}
	randomSuffix, err := secureid.NewHex(snowflakeLeaseOwnerRandomBytes)
	if err != nil {
		return "", errors.Tag(err)
	}
	return fmt.Sprintf("%s:%d:%s", host, os.Getpid(), randomSuffix), nil
}
