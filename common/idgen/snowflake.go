package idgen

import (
	"hash/crc32"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Is999/go-utils/errors"
	bwsnowflake "github.com/bwmarrin/snowflake"
)

const (
	// ShardMod 表示用户 ID 哈希分片数量，固定为 2 的幂便于平滑拆表。
	ShardMod = 1024
	// SnowflakeWorkerIDUnset 表示未显式配置雪花 worker_id。
	SnowflakeWorkerIDUnset int64 = -1

	snowflakeEpochMillis  int64 = 1704067200000                     // 业务纪元为 2024-01-01 UTC，ID 时间部分相对此时刻计算毫秒偏移。
	snowflakeWorkerBits         = 10                                // 节点编号占 10 位，可用范围为 0-1023。
	snowflakeSequenceBits       = 12                                // snowflakeSequenceBits 表示同一毫秒内的序列位数，单实例每毫秒最多 4096 个 ID。
	snowflakeMaxWorkerID        = int64(1<<snowflakeWorkerBits - 1) // snowflakeMaxWorkerID 表示 worker_id 可配置上限。
	// SnowflakeMaxWorkerID 表示雪花 worker_id 最大值。
	SnowflakeMaxWorkerID = snowflakeMaxWorkerID
)

var (
	staticWorkerID       atomic.Int64          // staticWorkerID 保存静态 worker_id，主要用于手工强管控部署
	workerResolverSeq    atomic.Uint64         // workerResolverSeq 生成动态 worker_id 解析器绑定 token
	workerResolverMu     sync.RWMutex          // workerResolverMu 保护当前动态 worker_id 解析器
	activeWorkerResolver workerResolverBinding // activeWorkerResolver 保存当前动态 worker_id 解析器
	snowflakeFormatOnce  sync.Once             // snowflakeFormatOnce 确保 bwmarrin/snowflake 位宽只初始化一次
	namespaceWorkers     sync.Map              // namespaceWorkers 保存本地编号及租约截止，发号不依赖续租协程及时调度
	workerGenerators     sync.Map              // workerGenerators 按业务命名空间和 worker_id 缓存进程内 ID 生成器
)

func init() {
	staticWorkerID.Store(SnowflakeWorkerIDUnset)
}

// WorkerResolver 按业务命名空间分配雪花 worker_id。
type WorkerResolver interface {
	// SnowflakeWorkerID 返回租约独占的 worker_id，租约不可用时返回错误。
	SnowflakeWorkerID(namespace string) (int64, error)
}

// WorkerResolverFunc 适配函数式 worker_id 解析器。
type WorkerResolverFunc func(namespace string) (int64, error)

// SnowflakeWorkerID 返回指定业务命名空间当前可用的 worker_id。
func (f WorkerResolverFunc) SnowflakeWorkerID(namespace string) (int64, error) {
	if f == nil {
		return 0, errors.New("雪花 worker_id 解析器未初始化")
	}
	return f(namespace)
}

// Snowflake 封装单个 bwmarrin/snowflake 节点；单调性仅限当前节点，不保证跨业务命名空间有序。
type Snowflake struct {
	workerID int64             // workerID 是部署分配的分布式实例编号，必须跨实例唯一。
	node     *bwsnowflake.Node // node 内部通过互斥锁保护同一 worker 的并发序列。
}

// snowflakeGeneratorKey 标识单个业务命名空间内的本地生成器。
type snowflakeGeneratorKey struct {
	namespace string // namespace 是调用方业务唯一域，如 user、recharge.order。
	workerID  int64  // workerID 是该业务命名空间下当前实例持有的 node_id。
}

// workerResolverBinding 保存当前生效的动态 worker_id 解析器。
type workerResolverBinding struct {
	resolver WorkerResolver // resolver 负责按 namespace 懒分配 worker_id。
	token    uint64         // token 用于关闭旧解析器时避免误清新解析器。
}

// WorkerLease 保存一次本地 worker 绑定；关闭后旧续租回包不能重新激活它。
type WorkerLease struct {
	workerID int64                     // workerID 在绑定生命周期内不变。
	deadline atomic.Pointer[time.Time] // nil 表示已撤销，零时刻仅用于静态编号。
}

