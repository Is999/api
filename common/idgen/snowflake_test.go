package idgen

import (
	"hash/crc32"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestWorkerLeaseDeadline 验证发号直接检查截止时间，不依赖后台协程撤销映射。
func TestWorkerLeaseDeadline(t *testing.T) {
	resetWorkerIDForTest()
	t.Cleanup(resetWorkerIDForTest)
	token := ConfigureWorkerResolver(WorkerResolverFunc(func(string) (int64, error) { return 7, nil }))
	lease, err := ConfigureWorkerLeaseForNamespace("user", 7, token, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NextID("user"); err != nil {
		t.Fatal(err)
	}
	// 只推进绑定期限，解析器仍返回同一编号也不得恢复无期限绑定。
	lease.deadline.Store(new(time.Now().Add(-time.Second)))
	if id, err := NextID("user"); err == nil || id != 0 {
		t.Fatalf("expired NextID() = %d, %v", id, err)
	}
	if lease.Renew(time.Now().Add(time.Minute)) {
		t.Fatal("expired lease was revived")
	}
	if _, ok := CurrentWorkerID("user"); ok {
		t.Fatal("expired worker remains current")
	}
}

// TestWorkerLeaseRejectsLateGeneratedID 验证首次建节点的毫秒等待跨过截止时不会返回 ID。
func TestWorkerLeaseRejectsLateGeneratedID(t *testing.T) {
	resetWorkerIDForTest()
	t.Cleanup(resetWorkerIDForTest)
	token := ConfigureWorkerResolver(WorkerResolverFunc(func(string) (int64, error) { return 7, nil }))
	lease, err := ConfigureWorkerLeaseForNamespace("user", 7, token, time.Now().Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	lease.deadline.Store(new(time.Now().Add(500 * time.Microsecond)))
	if id, err := NextID("user"); err == nil || id != 0 {
		t.Fatalf("late NextID() = %d, %v", id, err)
	}
}

// TestWorkerLeaseRenewAndReplace 验证续租保持原绑定，旧实例关闭和回包不会影响新绑定。
func TestWorkerLeaseRenewAndReplace(t *testing.T) {
	resetWorkerIDForTest()
	t.Cleanup(resetWorkerIDForTest)
	resolver := WorkerResolverFunc(func(string) (int64, error) { return 7, nil })
	token := ConfigureWorkerResolver(resolver)
	deadline := time.Now().Add(time.Minute)
	old, err := ConfigureWorkerLeaseForNamespace("user", 7, token, deadline)
	if err != nil {
		t.Fatal(err)
	}
	if !old.Renew(deadline.Add(time.Minute)) {
		t.Fatal("healthy renewal rejected")
	}
	newToken := ConfigureWorkerResolver(resolver)
	current, err := ConfigureWorkerLeaseForNamespace("user", 7, newToken, deadline)
	if err != nil {
		t.Fatal(err)
	}
	// 并发旧回包与旧 Close 都只能触达被撤销的旧指针。
	var workers sync.WaitGroup
	for range 20 {
		workers.Go(func() {
			old.Close()
			if old.Renew(deadline.Add(2 * time.Minute)) {
				t.Error("old lease was revived")
			}
		})
	}
	workers.Wait()
	if _, err := ConfigureWorkerLeaseForNamespace("user", 7, token, deadline); err == nil {
		t.Fatal("old resolver overwrote current binding")
	}
	if !current.Valid() {
		t.Fatal("old close revoked current binding")
	}
	if _, err := NextID("user"); err != nil {
		t.Fatal(err)
	}
	ClearWorkerResolver(token)
	if _, err := NextID("user"); err != nil {
		t.Fatalf("old token cleanup revoked new worker: %v", err)
	}
}

// TestNextIDConcurrentStaticBinding 验证并发首次发号共用静态绑定和同一序列节点。
func TestNextIDConcurrentStaticBinding(t *testing.T) {
	resetWorkerIDForTest()
	t.Cleanup(resetWorkerIDForTest)
	if err := ConfigureWorkerID(7); err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for range 20 {
		workers.Go(func() {
			if _, err := NextID("user"); err != nil {
				t.Error(err)
			}
		})
	}
	workers.Wait()
}

// TestWorkerResolverReplacedDuringResolve 验证旧解析器回包不能发布到新 token 的映射。
func TestWorkerResolverReplacedDuringResolve(t *testing.T) {
	resetWorkerIDForTest()
	t.Cleanup(resetWorkerIDForTest)
	entered, release := make(chan struct{}), make(chan struct{})
	ConfigureWorkerResolver(WorkerResolverFunc(func(string) (int64, error) {
		close(entered)
		<-release
		return 7, nil
	}))
	result := make(chan error, 1)
	go func() {
		_, err := NextID("user")
		result <- err
	}()
	<-entered
	newToken := ConfigureWorkerResolver(WorkerResolverFunc(func(string) (int64, error) { return 9, nil }))
	current, err := ConfigureWorkerLeaseForNamespace("user", 9, newToken, time.Now().Add(time.Minute))
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	if err := <-result; err == nil {
		t.Fatal("old resolver result was accepted")
	}
	if !current.Valid() {
		t.Fatal("old resolver revoked the new lease")
	}
	id, err := NextID("user")
	if err != nil || snowflakeWorkerIDFromID(id) != 9 {
		t.Fatalf("current NextID() = %d, %v", id, err)
	}
}

// TestNextIDConcurrentLeaseBinding 验证并发首次获取同一租约后仍共用一个生成器序列。
func TestNextIDConcurrentLeaseBinding(t *testing.T) {
	resetWorkerIDForTest()
	t.Cleanup(resetWorkerIDForTest)
	token := ConfigureWorkerResolver(WorkerResolverFunc(func(string) (int64, error) { return 7, nil }))
	if _, err := ConfigureWorkerLeaseForNamespace("user", 7, token, time.Now().Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	const count = 64 // 并发首次发号量，不依赖顺序验证唯一性。
	ids := make(chan int64, count)
	var workers sync.WaitGroup
	for range count {
		workers.Go(func() {
			id, err := NextID("user")
			if err != nil {
				t.Error(err)
				return
			}
			ids <- id
		})
	}
	workers.Wait()
	close(ids)
	seen := make(map[int64]struct{}, count)
	for id := range ids {
		if _, exists := seen[id]; exists {
			t.Fatalf("duplicated ID: %d", id)
		}
		seen[id] = struct{}{}
	}
	if len(seen) != count {
		t.Fatalf("generated IDs = %d, want %d", len(seen), count)
	}
}

// TestSnowflakeNextID 验证雪花 ID 在同一生成器内单调递增。
func TestSnowflakeNextID(t *testing.T) {
	generator, err := NewSnowflake(1)
	if err != nil {
		t.Fatalf("NewSnowflake() error = %v", err)
	}
	first, err := generator.NextID()
	if err != nil {
		t.Fatalf("NextID() first error = %v", err)
	}
	second, err := generator.NextID()
	if err != nil {
		t.Fatalf("NextID() second error = %v", err)
	}
	if second <= first {
		t.Fatalf("NextID() not increasing first=%d second=%d", first, second)
	}
}

// TestSnowflakeConcurrentUniqueIDs 验证成熟库节点在同一 worker 内并发生成 ID 不重复。
func TestSnowflakeConcurrentUniqueIDs(t *testing.T) {
	generator, err := NewSnowflake(31)
	if err != nil {
		t.Fatalf("NewSnowflake() error = %v", err)
	}

	const (
		workers       = 8                      // 并发调用同一生成器，覆盖内部序列竞争。
		perWorkerIDs  = 512                    // 每协程连续取号，避免只测单次调用。
		expectedTotal = workers * perWorkerIDs // 同时作为聚合容量和完整性断言基准。
	)
	// 主协程等待全部生成完成后才读取，缓冲区必须容纳整批 ID。
	ids := make(chan int64, expectedTotal)
	// 多个协程共享同一生成器，覆盖内部时间序列和锁竞争。
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perWorkerIDs; j++ {
				id, nextErr := generator.NextID()
				if nextErr != nil {
					t.Errorf("NextID() error = %v", nextErr)
					return
				}
				ids <- id
			}
		}()
	}
	wg.Wait()
	close(ids)

	// 汇总阶段同时验证 worker 位和全量唯一性。
	seen := make(map[int64]struct{}, expectedTotal)
	for id := range ids {
		if got := snowflakeWorkerIDFromID(id); got != 31 {
			t.Fatalf("worker id in snowflake = %d want=31", got)
		}
		if _, ok := seen[id]; ok {
			t.Fatalf("duplicated snowflake id: %d", id)
		}
		seen[id] = struct{}{}
	}
	if len(seen) != expectedTotal {
		t.Fatalf("generated id count = %d want=%d", len(seen), expectedTotal)
	}
}

// TestNextIDRequiresWorkerID 验证进程级 ID 生成必须先显式配置 worker_id。
func TestNextIDRequiresWorkerID(t *testing.T) {
	resetWorkerIDForTest()
	t.Cleanup(resetWorkerIDForTest)

	if _, err := NextID("user"); err == nil || !strings.Contains(err.Error(), "worker_id 未配置") {
		t.Fatalf("NextID() error = %v, want missing worker_id", err)
	}
}

// TestNextIDRejectsNonCanonicalNamespace 验证调用方必须传入原样规范的业务命名空间。
func TestNextIDRejectsNonCanonicalNamespace(t *testing.T) {
	resetWorkerIDForTest()
	t.Cleanup(resetWorkerIDForTest)

	if err := ConfigureWorkerID(12); err != nil {
		t.Fatalf("ConfigureWorkerID() error = %v", err)
	}
	if _, err := NextID(" "); err == nil || !strings.Contains(err.Error(), "namespace 不能为空") {
		t.Fatalf("NextID(empty namespace) error = %v, want namespace error", err)
	}
	if _, err := NextID(" user "); err == nil || !strings.Contains(err.Error(), "首尾空白") {
		t.Fatalf("NextID(non-canonical namespace) error = %v, want whitespace error", err)
	}
	if err := ConfigureWorkerIDForNamespace(" user", 12); err == nil {
		t.Fatal("ConfigureWorkerIDForNamespace() 应拒绝首尾空白")
	}
}

// TestNextIDUsesConfiguredWorkerID 验证配置 worker_id 会进入雪花 ID worker 段。
func TestNextIDUsesConfiguredWorkerID(t *testing.T) {
	resetWorkerIDForTest()
	t.Cleanup(resetWorkerIDForTest)

	if err := ConfigureWorkerID(12); err != nil {
		t.Fatalf("ConfigureWorkerID() error = %v", err)
	}
	id, err := NextID("user")
	if err != nil {
		t.Fatalf("NextID() error = %v", err)
	}
	if got := snowflakeWorkerIDFromID(id); got != 12 {
		t.Fatalf("worker id in snowflake = %d want=12", got)
	}
}

// TestReleaseWorkerIDStopsNextID 验证释放后的本地 worker 禁止发号，不包含 Redis 租约释放验证。
func TestReleaseWorkerIDStopsNextID(t *testing.T) {
	resetWorkerIDForTest()
	t.Cleanup(resetWorkerIDForTest)

	if err := ConfigureWorkerID(12); err != nil {
		t.Fatalf("ConfigureWorkerID() error = %v", err)
	}
	ReleaseWorkerID(12)
	if _, err := NextID("user"); err == nil || !strings.Contains(err.Error(), "租约已释放") {
		t.Fatalf("NextID() error = %v, want released lease error", err)
	}
}

// TestConfigureWorkerIDClearsWorkerCache 验证 worker 变更会清空旧进程生成器缓存。
func TestConfigureWorkerIDClearsWorkerCache(t *testing.T) {
	resetWorkerIDForTest()
	t.Cleanup(resetWorkerIDForTest)

	if err := ConfigureWorkerID(12); err != nil {
		t.Fatalf("ConfigureWorkerID(12) error = %v", err)
	}
	if _, err := NextID("user"); err != nil {
		t.Fatalf("NextID() first error = %v", err)
	}
	if err := ConfigureWorkerID(13); err != nil {
		t.Fatalf("ConfigureWorkerID(13) error = %v", err)
	}
	id, err := NextID("user")
	if err != nil {
		t.Fatalf("NextID() second error = %v", err)
	}
	if got := snowflakeWorkerIDFromID(id); got != 13 {
		t.Fatalf("worker id after reconfigure = %d want=13", got)
	}
}

// TestNextIDIsolatesWorkerGeneratorAcrossNamespaces 验证不同业务命名空间使用独立本地生成器。
func TestNextIDIsolatesWorkerGeneratorAcrossNamespaces(t *testing.T) {
	resetWorkerIDForTest()
	t.Cleanup(resetWorkerIDForTest)

	if err := ConfigureWorkerID(12); err != nil {
		t.Fatalf("ConfigureWorkerID() error = %v", err)
	}
	if _, err := NextID("user"); err != nil {
		t.Fatalf("NextID(user) error = %v", err)
	}
	if _, err := NextID("order"); err != nil {
		t.Fatalf("NextID(order) error = %v", err)
	}
	if _, ok := workerGenerators.Load(snowflakeGeneratorKey{namespace: "user", workerID: 12}); !ok {
		t.Fatal("worker generator for user namespace missing")
	}
	if _, ok := workerGenerators.Load(snowflakeGeneratorKey{namespace: "order", workerID: 12}); !ok {
		t.Fatal("worker generator for order namespace missing")
	}
	count := 0
	workerGenerators.Range(func(_, _ any) bool {
		count++
		return true
	})
	if count != 2 {
		t.Fatalf("worker generator count = %d want=2", count)
	}
}

// TestConfigureWorkerIDForNamespace 验证单业务命名空间可独立绑定 worker_id。
func TestConfigureWorkerIDForNamespace(t *testing.T) {
	resetWorkerIDForTest()
	t.Cleanup(resetWorkerIDForTest)

	if err := ConfigureWorkerIDForNamespace("recharge.order", 21); err != nil {
		t.Fatalf("ConfigureWorkerIDForNamespace() error = %v", err)
	}
	id, err := NextID("recharge.order")
	if err != nil {
		t.Fatalf("NextID(recharge.order) error = %v", err)
	}
	if got := snowflakeWorkerIDFromID(id); got != 21 {
		t.Fatalf("worker id = %d want=21", got)
	}
	if _, err = NextID("withdraw.order"); err == nil || !strings.Contains(err.Error(), "worker_id 未配置") {
		t.Fatalf("NextID(withdraw.order) error = %v, want missing worker", err)
	}
}

// TestNextIDUsesSegmentResolver 验证启用 Segment 的业务 namespace 不依赖雪花 worker_id。
func TestNextIDUsesSegmentResolver(t *testing.T) {
	resetWorkerIDForTest()
	t.Cleanup(resetWorkerIDForTest)

	resolver := &fakeSegmentResolver{
		enabled: map[string]bool{"recharge.order": true},
		next:    1000,
	}
	token := ConfigureSegmentResolver(resolver)
	t.Cleanup(func() { ClearSegmentResolver(token) })

	first, err := NextID("recharge.order")
	if err != nil {
		t.Fatalf("NextID(segment first) error = %v", err)
	}
	second, err := NextID("recharge.order")
	if err != nil {
		t.Fatalf("NextID(segment second) error = %v", err)
	}
	if first != 1001 || second != 1002 {
		t.Fatalf("segment ids = %d,%d want 1001,1002", first, second)
	}
	if _, ok := CurrentWorkerID("recharge.order"); ok {
		t.Fatal("segment namespace should not configure snowflake worker")
	}
}

// TestNextIDFallsBackToSnowflakeForNonSegmentNamespace 验证未配置 Segment 的业务仍走默认雪花策略。
func TestNextIDFallsBackToSnowflakeForNonSegmentNamespace(t *testing.T) {
	resetWorkerIDForTest()
	t.Cleanup(resetWorkerIDForTest)

	resolver := &fakeSegmentResolver{enabled: map[string]bool{"recharge.order": true}}
	token := ConfigureSegmentResolver(resolver)
	t.Cleanup(func() { ClearSegmentResolver(token) })
	if err := ConfigureWorkerID(12); err != nil {
		t.Fatalf("ConfigureWorkerID() error = %v", err)
	}
	id, err := NextID("withdraw.order")
	if err != nil {
		t.Fatalf("NextID(non segment) error = %v", err)
	}
	if got := snowflakeWorkerIDFromID(id); got != 12 {
		t.Fatalf("worker id = %d want=12", got)
	}
}

// TestShardNo 验证用户分片号固定来源于 ID 字符串 CRC32 哈希。
func TestShardNo(t *testing.T) {
	tests := []struct {
		id   int64 // id 表示待计算分片号的用户 ID。
		want int   // want 表示按 CRC32 规则期望得到的分片号。
	}{
		{id: 0, want: stableShardNoForTest(0)},
		{id: 999, want: stableShardNoForTest(999)},
		{id: 1000, want: stableShardNoForTest(1000)},
		{id: 1001, want: stableShardNoForTest(1001)},
	}
	for _, tt := range tests {
		if got := ShardNo(tt.id); got != tt.want {
			t.Fatalf("ShardNo(%d)=%d want=%d", tt.id, got, tt.want)
		}
	}
	if ShardNo(1024) == 0 {
		t.Fatal("ShardNo(1024) should not fall back to raw id%1024")
	}
}

// TestResolveWorkerIDRejectsUnset 验证静态模式只接受配置文件中的 worker_id。
func TestResolveWorkerIDRejectsUnset(t *testing.T) {
	resetWorkerIDForTest()
	t.Cleanup(resetWorkerIDForTest)
	if _, err := ResolveWorkerID(SnowflakeWorkerIDUnset); err == nil {
		t.Fatal("ResolveWorkerID() 应拒绝未配置 worker_id")
	}
}

// snowflakeWorkerIDFromID 从雪花 ID 中解析 worker_id 位段。
func snowflakeWorkerIDFromID(id int64) int64 {
	return (id >> snowflakeSequenceBits) & snowflakeMaxWorkerID
}

// stableShardNoForTest 使用与生产一致的 CRC32 规则计算测试期望分片号。
func stableShardNoForTest(id int64) int {
	return int(crc32.ChecksumIEEE([]byte(strconv.FormatInt(id, 10))) % ShardMod)
}

// fakeSegmentResolver 仅用于串行策略分派测试，不模拟 Redis 号段分配或并发持久化。
type fakeSegmentResolver struct {
	enabled map[string]bool // enabled 标记哪些业务 namespace 启用 Segment
	next    int64           // next 保存最后一次返回的 ID
}

// SegmentEnabled 判断测试 namespace 是否启用 Segment。
func (f *fakeSegmentResolver) SegmentEnabled(namespace string) bool {
	return f.enabled[namespace]
}

// SegmentID 返回递增的测试 Segment ID。
func (f *fakeSegmentResolver) SegmentID(string) (int64, error) {
	f.next++
	return f.next, nil
}
