package user

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"api/common/idgen"
	keys "api/common/rediskeys"
	"api/common/runtimecfg"
	"api/internal/config"
	redislock "api/internal/infra/redsync"
	"api/internal/model"
	"api/internal/svc"

	"github.com/Is999/go-utils/errors"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestGetUserByIDUsesIdentityRoute 确保用户 ID 通过身份目录定位物理表。
func TestGetUserByIDUsesIdentityRoute(t *testing.T) {
	// SQLite 只创建后半分片表，路由错误不能通过默认表读到同一用户。
	const routeShardCount = 2
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "user-fast-path.db")), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("gorm.Open(sqlite) error = %v", err)
	}
	userID := int64(1)
	for idgen.ShardNo(userID) < 512 {
		userID++
	}
	shardNo := idgen.ShardNo(userID)
	tableName := migrateUserProfileTableForTest(t, db, userID, routeShardCount)
	if err = db.Exec("CREATE TABLE user_identity_username (id INTEGER PRIMARY KEY, identity_value TEXT NOT NULL, user_id INTEGER NOT NULL, user_shard_no INTEGER NOT NULL)").Error; err != nil {
		t.Fatalf("create user identity table error = %v", err)
	}
	if err = db.Table(tableName).Create(&model.User{
		ID:           userID,
		ShardNo:      shardNo,
		Username:     "demo",
		PasswordHash: "hash",
		Status:       model.UserStatusEnabled,
		AuthVersion:  7,
	}).Error; err != nil {
		t.Fatalf("insert user error = %v", err)
	}
	if err = db.Exec("INSERT INTO user_identity_username (id, identity_value, user_id, user_shard_no) VALUES (?, ?, ?, ?)", 1, "demo", userID, shardNo).Error; err != nil {
		t.Fatalf("insert user identity error = %v", err)
	}

	// 只传用户 ID，通过身份目录联查真实物理表并恢复完整资料。
	split := NewUserLogic(context.Background(), svc.NewServiceContext(config.Config{
		User: config.UserConfig{RouteShardCount: routeShardCount},
	}, "v1", svc.Dependencies{}))
	splitUser, err := split.getUserByID(db, userID)
	if err != nil {
		t.Fatalf("split getUserByID() error = %v", err)
	}
	if splitUser == nil || splitUser.ID != userID || splitUser.Username != "demo" || splitUser.AuthVersion != 7 {
		t.Fatalf("split user = %+v, want id=%d", splitUser, userID)
	}

	// 无 Redis 的直接装配仍须完成资料回源，缓存写入入口自行跳过。
	uncached := NewUserLogic(t.Context(), svc.NewServiceContext(config.Config{
		User: config.UserConfig{RouteShardCount: routeShardCount},
	}, "v1", svc.Dependencies{SiteDBs: svc.SiteDatabases{MainDB: db}}))
	profile, err := uncached.GetUserProfile(userID)
	if err != nil || profile == nil || profile.ID != userID {
		t.Fatalf("uncached profile = %+v, error = %v", profile, err)
	}
	// 缺失和禁用不能因跳过缓存而被转换成成功响应。
	if _, err := uncached.GetUserProfile(userID + 1); !errors.Is(err, ErrUserNotFound) {
		t.Fatalf("missing uncached profile error = %v, want ErrUserNotFound", err)
	}
	if err := db.Table(tableName).Where("id = ?", userID).Update("status", model.UserStatusDisabled).Error; err != nil {
		t.Fatalf("disable user: %v", err)
	}
	if _, err := uncached.GetUserProfile(userID); !errors.Is(err, ErrUserDisabled) {
		t.Fatalf("disabled uncached profile error = %v, want ErrUserDisabled", err)
	}
}