// Valid 直接检查本地单调时钟，Redis 调用阻塞时也不能越过租约安全窗口。
func (l *WorkerLease) Valid() bool {
	deadline := l.deadline.Load()
	return deadline != nil && (deadline.IsZero() || time.Now().Before(*deadline))
}

// Renew 只延长仍有效的原绑定，迟到回包和旧实例不得复活已撤销租约。
func (l *WorkerLease) Renew(deadline time.Time) bool {
	for {
		previous := l.deadline.Load()
		if previous == nil || previous.IsZero() || !time.Now().Before(*previous) || !deadline.After(*previous) {
			return false
		}
		if l.deadline.CompareAndSwap(previous, &deadline) {
			return true
		}
	}
}

// Close 撤销本次绑定；保留拒绝标记，直到新租约成功发布才能再次发号。
func (l *WorkerLease) Close() {
	l.deadline.Store(nil)
}

// NewSnowflake 创建指定 worker 的 bwmarrin/snowflake ID 生成器。
func NewSnowflake(workerID int64) (*Snowflake, error) {
	if err := ValidateWorkerID(workerID); err != nil {
		return nil, errors.Tag(err)
	}
	configureSnowflakeFormat()
	node, err := bwsnowflake.NewNode(workerID)
	if err != nil {
		return nil, errors.Wrapf(err, "创建雪花 ID 节点失败 worker_id=%d", workerID)
	}
	// 等待至少 1ms，降低同一 worker_id 进程极速重启时复用上一进程最后毫秒窗口的风险。
	time.Sleep(time.Millisecond)
	return &Snowflake{workerID: workerID, node: node}, nil
}

// configureSnowflakeFormat 固定成熟库位宽，确保 worker_id 和测试解析规则一致。
func configureSnowflakeFormat() {
	snowflakeFormatOnce.Do(func() {
		bwsnowflake.Epoch = snowflakeEpochMillis
		bwsnowflake.NodeBits = uint8(snowflakeWorkerBits)
		bwsnowflake.StepBits = uint8(snowflakeSequenceBits)
	})
}

// ValidateWorkerID 校验雪花 worker_id 是否落在 10 bit 可表达范围内。
func ValidateWorkerID(workerID int64) error {
	if workerID < 0 || workerID > snowflakeMaxWorkerID {
		return errors.Errorf("雪花 worker_id 必须在 0-%d 之间", snowflakeMaxWorkerID)
	}
	return nil
}

// ResolveWorkerID 校验静态 worker_id，未配置时要求调用方启用 Redis 租约。
func ResolveWorkerID(configWorkerID int64) (int64, error) {
	if configWorkerID == SnowflakeWorkerIDUnset {
		return 0, errors.Errorf("雪花 worker_id 未配置，请设置 snowflake.worker_id 或启用 snowflake.redis")
	}
	if err := ValidateWorkerID(configWorkerID); err != nil {
		return 0, errors.Tag(err)
	}
	return configWorkerID, nil
}

// ConfigureWorkerID 设置当前进程所有业务命名空间共享的静态 worker_id。
func ConfigureWorkerID(workerID int64) error {
	if err := ValidateWorkerID(workerID); err != nil {
		return errors.Tag(err)
	}
	workerResolverMu.Lock()
	defer workerResolverMu.Unlock()
	activeWorkerResolver = workerResolverBinding{}
	staticWorkerID.Store(workerID)
	clearNamespaceWorkers()
	clearWorkerGenerators()
	return nil
}

// ConfigureWorkerResolver 注册按业务命名空间动态分配 worker_id 的解析器。
func ConfigureWorkerResolver(resolver WorkerResolver) uint64 {
	if resolver == nil {
		ClearWorkerResolver(0)
		return 0
	}
	token := workerResolverSeq.Add(1)
	workerResolverMu.Lock()
	defer workerResolverMu.Unlock()
	activeWorkerResolver = workerResolverBinding{resolver: resolver, token: token}
	staticWorkerID.Store(SnowflakeWorkerIDUnset)
	clearNamespaceWorkers()
	clearWorkerGenerators()
	return token
}

