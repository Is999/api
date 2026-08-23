package configload

import (
	"context"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"api/common/idgen"
	keys "api/common/rediskeys"
	"api/internal/config"
	"api/internal/infra/redisx"

	"github.com/alicebob/miniredis/v2"
	miniredisserver "github.com/alicebob/miniredis/v2/server"
	"github.com/redis/go-redis/v9"
)

// TestSnowflakeLeaseRuntimeWindow 覆盖生产 Redis 工厂下续租超期、取消和正常延期的实际发号入口。
func TestSnowflakeLeaseRuntimeWindow(t *testing.T) {
	for _, mode := range []string{"blocked", "close", "healthy", "real_redis"} {
		t.Run(mode, func(t *testing.T) {
			cleanupSnowflakeWorker(t)
			var server *miniredis.Miniredis
			address := ""
			if mode == "real_redis" {
				address = os.Getenv("INTEGRATION_REDIS_ADDR")
				if address == "" {
					t.Skip("设置 INTEGRATION_REDIS_ADDR 后运行真实 Redis 租约延期回归")
				}
			} else {
				server = miniredis.RunT(t)
				address = server.Addr()
			}
			client, err := redisx.New(t.Context(), config.RedisConfig{
				Type: "single", Addrs: []string{address}, DB: 8, PoolSize: 8,
			}, config.ObservabilityConfig{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = client.Close() })
			// 每个用例使用独立 scope；真实 Redis 只回收该用例的一把租约 Key。
			cfg := config.SnowflakeConfig{Redis: config.SnowflakeRedisConfig{
				Enabled: true, Scope: "snowflake-window-" + strconv.FormatInt(time.Now().UnixNano(), 10),
				LeaseSeconds: 10, RenewIntervalSeconds: 1,
				Namespaces: map[string]config.SnowflakeRedisNamespaceConfig{"user": {NodeCount: 1}},
			}}
			if err := validateSnowflakeConfig(cfg); err != nil {
				t.Fatal(err)
			}
			runtime, err := ConfigureSnowflakeWorker(t.Context(), cfg, client)
			if err != nil {
				t.Fatal(err)
			}
			key := keys.SnowflakeNodeLeaseKey(cfg.Redis.Scope, "user", 0)
			blocked, release := make(chan struct{}, 1), make(chan struct{})
			var activeLease *snowflakeRedisLease
			t.Cleanup(func() {
				close(release)
				ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				_ = runtime.Close(ctx)
				if activeLease != nil {
					select {
					case <-activeLease.done:
					case <-ctx.Done():
						t.Error("fixture renewal did not exit")
					}
				}
				if err := client.Del(ctx, key).Err(); err != nil {
					t.Errorf("cleanup fixture lease: %v", err)
				}
			})
			if mode == "blocked" || mode == "close" {
				// 只阻塞续租回包，申请和观察命令仍可执行；真实客户端保留默认读超时及重试。
				server.Server().SetPreHook(func(_ *miniredisserver.Peer, command string, _ ...string) bool {
					if strings.EqualFold(command, "evalsha") || strings.EqualFold(command, "eval") {
						select {
						case blocked <- struct{}{}:
						default:
						}
						<-release
					}
					return false
				})
			}
			started := time.Now()
			if _, err := idgen.NextID("user"); err != nil {
				t.Fatal(err)
			}
			manager := runtime.(*snowflakeRedisLeaseManager)
			manager.mu.Lock()
			lease := manager.leases["user"]
			manager.mu.Unlock()
			activeLease = lease
			if mode == "blocked" || mode == "close" {
				select {
				case <-blocked:
				case <-time.After(3 * time.Second):
					t.Fatal("renew command was not reached")
				}
			}
			if mode == "close" {
				// 关闭立即撤销发号并传播取消；尚未返回的网络读仍受原期限约束。
				ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
				defer cancel()
				closeStarted := time.Now()
				_ = runtime.Close(ctx)
				if time.Since(closeStarted) > time.Second || lease.renewCtx.Err() == nil || lease.workerLease.Valid() {
					t.Fatal("close did not cancel renewal and revoke the local worker")
				}
				if id, err := idgen.NextID("user"); err == nil || id != 0 {
					t.Fatalf("closed NextID() = %d, %v", id, err)
				}
				return
			}
			// 最短合法租约的安全窗口为九秒，观察点仍早于远端十秒 TTL。
			<-time.After(time.Until(started.Add(9250 * time.Millisecond)))
			if mode == "blocked" {
				if id, err := idgen.NextID("user"); err == nil || id != 0 {
					t.Fatalf("expired NextID() = %d, %v", id, err)
				}
				select {
				case <-lease.done:
				case <-time.After(500 * time.Millisecond):
					t.Fatal("renew call exceeded the safe lease window")
				}
				// 模拟远端 TTL 到期后的接管，旧 owner 不能恢复本地发号。
				server.FastForward(11 * time.Second)
				if err := client.Set(t.Context(), key, "replacement-owner", time.Minute).Err(); err != nil {
					t.Fatal(err)
				}
				if id, err := idgen.NextID("user"); err == nil || id != 0 {
					t.Fatalf("replaced NextID() = %d, %v", id, err)
				}
				return
			}
			// 正常续租跨过初始安全窗口后仍可发号，远端 TTL 也应保持正值。
			if _, err := idgen.NextID("user"); err != nil {
				t.Fatal(err)
			}
			if ttl, err := client.PTTL(t.Context(), key).Result(); err != nil || ttl <= 0 || ttl > 10*time.Second {
				t.Fatalf("renewed PTTL() = %v, %v", ttl, err)
			}
		})
	}
}