// TestUserProfileRebuildWaitDelay 校验缓存观察间隔线性增长、封顶和抖动边界。
func TestUserProfileRebuildWaitDelay(t *testing.T) {
	// cases 覆盖每轮上下界以及非法参数归一化，防止缓存竞争路径退化为固定高频轮询。
	cases := []struct {
		name    string        // 当前边界场景
		attempt int           // 第几段等待，从 1 开始
		jitter  time.Duration // 测试注入的抖动
		want    time.Duration // 归一化后的最终等待
	}{
		{name: "first lower", attempt: 1, jitter: 0, want: 50 * time.Millisecond},
		{name: "first upper", attempt: 1, jitter: 50 * time.Millisecond, want: 100 * time.Millisecond},
		{name: "second lower", attempt: 2, jitter: 0, want: 100 * time.Millisecond},
		{name: "third upper", attempt: 3, jitter: 50 * time.Millisecond, want: 200 * time.Millisecond},
		{name: "fifth lower", attempt: 5, jitter: 0, want: 250 * time.Millisecond},
		{name: "base capped", attempt: 6, jitter: 0, want: 250 * time.Millisecond},
		{name: "jitter capped", attempt: 6, jitter: time.Second, want: 300 * time.Millisecond},
		{name: "invalid values", attempt: 0, jitter: -time.Second, want: 50 * time.Millisecond},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := userProfileRebuildWaitDelay(tt.attempt, tt.jitter); got != tt.want {
				t.Fatalf("userProfileRebuildWaitDelay(%d, %v) = %v, want %v", tt.attempt, tt.jitter, got, tt.want)
			}
		})
	}

	var minTotal time.Duration
	var maxTotal time.Duration
	for attempt := 1; attempt <= userProfileRebuildWaitAttempts; attempt++ {
		minTotal += userProfileRebuildWaitDelay(attempt, 0)
		maxTotal += userProfileRebuildWaitDelay(attempt, userProfileRebuildWaitJitter)
	}
	if minTotal != 750*time.Millisecond || maxTotal != time.Second {
		t.Fatalf("cache observation delay total = %v–%v, want 750ms–1s", minTotal, maxTotal)
	}
}

