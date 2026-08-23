package configload

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"api/common/idgen"
	keys "api/common/rediskeys"
	"api/internal/config"

	"github.com/Is999/go-utils/errors"
	"github.com/redis/go-redis/v9"
)

const (
	defaultIDSegmentStep                   int64 = 10000    // 默认每次申请 1 万个 ID，降低 Redis 访问频率
	maxIDSegmentStep                       int64 = 10000000 // 单次号段最大步长，避免配置错误放大 Redis 高水位
	defaultIDSegmentAllocateTimeoutSeconds       = 2        // 默认 Redis 号段申请超时
	maxIDSegmentAllocateTimeoutSeconds           = 30       // Redis 号段申请最大超时秒数，避免启动和预取长期阻塞
)

// redisSegmentManager 管理当前进程按业务 namespace 启用的 Redis Segment 取号器。
type redisSegmentManager struct {
	client    redis.UniversalClient    // Redis 客户端，由当前 single/cluster 配置选择实现
	cfg       config.IDSegmentConfig   // 已补齐默认值的 Segment 配置
	token     uint64                   // idgen Segment 解析器绑定 token
	mu        sync.RWMutex             // 保护 states 和 closed
	states    map[string]*segmentState // 按业务 namespace 保存本地双缓冲状态
	closed    bool                     // closed 表示管理器已关闭，不再分配新号段
	ctx       context.Context          // ctx 约束同步申请和后台预取生命周期
	cancel    context.CancelFunc       // cancel 在停机时中断 Redis 申请
	workers   sync.WaitGroup           // workers 等待已登记的预取任务退出
	closeDone chan struct{}            // closeDone 在解析器清理且预取全部退出后关闭
}

// segmentState 保存单个业务 namespace 的本地号段双缓冲。
type segmentState struct {
	namespace string                          // 业务命名空间
	cfg       config.IDSegmentNamespaceConfig // 已补齐默认值的 namespace 配置
	mu        sync.Mutex                      // 保护 current、next、prefetching 和 closed
	cond      *sync.Cond                      // 等待预取完成或关闭信号的条件变量
	current   idSegmentRange                  // 当前正在消费的号段
	next      idSegmentRange                  // 预取完成后等待切换的下一号段
	prefetch  bool                            // 是否已有后台预取任务
	closed    bool                            // 是否已关闭该 namespace
}

// idSegmentRange 表示本地缓存的半开区间号段。
type idSegmentRange struct {
	next int64 // 下一个待返回 ID
	end  int64 // 半开区间结束值
}

// normalizeIDSegmentConfig 补齐 Redis Segment 默认配置。
func normalizeIDSegmentConfig(cfg config.IDSegmentConfig, redisCfg config.SnowflakeRedisConfig) config.IDSegmentConfig {
	if cfg.Scope == "" {
		cfg.Scope = firstNonEmpty(redisCfg.Scope, defaultSnowflakeRedisScope)
	}
	if cfg.AllocateTimeoutSeconds == 0 {
		cfg.AllocateTimeoutSeconds = defaultIDSegmentAllocateTimeoutSeconds
	}
	// 复制后再补默认值，避免修改调用方持有的配置映射；nil 输入也返回可用空映射。
	normalized := make(map[string]config.IDSegmentNamespaceConfig, len(cfg.Namespaces))
	for namespace, item := range cfg.Namespaces {
		normalized[namespace] = normalizeIDSegmentNamespaceConfig(item)
	}
	cfg.Namespaces = normalized
	return cfg
}

// normalizeIDSegmentNamespaceConfig 补齐单业务 namespace 的 Segment 默认配置。
func normalizeIDSegmentNamespaceConfig(cfg config.IDSegmentNamespaceConfig) config.IDSegmentNamespaceConfig {
	if cfg.Step == 0 {
		cfg.Step = defaultIDSegmentStep
	}
	if cfg.PrefetchThreshold == 0 {
		cfg.PrefetchThreshold = cfg.Step / 5
	}
	return cfg
}