// ClearWorkerResolver 清理指定 token 对应的动态 worker_id 解析器。
func ClearWorkerResolver(token uint64) {
	workerResolverMu.Lock()
	if token == 0 || activeWorkerResolver.token == token {
		activeWorkerResolver = workerResolverBinding{}
		staticWorkerID.Store(SnowflakeWorkerIDUnset)
		clearNamespaceWorkers()
		clearWorkerGenerators()
	}
	workerResolverMu.Unlock()
}

// ConfigureWorkerIDForNamespace 记录单个业务命名空间当前持有的 worker_id。
func ConfigureWorkerIDForNamespace(namespace string, workerID int64) error {
	_, err := configureNamespaceWorker(namespace, workerID, 0, time.Time{})
	return err
}

// ConfigureWorkerLeaseForNamespace 发布有截止时间的租约，token 阻止旧解析器覆盖新实例。
func ConfigureWorkerLeaseForNamespace(namespace string, workerID int64, token uint64, deadline time.Time) (*WorkerLease, error) {
	if token == 0 || !time.Now().Before(deadline) {
		return nil, errors.New("雪花 worker 租约绑定无效或已过期")
	}
	return configureNamespaceWorker(namespace, workerID, token, deadline)
}

// configureNamespaceWorker 校验并替换单个绑定，零截止时间只由静态编号入口传入。
func configureNamespaceWorker(namespace string, workerID int64, token uint64, deadline time.Time) (*WorkerLease, error) {
	if namespace == "" || strings.TrimSpace(namespace) != namespace {
		return nil, errors.New("雪花 ID namespace 不能为空或包含首尾空白")
	}
	if err := ValidateWorkerID(workerID); err != nil {
		return nil, errors.Tag(err)
	}
	workerResolverMu.Lock()
	defer workerResolverMu.Unlock()
	if token != 0 && (activeWorkerResolver.token != token || activeWorkerResolver.resolver == nil) {
		return nil, errors.New("雪花 worker_id 解析器已关闭")
	}
	binding := &WorkerLease{workerID: workerID}
	binding.deadline.Store(&deadline)
	// 并发首次解析静态编号共用现有绑定，避免互相撤销刚取得的发号资格。
	if deadline.IsZero() {
		if current, ok := namespaceWorkers.Load(namespace); ok && current.(*WorkerLease).workerID == workerID && current.(*WorkerLease).Valid() {
			return current.(*WorkerLease), nil
		}
	}
	if previous, loaded := namespaceWorkers.Swap(namespace, binding); loaded {
		previous.(*WorkerLease).Close()
		if previous.(*WorkerLease).workerID != workerID {
			clearWorkerGeneratorsForNamespace(namespace)
		}
	}
	return binding, nil
}

// ReleaseWorkerID 释放当前进程持有的 worker_id，避免租约丢失后继续生成冲突 ID。
func ReleaseWorkerID(workerID int64) {
	// 只撤销仍指向该 worker 的静态配置，不能清除随后分配的新 worker。
	staticWorkerID.CompareAndSwap(workerID, SnowflakeWorkerIDUnset)
	clearNamespaceWorkersForWorker(workerID)
	clearWorkerGeneratorsForWorker(workerID)
}

// ReleaseWorkerIDForNamespace 释放单个业务命名空间持有的 worker_id。
func ReleaseWorkerIDForNamespace(namespace string, workerID int64) {
	if namespace == "" || strings.TrimSpace(namespace) != namespace {
		return
	}
	if current, ok := namespaceWorkers.Load(namespace); ok && current.(*WorkerLease).workerID == workerID {
		current.(*WorkerLease).Close()
	}
	workerGenerators.Delete(snowflakeGeneratorKey{namespace: namespace, workerID: workerID})
}