// TestGetUserProfileCollapsesConcurrentCacheMiss 验证并发缓存未命中只执行一次身份目录和物理表回源。
func TestGetUserProfileCollapsesConcurrentCacheMiss(t *testing.T) {
	// SQLite 与 miniredis 用于统计本进程并发回源，不证明 MySQL/Redis 集群表现。
	const (
		appID           = "profile-singleflight"
		concurrency     = 16
		routeShardCount = 2
	)
	previousRuntime := runtimecfg.Get()
	runtimecfg.Set(runtimecfg.Snapshot{AppID: appID})
	t.Cleanup(func() {
		runtimecfg.Restore(previousRuntime)
	})

	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "user-profile.db")), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("gorm.Open(sqlite) error = %v", err)
	}
	userID := int64(1)
	for idgen.ShardNo(userID) < 512 {
		userID++
	}
	shardNo := idgen.ShardNo(userID)
	tableName := migrateUserProfileTableForTest(t, db, userID, routeShardCount)
	if err = db.Exec("CREATE TABLE user_identity_username (id INTEGER PRIMARY KEY, identity_value TEXT NOT NULL, user_id INTEGER NOT NULL, user_shard_no INTEGER NOT NULL)").Error; err != nil {
		t.Fatalf("create user identity table error = %v", err)
	}
	if err = db.Table(tableName).Create(&model.User{
		ID:           userID,
		ShardNo:      shardNo,
		Username:     "demo",
		PasswordHash: "hash",
		Status:       model.UserStatusEnabled,
		AuthVersion:  7,
	}).Error; err != nil {
		t.Fatalf("insert user error = %v", err)
	}
	if err = db.Exec("INSERT INTO user_identity_username (id, identity_value, user_id, user_shard_no) VALUES (?, ?, ?, ?)", 1, "demo", userID, shardNo).Error; err != nil {
		t.Fatalf("insert user identity error = %v", err)
	}

	// 查询回调增加延迟，放大并发请求同时进入回源窗口的概率。
	var queryCount atomic.Int32
	if err = db.Callback().Query().Before("gorm:query").Register("test:count_profile_query", func(*gorm.DB) {
		queryCount.Add(1)
		time.Sleep(100 * time.Millisecond)
	}); err != nil {
		t.Fatalf("register query callback error = %v", err)
	}
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() {
		_ = client.Close()
	})
	service := svc.NewServiceContext(config.Config{
		AppID: appID,
		Auth:  config.AuthConfig{ProfileCacheTTLSeconds: 300},
		User:  config.UserConfig{RouteShardCount: routeShardCount},
	}, "v1", svc.Dependencies{
		SiteDBs: svc.SiteDatabases{MainDB: db},
		Rds:     client,
	})
	logic := NewUserLogic(context.Background(), service)
	cacheKey := logic.userProfileKey(userID)
	if err := client.Set(context.Background(), cacheKey, "{broken-profile-json", time.Minute).Err(); err != nil {
		t.Fatalf("seed malformed profile cache error = %v", err)
	}

	// 同一屏障释放全部请求，期望 singleflight 只执行一次数据库联查。
	start := make(chan struct{})
	errs := make(chan error, concurrency)
	var waitGroup sync.WaitGroup
	waitGroup.Add(concurrency)
	for range concurrency {
		go func() {
			defer waitGroup.Done()
			<-start
			profile, err := NewUserLogic(context.Background(), service).GetUserProfile(userID)
			if err == nil && (profile == nil || profile.ID != userID) {
				err = errors.Errorf("profile = %+v, want user_id=%d", profile, userID)
			}
			errs <- err
		}()
	}
	close(start)
	waitGroup.Wait()
	close(errs)

	for err := range errs {
		if err != nil {
			t.Fatalf("GetUserProfile() error = %v", err)
		}
	}
	if got := queryCount.Load(); got != 1 {
		t.Fatalf("profile query count = %d, want 1", got)
	}

	// 首轮回源完成后检查正值缓存 TTL 及删除能力。
	if ttl := server.TTL(cacheKey); ttl < 299*time.Second || ttl > 330*time.Second {
		t.Fatalf("profile cache TTL = %s, want about 300s with at most 10%% jitter", ttl)
	}
	if err := logic.DeleteUserProfileCache(userID); err != nil {
		t.Fatalf("DeleteUserProfileCache() error = %v", err)
	}
	lock := redislock.NewLock(client, logic.userProfileRebuildLockKey(userID))
	if err := lock.TryLock(context.Background(), userProfileRebuildLockTTL); err != nil {
		t.Fatalf("TryLock() error = %v", err)
	}
	lockHeld := true
	defer func() {
		if lockHeld {
			_ = lock.Unlock()
		}
	}()

	// 本测试持有另一把锁模拟竞争者，不启动第二个进程；等待方只能观察缓存写回。
	queryCountBeforeWait := queryCount.Load()
	commandsBeforeWait := server.CommandCount()
	waitErr := make(chan error, 1)
	go func() {
		profile, err := logic.loadUserProfile(cacheKey, userID)
		if err == nil && (profile == nil || profile.ID != userID) {
			err = errors.Errorf("profile = %+v, want user_id=%d", profile, userID)
		}
		waitErr <- err
	}()
	time.Sleep(450 * time.Millisecond)
	if err := logic.CacheUserProfile(userID, BuildUserProfile(&model.User{
		ID:       userID,
		ShardNo:  shardNo,
		Username: "demo",
		Status:   model.UserStatusEnabled,
	})); err != nil {
		t.Fatalf("CacheUserProfile() error = %v", err)
	}
	select {
	case err := <-waitErr:
		if err != nil {
			t.Fatalf("loadUserProfile() while lock is held error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("loadUserProfile() did not observe the cache rebuilt by another process")
	}
	if got := queryCount.Load(); got != queryCountBeforeWait {
		t.Fatalf("profile query count during distributed lock contention = %d, want %d", got, queryCountBeforeWait)
	}
	// miniredis 的单轮冷启动锁路径最多计 4 条命令，随后最多六次 GET，测试写回再占一次 SET；上限 12 可防止恢复双重轮询。
	if got := server.CommandCount() - commandsBeforeWait; got > 12 {
		t.Fatalf("Redis command count during distributed lock contention = %d, want <= 12", got)
	}
	if err := lock.Unlock(); err != nil {
		t.Fatalf("Unlock() error = %v", err)
	}
	lockHeld = false
}

// TestGetUserProfileCachesMissingUser 验证不存在用户使用短 TTL 空值缓存并可被正值覆盖。
func TestGetUserProfileCachesMissingUser(t *testing.T) {
	// 空库和查询计数器用于验证不存在用户只回源一次。
	const (
		appID           = "profile-empty-cache"
		routeShardCount = 2
		userID          = int64(42)
	)
	previousRuntime := runtimecfg.Get()
	runtimecfg.Set(runtimecfg.Snapshot{AppID: appID})
	t.Cleanup(func() {
		runtimecfg.Restore(previousRuntime)
	})

	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "missing-user-profile.db")), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("gorm.Open(sqlite) error = %v", err)
	}
	migrateUserProfileTableForTest(t, db, userID, routeShardCount)
	if err = db.Exec("CREATE TABLE user_identity_username (id INTEGER PRIMARY KEY, identity_value TEXT NOT NULL, user_id INTEGER NOT NULL, user_shard_no INTEGER NOT NULL)").Error; err != nil {
		t.Fatalf("create user identity table error = %v", err)
	}
	var queryCount atomic.Int32
	if err = db.Callback().Query().Before("gorm:query").Register("test:count_missing_profile_query", func(*gorm.DB) {
		queryCount.Add(1)
	}); err != nil {
		t.Fatalf("register query callback error = %v", err)
	}
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() {
		_ = client.Close()
	})
	service := svc.NewServiceContext(config.Config{
		AppID: appID,
		Auth:  config.AuthConfig{ProfileCacheTTLSeconds: 300},
		User:  config.UserConfig{RouteShardCount: routeShardCount},
	}, "v1", svc.Dependencies{
		SiteDBs: svc.SiteDatabases{MainDB: db},
		Rds:     client,
	})
	logic := NewUserLogic(context.Background(), service)

	// 第二次读取应命中短 TTL 空标记，不再访问数据库。
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := logic.GetUserProfile(userID); !errors.Is(err, ErrUserNotFound) {
			t.Fatalf("GetUserProfile() attempt %d error = %v, want ErrUserNotFound", attempt+1, err)
		}
	}
	if got := queryCount.Load(); got != 1 {
		t.Fatalf("missing profile query count = %d, want 1", got)
	}
	cacheKey := logic.userProfileKey(userID)
	if value, err := client.Get(context.Background(), cacheKey).Result(); err != nil || value != keys.EmptyValueMarker {
		t.Fatalf("missing profile cache = %q, %v; want empty marker", value, err)
	}
	if ttl := server.TTL(cacheKey); ttl < 119*time.Second || ttl > 132*time.Second {
		t.Fatalf("missing profile cache TTL = %s, want about 120s with at most 10%% jitter", ttl)
	}

	// 后续正值写入必须覆盖空标记，并直接返回新用户资料。
	if err := logic.CacheUserProfile(userID, BuildUserProfile(&model.User{
		ID:       userID,
		ShardNo:  idgen.ShardNo(userID),
		Username: "created-user",
		Status:   model.UserStatusEnabled,
	})); err != nil {
		t.Fatalf("CacheUserProfile() error = %v", err)
	}
	profile, err := logic.GetUserProfile(userID)
	if err != nil || profile == nil || profile.Username != "created-user" {
		t.Fatalf("GetUserProfile() after positive cache = %+v, %v", profile, err)
	}
	if got := queryCount.Load(); got != 1 {
		t.Fatalf("profile query count after positive overwrite = %d, want 1", got)
	}
}

