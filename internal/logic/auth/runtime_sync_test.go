package auth

import (
	"context"
	"crypto/rand"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	codes "api/common/codes"
	"api/common/idgen"
	keys "api/common/rediskeys"
	"api/common/runtimecfg"
	"api/internal/config"
	userlogic "api/internal/logic/user"
	"api/internal/model"
	"api/internal/routealias"
	"api/internal/svc"
	"api/internal/types"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// TestSyncUserRuntimeDefaultsToProfileCache 验证未指定同步范围时默认只处理资料缓存。
func TestSyncUserRuntimeDefaultsToProfileCache(t *testing.T) {
	logicObj := NewAuthLogic(context.Background(), svc.NewServiceContext(config.Config{}, "test-version", svc.Dependencies{}))
	resp := logicObj.SyncUserRuntime(&types.UserRuntimeSyncReq{ID: 42, Reason: "manual"})
	if resp.Code != codes.UpdateSuccess {
		t.Fatalf("SyncUserRuntime() code = %d, want %d", resp.Code, codes.UpdateSuccess)
	}
	data, ok := resp.Data.(*types.UserRuntimeSyncResp)
	if !ok {
		t.Fatalf("SyncUserRuntime() data type = %T, want *types.UserRuntimeSyncResp", resp.Data)
	}
	if data.UserID != 42 || !data.ProfileCacheInvalidated || data.SessionsInvalidated || data.Reason != "manual" {
		t.Fatalf("SyncUserRuntime() data = %+v, want profile-only sync", data)
	}
}

// TestSyncUserRuntimeSessionsRequireRedis 验证登录态失效必须由 API 进程持有 Redis 后才能执行。
func TestSyncUserRuntimeSessionsRequireRedis(t *testing.T) {
	logicObj := NewAuthLogic(context.Background(), svc.NewServiceContext(config.Config{}, "test-version", svc.Dependencies{}))
	resp := logicObj.SyncUserRuntime(&types.UserRuntimeSyncReq{ID: 42, Sessions: true, AuthVersion: 2})
	if resp.Code != codes.ServerError {
		t.Fatalf("SyncUserRuntime() code = %d, want %d when Redis missing", resp.Code, codes.ServerError)
	}
}

// TestSyncUserRuntimeValidatesVersionBeforeCacheSideEffects 确保未提交版本不会先删除仍有效的资料缓存。
func TestSyncUserRuntimeValidatesVersionBeforeCacheSideEffects(t *testing.T) {
	svcCtx, rds, _ := newAuthFlowTestService(t)
	registerCtx := authFlowContext(AuthEventActionRegisterSuccess, string(routealias.AuthRegister), http.MethodPost, "/api/auth/register", "10.0.0.8")
	created := requireAuthTokenResp(t, NewAuthLogic(registerCtx, svcCtx).Register(&types.RegisterReq{
		Username: "runtime_sync_user",
		Password: "P@ssw0rd!",
	}), codes.CreateSuccess)
	if err := userlogic.NewUserLogic(t.Context(), svcCtx).CacheUserProfile(created.User.ID, created.User); err != nil {
		t.Fatalf("CacheUserProfile() error = %v", err)
	}
	profileKey := keys.WithPrefix(fmt.Sprintf(keys.UserProfile, created.User.ID))
	if rds.Exists(context.Background(), profileKey).Val() != 1 {
		t.Fatalf("测试前置资料缓存不存在 key=%s", profileKey)
	}

	result := NewAuthLogic(context.Background(), svcCtx).SyncUserRuntime(&types.UserRuntimeSyncReq{
		ID:          created.User.ID,
		Profile:     true,
		Sessions:    true,
		AuthVersion: 2,
	})
	if result.Code != codes.ServerError {
		t.Fatalf("SyncUserRuntime() code = %d, want %d", result.Code, codes.ServerError)
	}
	if rds.Exists(context.Background(), profileKey).Val() != 1 {
		t.Fatal("认证版本校验失败前不应删除用户资料缓存")
	}
}

// TestSyncUserRuntimeWaitsForInflightProfile 验证同步必须等待旧回源发布后再删除，不能提前报告成功。
func TestSyncUserRuntimeWaitsForInflightProfile(t *testing.T) {
	testProfileSyncWithInflightLoad(t, nil)
}

// TestSyncUserRuntimeReportsProfileInvalidationFailure 验证等待回源时取消必须向调用方返回失败，不宣称已失效。
func TestSyncUserRuntimeReportsProfileInvalidationFailure(t *testing.T) {
	service, client, _ := newAuthFlowTestService(t)
	const userID = int64(42)
	profileKey := keys.WithPrefix(fmt.Sprintf(keys.UserProfile, userID))
	lockKey := keys.WithPrefix(fmt.Sprintf(keys.UserProfileRebuildLock, userID))
	if err := client.Set(t.Context(), profileKey, "existing-profile", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	if err := client.Set(t.Context(), lockKey, "other-owner", time.Minute).Err(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	result := NewAuthLogic(ctx, service).SyncUserRuntime(&types.UserRuntimeSyncReq{ID: userID, Profile: true})
	if result.Code != codes.ServerError {
		t.Fatalf("runtime sync code=%d, want %d", result.Code, codes.ServerError)
	}
	if value, err := client.Get(t.Context(), profileKey).Result(); err != nil || value != "existing-profile" {
		t.Fatalf("profile after canceled sync=%q, error=%v", value, err)
	}
}

// TestSyncUserRuntimeRealRedis 使用真实 Redis 验证锁与删除顺序，数据库仍是 SQLite 回源屏障。
func TestSyncUserRuntimeRealRedis(t *testing.T) {
	// 仅显式提供测试 Redis 时运行；独立 DB 和随机站点隔离现有数据，不执行全库清理。
	addr := os.Getenv("API_PROFILE_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("set API_PROFILE_TEST_REDIS_ADDR to test with real Redis")
	}
	db := 9
	if value := os.Getenv("API_PROFILE_TEST_REDIS_DB"); value != "" {
		var err error
		db, err = strconv.Atoi(value)
		if err != nil || db < 0 {
			t.Fatalf("invalid API_PROFILE_TEST_REDIS_DB=%q", value)
		}
	}
	client := redis.NewClient(&redis.Options{Addr: addr, DB: db})
	t.Cleanup(func() { _ = client.Close() })
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatalf("test Redis is unavailable: %v", err)
	}
	testProfileSyncWithInflightLoad(t, client)
}

// testProfileSyncWithInflightLoad 对内存与真实 Redis 复用同一业务入口和受控回源时序。
func testProfileSyncWithInflightLoad(t *testing.T, client *redis.Client) {
	t.Helper()
	service, fixtureRedis, _ := newAuthFlowTestService(t)
	service.Collector = nil
	if client == nil {
		client = fixtureRedis.(*redis.Client)
	}
	service.Rds = client
	previousRuntime := runtimecfg.Get()
	cfg := service.CurrentConfig()
	cfg.AppID = "profile-sync-" + strings.ToLower(rand.Text())
	service.UpdateConfig(cfg)
	runtimecfg.Set(runtimecfg.Snapshot{AppID: cfg.AppID})
	t.Cleanup(func() { runtimecfg.Restore(previousRuntime) })

	// 直接准备已提交用户，不经过注册或会话创建，Redis 副作用仅有资料和现有回源锁。
	userID := time.Now().UnixNano()
	if err := service.WriteDB(svc.DatabaseMain).Create(&authFlowUserSQLite{
		ID: userID, ShardNo: idgen.ShardNo(userID), Username: "profile_sync", Nickname: "before",
		PasswordHash: "unused", Status: model.UserStatusEnabled, AuthVersion: 1,
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := service.WriteDB(svc.DatabaseMain).Create(&authFlowUserIdentitySQLite{
		IdentityValue: "profile_sync", UserID: userID, UserShardNo: idgen.ShardNo(userID),
	}).Error; err != nil {
		t.Fatal(err)
	}
	profileKey := keys.WithPrefix(fmt.Sprintf(keys.UserProfile, userID))
	lockKey := keys.WithPrefix(fmt.Sprintf(keys.UserProfileRebuildLock, userID))
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if err := client.Del(ctx, profileKey, lockKey).Err(); err != nil {
			t.Errorf("clean test profile keys: %v", err)
		}
	})

	loaded := make(chan struct{})
	release := make(chan struct{})
	var paused atomic.Bool
	db := service.WriteDB(svc.DatabaseMain)
	if err := db.Callback().Query().After("gorm:query").Register("test:pause_profile_snapshot", func(tx *gorm.DB) {
		if !strings.Contains(tx.Statement.SQL.String(), "LEFT JOIN") || !paused.CompareAndSwap(false, true) {
			return
		}
		// 已读到旧快照但尚未写 Redis，后台更新在这个窗口提交。
		close(loaded)
		select {
		case <-release:
		case <-tx.Statement.Context.Done():
			_ = tx.AddError(tx.Statement.Context.Err())
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Query().Remove("test:pause_profile_snapshot") })
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	var workers sync.WaitGroup
	var releaseOnce sync.Once
	defer func() {
		releaseOnce.Do(func() { close(release) })
		cancel()
		workers.Wait()
	}()
	loadDone := make(chan error, 1)
	workers.Go(func() {
		_, err := userlogic.NewUserLogic(ctx, service).GetUserProfile(userID)
		loadDone <- err
	})
	select {
	case <-loaded:
	case err := <-loadDone:
		t.Fatalf("profile load ended before barrier: %v", err)
	case <-ctx.Done():
		t.Fatal("profile query did not reach snapshot barrier")
	}
	if err := db.Table(model.TableNameUser).Where("id = ?", userID).Update("nickname", "after").Error; err != nil {
		t.Fatal(err)
	}

	// 观察同步实际发出抢锁命令，避免靠固定休眠猜测两个 goroutine 的执行顺序。
	attempt := &profileLockAttemptHook{key: lockKey, attempted: make(chan struct{})}
	client.AddHook(attempt)
	syncDone := make(chan *types.BizResult, 1)
	workers.Go(func() {
		syncDone <- NewAuthLogic(ctx, service).SyncUserRuntime(&types.UserRuntimeSyncReq{ID: userID, Profile: true})
	})
	select {
	case <-attempt.attempted:
	case result := <-syncDone:
		t.Fatalf("runtime sync returned before waiting for old profile: %+v", result)
	case <-ctx.Done():
		t.Fatal("runtime sync did not try to acquire the rebuild lock")
	}
	select {
	case result := <-syncDone:
		t.Fatalf("runtime sync returned while old profile was blocked: %+v", result)
	default:
	}
	releaseOnce.Do(func() { close(release) })
	if err := <-loadDone; err != nil {
		t.Fatalf("old profile load: %v", err)
	}
	result := <-syncDone
	if result.Code != codes.UpdateSuccess {
		t.Fatalf("runtime sync = %+v", result)
	}
	if count, err := client.Exists(ctx, profileKey, lockKey).Result(); err != nil || count != 0 {
		t.Fatalf("keys remaining after sync=%d, error=%v; want no profile or rebuild lock", count, err)
	}

	// 同步返回后的首次资料请求必须读取新提交值，不能命中旧回源写入。
	profile, err := userlogic.NewUserLogic(ctx, service).GetUserProfile(userID)
	if err != nil || profile == nil || profile.Nickname != "after" {
		t.Fatalf("profile after runtime sync=%+v, error=%v; want nickname=after", profile, err)
	}
}

// profileLockAttemptHook 只观察目标用户的第一次抢锁，用于确定回源与失效的测试时序。
type profileLockAttemptHook struct {
	key       string        // 本用例随机站点下的资料回源锁，不拦截其他 Redis 命令。
	attempted chan struct{} // 第一次抢锁命令完成后通知测试线程。
	once      sync.Once     // 重试和后续回源不能重复关闭通知。
}

// DialHook 保留真实 Redis 连接建立路径。
func (*profileLockAttemptHook) DialHook(next redis.DialHook) redis.DialHook { return next }

// ProcessHook 在实际抢锁返回后通知测试，不改写锁竞争结果。
func (h *profileLockAttemptHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		err := next(ctx, cmd)
		if cmd.Name() == "set" && len(cmd.Args()) > 1 && cmd.Args()[1] == h.key {
			h.once.Do(func() { close(h.attempted) })
		}
		return err
	}
}

// ProcessPipelineHook 保留业务批处理命令的原始结果。
func (*profileLockAttemptHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}