const (
	snowflakeTestScope             = "unit"           // snowflakeTestScope 表示测试部署级租约池。
	snowflakeTestUserNamespace     = "user"           // snowflakeTestUserNamespace 表示用户业务 ID 命名空间。
	snowflakeTestRechargeNamespace = "recharge.order" // snowflakeTestRechargeNamespace 表示充值订单业务 ID 命名空间。
	snowflakeTestWithdrawNamespace = "withdraw.order" // snowflakeTestWithdrawNamespace 表示提现订单业务 ID 命名空间。
)

// TestConfigureSnowflakeWorkerAcquiresRedisLease 验证关闭时立即释放本地 worker，但 Redis key 保留完整 TTL 隔离窗口。
func TestConfigureSnowflakeWorkerAcquiresRedisLease(t *testing.T) {
	// 首次取号才按 namespace 惰性申请 worker 租约。
	cleanupSnowflakeWorker(t)
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	lease, err := ConfigureSnowflakeWorker(context.Background(), redisSnowflakeConfig(snowflakeTestScope), client)
	if err != nil {
		t.Fatalf("ConfigureSnowflakeWorker() error = %v", err)
	}
	if _, ok := idgen.CurrentWorkerID(snowflakeTestUserNamespace); ok {
		t.Fatal("expected no namespace worker before first ID")
	}
	if _, err = idgen.NextID(snowflakeTestUserNamespace); err != nil {
		t.Fatalf("NextID(user) error = %v", err)
	}
	workerID, ok := idgen.CurrentWorkerID(snowflakeTestUserNamespace)
	if !ok {
		t.Fatal("expected user worker_id to be configured")
	}
	key := keys.SnowflakeNodeLeaseKey(snowflakeTestScope, snowflakeTestUserNamespace, workerID)
	if !server.Exists(key) {
		t.Fatalf("expected snowflake lease key %s to exist", key)
	}
	// 关闭立即撤销本地 worker，但 Redis key 保留完整隔离 TTL。
	if err = lease.Close(context.Background()); err != nil {
		t.Fatalf("lease.Close() error = %v", err)
	}
	if !server.Exists(key) {
		t.Fatalf("expected snowflake lease key %s to remain during shutdown quarantine", key)
	}
	if ttl := server.TTL(key); ttl < 29*time.Second || ttl > 30*time.Second {
		t.Fatalf("shutdown quarantine TTL = %s, want refreshed 30s", ttl)
	}
	if _, ok = idgen.CurrentWorkerID(snowflakeTestUserNamespace); ok {
		t.Fatal("expected user worker_id to be released")
	}
}