// migrateUserProfileTableForTest 按生产路由创建完整 SQLite 用户表，避免字段裁剪掩盖联查契约变化。
func migrateUserProfileTableForTest(t *testing.T, db *gorm.DB, userID int64, routeShardCount int) string {
	t.Helper()
	tableName, err := model.UserPhysicalTableName(idgen.ShardNo(userID), routeShardCount)
	if err != nil {
		t.Fatalf("UserPhysicalTableName() error = %v", err)
	}
	if err = db.Table(tableName).AutoMigrate(&model.User{}); err != nil {
		t.Fatalf("AutoMigrate(%s) error = %v", tableName, err)
	}
	return tableName
}

// TestUserProfileCacheRejectsMismatchedID 防止 Redis 损坏或误写导致跨用户资料返回。
func TestUserProfileCacheRejectsMismatchedID(t *testing.T) {
	const (
		appID  = "profile-id-invariant"
		userID = int64(42)
	)
	previousRuntime := runtimecfg.Get()
	runtimecfg.Set(runtimecfg.Snapshot{AppID: appID})
	t.Cleanup(func() { runtimecfg.Restore(previousRuntime) })
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	logicObj := NewUserLogic(context.Background(), svc.NewServiceContext(config.Config{
		AppID: appID,
		Auth:  config.AuthConfig{ProfileCacheTTLSeconds: 300},
	}, "v1", svc.Dependencies{Rds: client}))
	cacheKey := logicObj.userProfileKey(userID)
	if err := client.Set(context.Background(), cacheKey, `{"id":"43","username":"other"}`, time.Minute).Err(); err != nil {
		t.Fatalf("seed mismatched profile cache error = %v", err)
	}
	profile, found, err := logicObj.cachedUserProfile(cacheKey, userID)
	if err != nil || found || profile != nil {
		t.Fatalf("cachedUserProfile() = %+v, %t, %v; want deleted cache miss", profile, found, err)
	}
	if server.Exists(cacheKey) {
		t.Fatal("mismatched profile cache should be deleted")
	}
	if err := logicObj.CacheUserProfile(userID, BuildUserProfile(&model.User{ID: 43})); err == nil {
		t.Fatal("CacheUserProfile() expected mismatched ID error")
	}
}