// newRedisSegmentManager 创建 Redis Segment 本地双缓冲管理器。
func newRedisSegmentManager(ctx context.Context, cfg config.IDSegmentConfig, redisCfg config.SnowflakeRedisConfig, client redis.UniversalClient) (*redisSegmentManager, error) {
	// 共享依赖必须在创建后台预取协程前完成校验。
	if !cfg.Enabled {
		return nil, nil
	}
	if client == nil {
		return nil, errors.New("snowflake.segment.enabled=true 时 Redis 客户端不能为空")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateIDSegmentConfig(config.SnowflakeConfig{Redis: redisCfg, Segment: cfg}); err != nil {
		return nil, errors.Tag(err)
	}
	cfg = normalizeIDSegmentConfig(cfg, redisCfg)
	if err := client.Ping(ctx).Err(); err != nil {
		return nil, errors.Wrap(err, "检查 ID Segment Redis 客户端失败")
	}
	// 每个启用的 namespace 独立持有双缓冲状态，避免业务间互相阻塞。
	manager := &redisSegmentManager{
		client: client,
		cfg:    cfg,
		states: make(map[string]*segmentState),
	}
	for namespace, item := range cfg.Namespaces {
		if !item.Enabled {
			continue
		}
		state := &segmentState{namespace: namespace, cfg: item}
		state.cond = sync.NewCond(&state.mu)
		manager.states[namespace] = state
		idgen.RecordSegmentRemaining(namespace, 0)
	}
	if len(manager.states) == 0 {
		return nil, errors.New("snowflake.segment.enabled=true 时必须至少启用一个 namespace")
	}
	// 所有状态构建成功后再发布解析器，失败不会暴露半初始化实例。
	manager.ctx, manager.cancel = context.WithCancel(ctx)
	manager.closeDone = make(chan struct{})
	manager.token = idgen.ConfigureSegmentResolver(manager)
	return manager, nil
}

// SegmentEnabled 判断指定 namespace 是否启用 Redis Segment。
func (m *redisSegmentManager) SegmentEnabled(namespace string) bool {
	if namespace == "" || strings.TrimSpace(namespace) != namespace || m == nil {
		return false
	}
	m.mu.RLock()
	defer m.mu.RUnlock()
	if m.closed {
		return false
	}
	_, ok := m.states[namespace]
	return ok
}

// SegmentID 返回指定 namespace 的下一个本地 Segment ID。
func (m *redisSegmentManager) SegmentID(namespace string) (int64, error) {
	if namespace == "" || strings.TrimSpace(namespace) != namespace {
		return 0, errors.New("ID Segment namespace 不能为空或包含首尾空白")
	}
	m.mu.RLock()
	if m.closed {
		m.mu.RUnlock()
		return 0, errors.Errorf("ID Segment 管理器已关闭 namespace=%s", namespace)
	}
	state := m.states[namespace]
	m.mu.RUnlock()
	if state == nil {
		return 0, errors.Errorf("ID Segment namespace 未配置: %s", namespace)
	}
	return state.nextID(m)
}

// Ready 检查 Segment 管理器状态和 Redis 连接。
func (m *redisSegmentManager) Ready(ctx context.Context) error {
	if m == nil {
		return errors.New("ID Segment 管理器未初始化")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	m.mu.RLock()
	closed := m.closed
	m.mu.RUnlock()
	if closed {
		return errors.New("ID Segment 管理器已关闭")
	}
	if err := m.client.Ping(ctx).Err(); err != nil {
		return errors.Wrap(err, "检查 ID Segment Redis 连接失败")
	}
	return nil
}

// Close 关闭 Segment 解析器、取消 Redis 申请并等待已登记的预取任务退出。
func (m *redisSegmentManager) Close(ctx context.Context) error {
	if m == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// 锁内只切换关闭状态并截取资源快照，耗时清理放到锁外执行。
	m.mu.Lock()
	if m.closed {
		done := m.closeDone
		m.mu.Unlock()
		return waitSegmentManagerClose(ctx, done)
	}
	m.closed = true
	if m.cancel != nil {
		m.cancel()
	}
	states := make([]*segmentState, 0, len(m.states))
	for namespace, state := range m.states {
		states = append(states, state)
		delete(m.states, namespace)
	}
	token := m.token
	m.mu.Unlock()

	// 先阻止新请求进入，再释放各 namespace 的本地状态和解析器。
	for _, state := range states {
		state.close()
	}
	// token 只撤下本实例解析器，不能清掉后来替换的注册。
	idgen.ClearSegmentResolver(token)
	// 后台预取结束后统一关闭完成信号，重复调用复用同一结果。
	go func() {
		m.workers.Wait()
		close(m.closeDone)
	}()
	return waitSegmentManagerClose(ctx, m.closeDone)
}

// nextID 从本地双缓冲取号，必要时同步申请新号段。
func (s *segmentState) nextID(m *redisSegmentManager) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for !s.current.valid() {
		if s.closed || m.isClosed() {
			return 0, errors.Errorf("ID Segment 已关闭 namespace=%s", s.namespace)
		}
		if s.next.valid() {
			// 当前段耗尽后先切换已预取段，这条路径不访问 Redis。
			s.current = s.next
			s.next = idSegmentRange{}
			idgen.RecordSegmentRemaining(s.namespace, s.remainingLocked())
			break
		}
		if s.prefetch {
			// 同一 namespace 只允许一个预取请求，等待时释放状态锁。
			s.cond.Wait()
			continue
		}
		// 两段均空且没有预取时才同步取段，申请同时受超时和停机取消约束。
		segment, err := m.allocateSegment(m.ctx, s.namespace, s.cfg)
		if err != nil {
			return 0, errors.Tag(err)
		}
		s.current = segment
		idgen.RecordSegmentRemaining(s.namespace, s.remainingLocked())
	}
	id := s.current.next
	s.current.next++
	idgen.RecordSegmentRemaining(s.namespace, s.remainingLocked())
	if s.current.remaining() <= s.cfg.PrefetchThreshold {
		s.prefetchNextLocked(m)
	}
	return id, nil
}

// prefetchNextLocked 在当前号段低水位时异步预取下一号段。
func (s *segmentState) prefetchNextLocked(m *redisSegmentManager) {
	if s.prefetch || s.next.valid() || s.closed || m.isClosed() {
		return
	}
	if !m.startWorker() {
		return
	}
	s.prefetch = true
	go func() {
		defer m.workers.Done()
		segment, err := m.allocateSegment(m.ctx, s.namespace, s.cfg)
		s.mu.Lock()
		defer s.mu.Unlock()
		defer s.cond.Broadcast()
		s.prefetch = false
		// 关闭或其他状态已接管时丢弃本次号段；允许跳号，不复用已领取区间。
		if err != nil || s.closed || m.isClosed() || s.next.valid() {
			return
		}
		s.next = segment
		idgen.RecordSegmentRemaining(s.namespace, s.remainingLocked())
	}()
}

// startWorker 在停机锁内登记预取任务，确保 Close 开始等待后不会再发生 WaitGroup.Add。
func (m *redisSegmentManager) startWorker() bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return false
	}
	m.workers.Add(1)
	return true
}