// TestConfigureSnowflakeWorkerQuarantinesClosedNode 确保单节点池在上一实例停机 TTL 到期前不会跨主机复用 worker_id。
func TestConfigureSnowflakeWorkerQuarantinesClosedNode(t *testing.T) {
	// 单节点池便于精确验证停机隔离期间不能重新分配。
	cleanupSnowflakeWorker(t)
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cfg := redisSnowflakeConfig(snowflakeTestScope)
	cfg.Redis.LeaseSeconds = 10
	cfg.Redis.RenewIntervalSeconds = 2
	cfg.Redis.Namespaces = map[string]config.SnowflakeRedisNamespaceConfig{
		snowflakeTestUserNamespace: {NodeCount: 1},
	}

	first, err := ConfigureSnowflakeWorker(context.Background(), cfg, client)
	if err != nil {
		t.Fatalf("ConfigureSnowflakeWorker(first) error = %v", err)
	}
	if _, err = idgen.NextID(snowflakeTestUserNamespace); err != nil {
		t.Fatalf("NextID(first) error = %v", err)
	}
	if err = first.Close(context.Background()); err != nil {
		t.Fatalf("first.Close() error = %v", err)
	}

	// 第二实例可以启动，但首次取号应因隔离 key 占用而失败。
	second, err := ConfigureSnowflakeWorker(context.Background(), cfg, client)
	if err != nil {
		t.Fatalf("ConfigureSnowflakeWorker(second) error = %v", err)
	}
	if _, err = idgen.NextID(snowflakeTestUserNamespace); err == nil || !strings.Contains(err.Error(), "池已耗尽") {
		t.Fatalf("NextID(before quarantine expiry) error = %v, want exhausted pool", err)
	}
	if err = second.Close(context.Background()); err != nil {
		t.Fatalf("second.Close() error = %v", err)
	}

	// TTL 到期后同一 worker 才允许被新实例再次使用。
	server.FastForward(11 * time.Second)
	third, err := ConfigureSnowflakeWorker(context.Background(), cfg, client)
	if err != nil {
		t.Fatalf("ConfigureSnowflakeWorker(third) error = %v", err)
	}
	if _, err = idgen.NextID(snowflakeTestUserNamespace); err != nil {
		t.Fatalf("NextID(after quarantine expiry) error = %v", err)
	}
	if err = third.Close(context.Background()); err != nil {
		t.Fatalf("third.Close() error = %v", err)
	}
}

// TestConfigureSnowflakeWorkerUsesNamespaceNodeCount 验证 namespace 可限制可竞争的 node_id 池大小。
func TestConfigureSnowflakeWorkerUsesNamespaceNodeCount(t *testing.T) {
	cleanupSnowflakeWorker(t)
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	cfg := redisSnowflakeConfig(snowflakeTestScope)
	cfg.Redis.Namespaces = map[string]config.SnowflakeRedisNamespaceConfig{
		snowflakeTestUserNamespace: {NodeCount: 10},
	}
	lease, err := ConfigureSnowflakeWorker(context.Background(), cfg, client)
	if err != nil {
		t.Fatalf("ConfigureSnowflakeWorker() error = %v", err)
	}
	if _, err = idgen.NextID(snowflakeTestUserNamespace); err != nil {
		t.Fatalf("NextID(user) error = %v", err)
	}
	workerID, ok := idgen.CurrentWorkerID(snowflakeTestUserNamespace)
	if !ok {
		t.Fatal("expected user worker_id to be configured")
	}
	if workerID < 0 || workerID >= 10 {
		t.Fatalf("user worker_id = %d, want 0-9", workerID)
	}
	if err = lease.Close(context.Background()); err != nil {
		t.Fatalf("lease.Close() error = %v", err)
	}
}

// TestConfigureSnowflakeWorkerReportsNamespaceNodePoolExhausted 验证 namespace 小池位耗尽时不会越界抢占。
func TestConfigureSnowflakeWorkerReportsNamespaceNodePoolExhausted(t *testing.T) {
	cleanupSnowflakeWorker(t)
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	cfg := redisSnowflakeConfig(snowflakeTestScope)
	cfg.Redis.Namespaces = map[string]config.SnowflakeRedisNamespaceConfig{
		snowflakeTestUserNamespace: {NodeCount: 10},
	}
	for workerID := int64(0); workerID < 10; workerID++ {
		key := keys.SnowflakeNodeLeaseKey(snowflakeTestScope, snowflakeTestUserNamespace, workerID)
		server.Set(key, "occupied")
	}
	lease, err := ConfigureSnowflakeWorker(context.Background(), cfg, client)
	if err != nil {
		t.Fatalf("ConfigureSnowflakeWorker() error = %v", err)
	}
	if _, err = idgen.NextID(snowflakeTestUserNamespace); err == nil || !strings.Contains(err.Error(), "range=0-9") {
		t.Fatalf("NextID(user) error = %v, want exhausted range=0-9", err)
	}
	if err = lease.Close(context.Background()); err != nil {
		t.Fatalf("lease.Close() error = %v", err)
	}
}