// TestDeleteUserProfileCachePreservesCacheWhenLockFails 验证未取得回源锁时不能删除资料或改写其他持有者。
func TestDeleteUserProfileCachePreservesCacheWhenLockFails(t *testing.T) {
	cases := []struct {
		name       string        // 普通竞争、请求取消和依赖故障分别断言，避免混淆失败原因。
		lockHeld   bool          // 使用固定 owner 模拟另一实例正在回源。
		canceled   bool          // 调用前已取消的请求不得继续执行缓存操作。
		timeout    time.Duration // 等待过程中取消使用短 deadline；零值沿用锁的三秒上限。
		redisError string        // 注入 Redis 命令故障，不作为本机环境缺失处理。
		want       error         // 调用方必须可识别的取消、竞争或依赖错误。
	}{
		{name: "already canceled", canceled: true, want: context.Canceled},
		{name: "canceled while waiting", lockHeld: true, timeout: 50 * time.Millisecond, want: context.DeadlineExceeded},
		{name: "contention exhausted", lockHeld: true, want: redislock.ErrLockTaken},
		{name: "redis unavailable", redisError: "ERR injected Redis failure", want: redislock.ErrLockUnavailable},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			previousRuntime := runtimecfg.Get()
			runtimecfg.Set(runtimecfg.Snapshot{AppID: "profile-delete-failure"})
			t.Cleanup(func() { runtimecfg.Restore(previousRuntime) })
			server := miniredis.RunT(t)
			client := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1})
			t.Cleanup(func() { _ = client.Close() })
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if tt.canceled {
				cancel()
			}
			if tt.timeout > 0 {
				var stop context.CancelFunc
				ctx, stop = context.WithTimeout(ctx, tt.timeout)
				defer stop()
			}
			logic := NewUserLogic(ctx, svc.NewServiceContext(config.Config{
				AppID: "profile-delete-failure",
			}, "test", svc.Dependencies{Rds: client}))
			profileKey := logic.userProfileKey(42)
			lockKey := logic.userProfileRebuildLockKey(42)
			if err := server.Set(profileKey, "existing-profile"); err != nil {
				t.Fatal(err)
			}
			if tt.lockHeld {
				if err := server.Set(lockKey, "other-owner"); err != nil {
					t.Fatal(err)
				}
				server.SetTTL(lockKey, userProfileRebuildLockTTL)
			}
			server.SetError(tt.redisError)
			started := time.Now()
			err := logic.DeleteUserProfileCache(42)
			if !errors.Is(err, tt.want) {
				t.Fatalf("DeleteUserProfileCache() error=%v, want %v", err, tt.want)
			}
			// 三秒获取上限之外只留测试调度余量，防止失败路径出现无限等待。
			if elapsed := time.Since(started); elapsed > 4*time.Second {
				t.Fatalf("cache invalidation waited %s, want bounded lock acquisition", elapsed)
			}
			if value, err := server.Get(profileKey); err != nil || value != "existing-profile" {
				t.Fatalf("cache after failed lock=%q, error=%v", value, err)
			}
			if tt.lockHeld {
				if owner, err := server.Get(lockKey); err != nil || owner != "other-owner" {
					t.Fatalf("other lock owner=%q, error=%v", owner, err)
				}
			} else if server.Exists(lockKey) {
				t.Fatal("failed acquisition left a rebuild lock")
			}
		})
	}
}