// CurrentWorkerID 返回当前进程指定业务命名空间已配置的 worker_id。
func CurrentWorkerID(namespaces ...string) (int64, bool) {
	if len(namespaces) > 0 {
		// 指定业务域时先查已绑定编号；仅未绑定时使用部署配置的静态编号。
		namespace := namespaces[0]
		if namespace == "" || strings.TrimSpace(namespace) != namespace {
			return 0, false
		}
		if workerID, ok := namespaceWorkerID(namespace); ok {
			return workerID, true
		}
		workerID := staticWorkerID.Load()
		return workerID, workerID != SnowflakeWorkerIDUnset
	}
	if workerID := staticWorkerID.Load(); workerID != SnowflakeWorkerIDUnset {
		return workerID, true
	}
	// 未指定命名空间时，只有唯一动态 worker 才能给出无歧义结果。
	var (
		found int64
		count int
	)
	namespaceWorkers.Range(func(_, value any) bool {
		binding := value.(*WorkerLease)
		if !binding.Valid() {
			return true
		}
		found = binding.workerID
		count++
		return count < 2
	})
	return found, count == 1
}

// NextID 按命名空间选择号段或雪花策略；号段失败时不切换策略，避免 ID 空间混用。
func NextID(namespace string) (int64, error) {
	if namespace == "" || strings.TrimSpace(namespace) != namespace {
		return 0, errors.New("雪花 ID namespace 不能为空或包含首尾空白")
	}
	if resolver, token := currentSegmentResolver(); resolver != nil && resolver.SegmentEnabled(namespace) {
		start := time.Now()
		id, err := nextSegmentID(namespace, resolver, token)
		recordIDGenerate(namespace, IDStrategySegment, err, time.Since(start))
		if err != nil {
			return 0, errors.Tag(err)
		}
		return id, nil
	}
	start := time.Now()
	id, err := nextSnowflakeID(namespace)
	recordIDGenerate(namespace, IDStrategySnowflake, err, time.Since(start))
	if err != nil {
		return 0, errors.Tag(err)
	}
	return id, nil
}

// nextSnowflakeID 返回指定业务命名空间的下一个雪花 ID。
func nextSnowflakeID(namespace string) (int64, error) {
	binding, err := activeWorker(namespace)
	if err != nil {
		return 0, errors.Tag(err)
	}
	key := snowflakeGeneratorKey{namespace: namespace, workerID: binding.workerID}
	generator, cached := workerGenerators.Load(key)
	if !cached {
		created, createErr := NewSnowflake(binding.workerID)
		if createErr != nil {
			return 0, errors.Tag(createErr)
		}
		// 并发首次取号只使用最终入缓存的节点，不能各用一份序列。
		generator, _ = workerGenerators.LoadOrStore(key, created)
	}
	id, err := generator.(*Snowflake).NextID()
	if err != nil {
		return 0, errors.Tag(err)
	}
	// 生成器内部可能等待下一毫秒；返回前再核对期限及绑定身份。
	current, ok := namespaceWorkers.Load(namespace)
	if !ok || current != binding || !binding.Valid() {
		return 0, errors.New("雪花 worker 租约已过期或释放")
	}
	return id, nil
}

// activeWorker 返回当前可用绑定，生成器必须用同一绑定完成返回前检查。
func activeWorker(namespace string) (*WorkerLease, error) {
	if current, ok := namespaceWorkers.Load(namespace); ok && current.(*WorkerLease).Valid() {
		return current.(*WorkerLease), nil
	}
	if resolver, token := currentWorkerResolver(); resolver != nil {
		workerID, err := resolver.SnowflakeWorkerID(namespace)
		if err != nil {
			return nil, errors.Tag(err)
		}
		// 租约申请可能跨越解析器关闭，完成后须丢弃旧绑定返回的 worker。
		if !workerResolverActive(token) {
			return nil, errors.Errorf("雪花 worker_id 解析器已关闭 namespace=%s", namespace)
		}
		// Redis 解析器已发布带期限绑定，不能被函数解析器的静态缓存路径覆盖。
		if current, ok := namespaceWorkers.Load(namespace); ok {
			binding := current.(*WorkerLease)
			if binding.workerID != workerID || !binding.Valid() {
				return nil, errors.New("雪花 worker 租约已过期或释放")
			}
			return binding, nil
		}
		return configureNamespaceWorker(namespace, workerID, token, time.Time{})
	}
	if workerID := staticWorkerID.Load(); workerID != SnowflakeWorkerIDUnset {
		// 静态编号由部署保证实例间互斥，此路径不向 Redis 申请租约。
		return configureNamespaceWorker(namespace, workerID, 0, time.Time{})
	}
	return nil, errors.Errorf("雪花 worker_id 未配置或 Redis node_id 租约已释放 namespace=%s", namespace)
}