// waitSegmentManagerClose 按调用方停机期限等待号段管理器完整退出。
func waitSegmentManagerClose(ctx context.Context, done <-chan struct{}) error {
	if done == nil {
		return nil
	}
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return errors.Wrap(ctx.Err(), "等待 ID Segment 后台预取退出超时")
	}
}

// close 关闭单个 namespace 的本地号段状态。
func (s *segmentState) close() {
	s.mu.Lock()
	s.closed = true
	s.current = idSegmentRange{}
	s.next = idSegmentRange{}
	s.cond.Broadcast()
	idgen.RecordSegmentRemaining(s.namespace, 0)
	s.mu.Unlock()
}

// allocateSegment 从 Redis 申请一个新的半开区间号段。
func (m *redisSegmentManager) allocateSegment(ctx context.Context, namespace string, cfg config.IDSegmentNamespaceConfig) (idSegmentRange, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	timeout := time.Duration(m.cfg.AllocateTimeoutSeconds) * time.Second
	allocCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	startAt := time.Now()
	key := keys.IDSegmentCounterKey(m.cfg.Scope, namespace)
	if key == "" {
		err := errors.Errorf("ID Segment key 为空 namespace=%s", namespace)
		idgen.RecordSegmentAllocation(namespace, "failed", time.Since(startAt))
		return idSegmentRange{}, err
	}
	// 高水位不设 TTL，重复初始化不能覆盖其它实例已经领取的号段。
	if _, err := m.client.SetNX(allocCtx, key, strconv.FormatInt(cfg.Start, 10), 0).Result(); err != nil {
		idgen.RecordSegmentAllocation(namespace, "failed", time.Since(startAt))
		return idSegmentRange{}, errors.Wrapf(err, "初始化 ID Segment 高水位失败 namespace=%s", namespace)
	}
	end, err := m.client.IncrBy(allocCtx, key, cfg.Step).Result()
	if err != nil {
		idgen.RecordSegmentAllocation(namespace, "failed", time.Since(startAt))
		return idSegmentRange{}, errors.Wrapf(err, "申请 ID Segment 号段失败 namespace=%s", namespace)
	}
	idgen.RecordSegmentAllocation(namespace, "success", time.Since(startAt))
	return idSegmentRange{next: end - cfg.Step + 1, end: end + 1}, nil
}

// isClosed 判断 Segment 管理器是否已关闭。
func (m *redisSegmentManager) isClosed() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.closed
}

// valid 判断号段是否仍有可用 ID。
func (r idSegmentRange) valid() bool {
	return r.next > 0 && r.next < r.end
}

// remaining 返回号段剩余 ID 数量。
func (r idSegmentRange) remaining() int64 {
	if !r.valid() {
		return 0
	}
	return r.end - r.next
}

// remainingLocked 返回当前 namespace 本地缓存的剩余 ID 数量。
func (s *segmentState) remainingLocked() int64 {
	return s.current.remaining() + s.next.remaining()
}

// firstNonEmpty 返回第一个非空字符串。
func firstNonEmpty(items ...string) string {
	for _, item := range items {
		if item != "" {
			return item
		}
	}
	return ""
}