// TestConfigureSnowflakeWorkerSkipsOccupiedRedisNode 验证已有租约不会被第二个实例复用。
func TestConfigureSnowflakeWorkerSkipsOccupiedRedisNode(t *testing.T) {
	cleanupSnowflakeWorker(t)
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	first, err := ConfigureSnowflakeWorker(context.Background(), redisSnowflakeConfig(snowflakeTestScope), client)
	if err != nil {
		t.Fatalf("ConfigureSnowflakeWorker(first) error = %v", err)
	}
	if _, err = idgen.NextID(snowflakeTestUserNamespace); err != nil {
		t.Fatalf("NextID(first user) error = %v", err)
	}
	firstWorkerID, _ := idgen.CurrentWorkerID(snowflakeTestUserNamespace)
	second, err := ConfigureSnowflakeWorker(context.Background(), redisSnowflakeConfig(snowflakeTestScope), client)
	if err != nil {
		t.Fatalf("ConfigureSnowflakeWorker(second) error = %v", err)
	}
	if _, err = idgen.NextID(snowflakeTestUserNamespace); err != nil {
		t.Fatalf("NextID(second user) error = %v", err)
	}
	secondWorkerID, _ := idgen.CurrentWorkerID(snowflakeTestUserNamespace)
	if secondWorkerID == firstWorkerID {
		t.Fatalf("expected second lease to use another node_id, got %d", secondWorkerID)
	}
	if err = first.Close(context.Background()); err != nil {
		t.Fatalf("first.Close() error = %v", err)
	}
	if err = second.Close(context.Background()); err != nil {
		t.Fatalf("second.Close() error = %v", err)
	}
}

// TestConfigureSnowflakeWorkerIsolatesBusinessNamespaces 验证不同业务 namespace 使用相互独立的 Redis 租约 key。
func TestConfigureSnowflakeWorkerIsolatesBusinessNamespaces(t *testing.T) {
	// 同一管理器分别触发两个业务空间的惰性租约申请。
	cleanupSnowflakeWorker(t)
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	lease, err := ConfigureSnowflakeWorker(context.Background(), redisSnowflakeConfig(snowflakeTestScope), client)
	if err != nil {
		t.Fatalf("ConfigureSnowflakeWorker() error = %v", err)
	}
	if _, err = idgen.NextID(snowflakeTestRechargeNamespace); err != nil {
		t.Fatalf("NextID(recharge.order) error = %v", err)
	}
	rechargeWorkerID, ok := idgen.CurrentWorkerID(snowflakeTestRechargeNamespace)
	if !ok {
		t.Fatal("expected recharge namespace worker_id")
	}
	if _, err = idgen.NextID(snowflakeTestWithdrawNamespace); err != nil {
		t.Fatalf("NextID(withdraw.order) error = %v", err)
	}
	withdrawWorkerID, ok := idgen.CurrentWorkerID(snowflakeTestWithdrawNamespace)
	if !ok {
		t.Fatal("expected withdraw namespace worker_id")
	}
	// namespace 必须进入 Redis key，避免相同 worker_id 发生跨业务冲突。
	rechargeKey := keys.SnowflakeNodeLeaseKey(snowflakeTestScope, snowflakeTestRechargeNamespace, rechargeWorkerID)
	withdrawKey := keys.SnowflakeNodeLeaseKey(snowflakeTestScope, snowflakeTestWithdrawNamespace, withdrawWorkerID)
	if rechargeKey == withdrawKey {
		t.Fatalf("expected different namespace lease keys, got %s", rechargeKey)
	}
	if !server.Exists(rechargeKey) || !server.Exists(withdrawKey) {
		t.Fatalf("expected both namespace lease keys to exist recharge=%s withdraw=%s", rechargeKey, withdrawKey)
	}
	// 关闭后两个租约均保留隔离窗口。
	if err = lease.Close(context.Background()); err != nil {
		t.Fatalf("lease.Close() error = %v", err)
	}
	if !server.Exists(rechargeKey) || !server.Exists(withdrawKey) {
		t.Fatalf("expected namespace lease keys to remain quarantined")
	}
}

// TestConfigureSnowflakeWorkerRejectsMissingRedisClient 验证 Redis 模式不会静默回退静态 worker。
func TestConfigureSnowflakeWorkerRejectsMissingRedisClient(t *testing.T) {
	cleanupSnowflakeWorker(t)
	if _, err := ConfigureSnowflakeWorker(context.Background(), redisSnowflakeConfig(snowflakeTestScope), nil); err == nil {
		t.Fatal("expected nil redis client to be rejected")
	}
}