// NextID 返回当前生成器的下一个雪花 ID。
func (g *Snowflake) NextID() (int64, error) {
	if g == nil || g.node == nil {
		return 0, errors.New("雪花 ID 生成器未初始化")
	}
	return g.node.Generate().Int64(), nil
}

// ShardNo 对 ID 十进制字符串计算 CRC32 后取模 1024，禁止改为 ID 直接取模。
func ShardNo(id int64) int {
	checksum := crc32.ChecksumIEEE([]byte(strconv.FormatInt(id, 10)))
	return int(checksum % ShardMod)
}

// currentWorkerResolver 返回当前动态 worker_id 解析器和绑定 token。
func currentWorkerResolver() (WorkerResolver, uint64) {
	workerResolverMu.RLock()
	defer workerResolverMu.RUnlock()
	return activeWorkerResolver.resolver, activeWorkerResolver.token
}

// workerResolverActive 判断指定 token 的动态解析器是否仍然有效。
func workerResolverActive(token uint64) bool {
	workerResolverMu.RLock()
	defer workerResolverMu.RUnlock()
	return token != 0 && activeWorkerResolver.token == token && activeWorkerResolver.resolver != nil
}

// namespaceWorkerID 返回业务命名空间当前缓存的 worker_id。
func namespaceWorkerID(namespace string) (int64, bool) {
	value, ok := namespaceWorkers.Load(namespace)
	if !ok {
		return 0, false
	}
	binding := value.(*WorkerLease)
	return binding.workerID, binding.Valid()
}

// clearNamespaceWorkers 清空业务命名空间到 worker_id 的映射。
func clearNamespaceWorkers() {
	namespaceWorkers.Range(func(key, value any) bool {
		value.(*WorkerLease).Close()
		namespaceWorkers.Delete(key)
		return true
	})
}

// clearNamespaceWorkersForWorker 清空指定 worker_id 关联的业务命名空间映射。
func clearNamespaceWorkersForWorker(workerID int64) {
	namespaceWorkers.Range(func(key, value any) bool {
		if value.(*WorkerLease).workerID == workerID {
			value.(*WorkerLease).Close()
		}
		return true
	})
}

// clearWorkerGenerators 清空进程内 worker 生成器缓存。
func clearWorkerGenerators() {
	workerGenerators.Range(func(key, _ any) bool {
		workerGenerators.Delete(key)
		return true
	})
}

// clearWorkerGeneratorsForNamespace 清空指定业务命名空间的本地生成器。
func clearWorkerGeneratorsForNamespace(namespace string) {
	workerGenerators.Range(func(key, _ any) bool {
		if item, ok := key.(snowflakeGeneratorKey); ok && item.namespace == namespace {
			workerGenerators.Delete(key)
		}
		return true
	})
}

// clearWorkerGeneratorsForWorker 清空指定 worker_id 关联的本地生成器。
func clearWorkerGeneratorsForWorker(workerID int64) {
	workerGenerators.Range(func(key, _ any) bool {
		if item, ok := key.(snowflakeGeneratorKey); ok && item.workerID == workerID {
			workerGenerators.Delete(key)
		}
		return true
	})
}

// resetWorkerIDForTest 重置进程级 worker_id，避免测试之间相互影响。
func resetWorkerIDForTest() {
	ClearWorkerResolver(0)
	ClearSegmentResolver(0)
	staticWorkerID.Store(SnowflakeWorkerIDUnset)
	clearNamespaceWorkers()
	clearWorkerGenerators()
}