// TestSnowflakeLeaseManagerCloseCancelsPendingAcquire 验证停机能取消持锁的 Redis 申请并完成管理器收口。
func TestSnowflakeLeaseManagerCloseCancelsPendingAcquire(t *testing.T) {
	// 自定义 Dialer 阻塞到上下文取消，模拟正在建立的 Redis 连接。
	dialStarted := make(chan struct{}, 1)
	client := redis.NewClient(&redis.Options{
		Addr:       "blocked.invalid:6379",
		MaxRetries: -1,
		Dialer: func(ctx context.Context, _, _ string) (net.Conn, error) {
			select {
			case dialStarted <- struct{}{}:
			default:
			}
			<-ctx.Done()
			return nil, ctx.Err()
		},
	})
	t.Cleanup(func() { _ = client.Close() })

	runtimeCtx, cancel := context.WithCancel(context.Background())
	manager := &snowflakeRedisLeaseManager{
		client: client,
		cfg: config.SnowflakeRedisConfig{
			Scope:        snowflakeTestScope,
			LeaseSeconds: 30,
			Namespaces: map[string]config.SnowflakeRedisNamespaceConfig{
				snowflakeTestUserNamespace: {NodeCount: 1},
			},
		},
		owner:  "test-owner",
		ctx:    runtimeCtx,
		cancel: cancel,
		leases: make(map[string]*snowflakeRedisLease),
	}
	// 确认申请已进入 Dialer 后再关闭管理器，避免测试启动竞态。
	acquireDone := make(chan error, 1)
	go func() {
		_, err := manager.SnowflakeWorkerID(snowflakeTestUserNamespace)
		acquireDone <- err
	}()
	select {
	case <-dialStarted:
	case <-time.After(time.Second):
		t.Fatal("Redis acquire did not reach dialer")
	}

	// Close 必须传播取消并让等待中的取号调用有界退出。
	closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
	defer closeCancel()
	if err := manager.Close(closeCtx); err != nil {
		t.Fatalf("manager.Close() error = %v", err)
	}
	select {
	case err := <-acquireDone:
		if err == nil || !strings.Contains(err.Error(), "context canceled") {
			t.Fatalf("SnowflakeWorkerID() error = %v, want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("pending Redis acquire was not canceled")
	}
}

// TestSnowflakeLeaseCloseHonorsContextDeadline 确保续约协程阻塞时租约关闭不会突破应用停止期限。
func TestSnowflakeLeaseCloseHonorsContextDeadline(t *testing.T) {
	lease := &snowflakeRedisLease{
		stop:      make(chan struct{}),
		done:      make(chan struct{}),
		closeDone: make(chan struct{}),
		onRelease: func(string, int64) {},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := lease.Close(ctx); err == nil {
		t.Fatal("Close() expected context deadline error")
	}
}

// TestConfigureSnowflakeWorkerUsesSegmentForConfiguredNamespace 验证配置的高吞吐 namespace 使用 Redis Segment 号段。
func TestConfigureSnowflakeWorkerUsesSegmentForConfiguredNamespace(t *testing.T) {
	cleanupSnowflakeWorker(t)
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	cfg := redisSnowflakeConfig(snowflakeTestScope)
	cfg.Segment = testIDSegmentConfig(snowflakeTestScope, snowflakeTestRechargeNamespace, 3)
	lease, err := ConfigureSnowflakeWorker(context.Background(), cfg, client)
	if err != nil {
		t.Fatalf("ConfigureSnowflakeWorker() error = %v", err)
	}
	first, err := idgen.NextID(snowflakeTestRechargeNamespace)
	if err != nil {
		t.Fatalf("NextID(segment first) error = %v", err)
	}
	second, err := idgen.NextID(snowflakeTestRechargeNamespace)
	if err != nil {
		t.Fatalf("NextID(segment second) error = %v", err)
	}
	if first != 1 || second != 2 {
		t.Fatalf("segment ids = %d,%d want 1,2", first, second)
	}
	if _, ok := idgen.CurrentWorkerID(snowflakeTestRechargeNamespace); ok {
		t.Fatal("segment namespace should not acquire snowflake worker")
	}
	key := keys.IDSegmentCounterKey(snowflakeTestScope, snowflakeTestRechargeNamespace)
	if got, _ := server.Get(key); got != "3" {
		t.Fatalf("segment high-water = %q want 3", got)
	}
	if err = lease.Close(context.Background()); err != nil {
		t.Fatalf("lease.Close() error = %v", err)
	}
}

// TestSegmentIDContinuesFromLocalRangeWhenRedisUnavailable 验证 Redis 短暂不可用时可继续消耗本地号段。
func TestSegmentIDContinuesFromLocalRangeWhenRedisUnavailable(t *testing.T) {
	// 步长为三的首个号段在 Redis 正常时一次性申请。
	cleanupSnowflakeWorker(t)
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	cfg := config.SnowflakeConfig{
		WorkerID: int64Ptr(12),
		Segment:  testIDSegmentConfig(snowflakeTestScope, snowflakeTestRechargeNamespace, 3),
	}
	cfg.Segment.AllocateTimeoutSeconds = 1
	lease, err := ConfigureSnowflakeWorker(context.Background(), cfg, client)
	if err != nil {
		t.Fatalf("ConfigureSnowflakeWorker() error = %v", err)
	}
	// 取出首个 ID 后关闭 Redis，剩余两个本地 ID 仍应连续可用。
	for want := int64(1); want <= 3; want++ {
		id, nextErr := idgen.NextID(snowflakeTestRechargeNamespace)
		if nextErr != nil {
			t.Fatalf("NextID(local segment %d) error = %v", want, nextErr)
		}
		if id != want {
			t.Fatalf("NextID(local segment) = %d want %d", id, want)
		}
		if want == 1 {
			_ = client.Close()
			server.Close()
		}
	}
	// 本地号段耗尽后必须返回依赖错误，不能生成越界 ID。
	if _, err = idgen.NextID(snowflakeTestRechargeNamespace); err == nil {
		t.Fatal("expected exhausted local segment to fail when Redis is unavailable")
	}
	if err = lease.Close(context.Background()); err != nil {
		t.Fatalf("lease.Close() error = %v", err)
	}
}

// TestConfigureSnowflakeWorkerSegmentCloseStopsNamespace 验证关闭运行期资源后 Segment namespace 不会继续发号。
func TestConfigureSnowflakeWorkerSegmentCloseStopsNamespace(t *testing.T) {
	cleanupSnowflakeWorker(t)
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	cfg := redisSnowflakeConfig(snowflakeTestScope)
	cfg.Segment = testIDSegmentConfig(snowflakeTestScope, snowflakeTestRechargeNamespace, 3)
	lease, err := ConfigureSnowflakeWorker(context.Background(), cfg, client)
	if err != nil {
		t.Fatalf("ConfigureSnowflakeWorker() error = %v", err)
	}
	if _, err = idgen.NextID(snowflakeTestRechargeNamespace); err != nil {
		t.Fatalf("NextID(segment) error = %v", err)
	}
	if err = lease.Close(context.Background()); err != nil {
		t.Fatalf("lease.Close() error = %v", err)
	}
	if _, err = idgen.NextID(snowflakeTestRechargeNamespace); err == nil {
		t.Fatal("expected segment namespace to stop after runtime close")
	}
}

// redisSnowflakeConfig 返回测试使用的 Redis 租约配置。
func redisSnowflakeConfig(scope string) config.SnowflakeConfig {
	return config.SnowflakeConfig{
		Redis: config.SnowflakeRedisConfig{
			Enabled:              true,
			Scope:                scope,
			LeaseSeconds:         30,
			RenewIntervalSeconds: 5,
		},
	}
}

// testIDSegmentConfig 返回单 namespace 测试 Segment 配置。
func testIDSegmentConfig(scope string, namespace string, step int64) config.IDSegmentConfig {
	return config.IDSegmentConfig{
		Enabled:                true,
		Scope:                  scope,
		AllocateTimeoutSeconds: 1,
		Namespaces: map[string]config.IDSegmentNamespaceConfig{
			namespace: {
				Enabled:           true,
				Step:              step,
				PrefetchThreshold: 0,
			},
		},
	}
}

// cleanupSnowflakeWorker 清理进程级雪花 worker，避免测试间串扰。
func cleanupSnowflakeWorker(t *testing.T) {
	t.Helper()
	idgen.ClearWorkerResolver(0)
	idgen.ClearSegmentResolver(0)
	t.Cleanup(func() {
		idgen.ClearWorkerResolver(0)
		idgen.ClearSegmentResolver(0)
	})
}
