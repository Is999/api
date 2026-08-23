package auth

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	codes "api/common/codes"
	"api/common/idgen"
	keys "api/common/rediskeys"
	"api/internal/config"
	"api/internal/infra/collectorx"
	mysqlx "api/internal/infra/mysql"
	userlogic "api/internal/logic/user"
	"api/internal/model"
	"api/internal/requestctx"
	"api/internal/routealias"
	"api/internal/svc"
	"api/internal/types"

	"github.com/alicebob/miniredis/v2"
	drivermysql "github.com/go-sql-driver/mysql"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// TestRegistrationIdentityConflictsReal 验证真实唯一约束与并发注册失败补偿，不预查邮箱或手机。
func TestRegistrationIdentityConflictsReal(t *testing.T) {
	dsn, redisAddr := os.Getenv("AUTH_INTEGRATION_MYSQL_DSN"), os.Getenv("AUTH_INTEGRATION_REDIS_ADDR")
	if dsn == "" || redisAddr == "" {
		t.Skip("需要显式配置已初始化的隔离 MySQL 和 Redis")
	}
	parsed, err := drivermysql.ParseDSN(dsn)
	if err != nil || !strings.HasPrefix(parsed.DBName, "accept_api_") {
		t.Fatal("真实注册回归仅允许 accept_api_ 前缀的隔离数据库")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	db, err := mysqlx.New(ctx, config.MySQLConfig{WriteDataSource: dsn, MaxOpenConns: 4, MaxIdleConns: 2, ConnMaxLifetime: 60}, config.ObservabilityConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mysqlx.Close(db) })
	client := redis.NewClient(&redis.Options{Addr: redisAddr, DialTimeout: time.Second, ReadTimeout: time.Second, WriteTimeout: time.Second})
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Ping(ctx).Err(); err != nil {
		t.Fatal(err)
	}
	// 1023 不与本轮候选进程的 0-9 用户节点池重叠；每轮标识与缓存前缀均独立。
	if err := idgen.ConfigureWorkerID(1023); err != nil {
		t.Fatal(err)
	}
	suffix := strings.ToLower(rand.Text()[:12])
	cfg := config.Config{AppID: "auth-conflict-" + suffix, AppKey: "auth-conflict-test-app-key", JwtSecret: "auth-conflict-test-jwt-secret", JwtExpiresIn: 300,
		User: config.UserConfig{RouteShardCount: 1}, Auth: config.AuthConfig{RegisterEnabled: true, SessionTTLSeconds: 300, PasswordMinLength: 8}}
	svcCtx := svc.NewServiceContext(cfg, "auth-conflict", svc.Dependencies{SiteDBs: svc.SiteDatabases{MainDB: db}, Rds: client})
	username, email := "conflict_"+suffix, suffix+"@example.com"
	phone := fmt.Sprintf("138%08d", time.Now().UnixNano()%100000000)
	registered := requireAuthTokenResp(t, NewAuthLogic(ctx, svcCtx).Register(&types.RegisterReq{Username: username, Password: "P@ssw0rd!", Email: email, Phone: phone}), codes.CreateSuccess)
	// 五张表的行数锁定回滚终态，测试不创建/删除表或清理其它注册数据。
	tables := []string{model.TableNameUser, model.TableNameUserIdentityUsername, model.TableNameUserIdentityEmail, model.TableNameUserIdentityPhone, model.TableNameUserIdentityOAuth}
	baseline := make(map[string]int64, len(tables))
	for _, table := range tables {
		var count int64
		if err := db.WithContext(ctx).Table(table).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		baseline[table] = count
	}
	var capturedMu sync.Mutex
	var capturedIDs []int64
	raceName := "race_" + suffix
	ready, release := make(chan struct{}, 2), make(chan struct{})
	// 在真实 INSERT 前等待两请求，确保二者都越过了用户名快速预查。
	const callback = "test:registration_conflict_barrier"
	if err := db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		identity, ok := tx.Statement.Dest.(*model.UserIdentity)
		if !ok || identity.IdentityType != model.UserIdentityTypeUsername {
			return
		}
		capturedMu.Lock()
		capturedIDs = append(capturedIDs, identity.UserID)
		capturedMu.Unlock()
		if identity.IdentityValue == raceName {
			ready <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
				tx.AddError(ctx.Err())
			}
		}
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Callback().Create().Remove(callback) })
	for _, req := range []types.RegisterReq{
		{Username: username, Password: "P@ssw0rd!"},
		{Username: "email_" + suffix, Password: "P@ssw0rd!", Email: email},
		{Username: "phone_" + suffix, Password: "P@ssw0rd!", Phone: phone},
	} {
		result := NewAuthLogic(ctx, svcCtx).Register(&req)
		if result == nil || result.Code != codes.UserAlreadyExists {
			t.Fatalf("Register(%s)=%+v，期望身份占用", req.Username, result)
		}
		if result.ResolveMessage("zh-CN") != "账号标识已被占用" || result.ResolveMessage("en-US") != "Account identifier is already in use" {
			t.Errorf("注册冲突文案未保持统一: zh=%q en=%q", result.ResolveMessage("zh-CN"), result.ResolveMessage("en-US"))
		}
	}
	results := make(chan *types.BizResult, 2)
	for range 2 {
		go func() {
			results <- NewAuthLogic(ctx, svcCtx).Register(&types.RegisterReq{Username: raceName, Password: "P@ssw0rd!"})
		}()
	}
	for range 2 {
		select {
		case <-ready:
		case <-ctx.Done():
			t.Fatal("两个注册请求未同时到达真实唯一约束前")
		}
	}
	close(release)
	winnerID, conflicts := int64(0), 0
	for range 2 {
		select {
		case result := <-results:
			if result != nil && result.Code == codes.CreateSuccess {
				winnerID = requireAuthTokenResp(t, result, codes.CreateSuccess).User.ID
			} else if result != nil && result.Code == codes.UserAlreadyExists {
				conflicts++
				if result.ResolveMessage("zh-CN") != "账号标识已被占用" {
					t.Errorf("并发冲突文案=%q", result.ResolveMessage("zh-CN"))
				}
			} else {
				t.Fatalf("并发注册结果=%+v", result)
			}
		case <-ctx.Done():
			t.Fatal("真实唯一约束决胜未按期结束")
		}
	}
	if winnerID <= 0 || conflicts != 1 {
		t.Fatalf("并发注册 winner=%d conflicts=%d", winnerID, conflicts)
	}
	for _, table := range tables {
		want := baseline[table]
		if table == model.TableNameUser || table == model.TableNameUserIdentityUsername {
			want++
		}
		var count int64
		if err := db.WithContext(ctx).Table(table).Count(&count).Error; err != nil || count != want {
			t.Errorf("注册回滚后 %s 行数=%d，期望=%d，err=%v", table, count, want, err)
		}
	}
	// 失败请求的 Hash、索引和认证版本必须全部撤销，不能影响成功用户会话。
	capturedMu.Lock()
	ids := append([]int64(nil), capturedIDs...)
	capturedMu.Unlock()
	if len(ids) != 4 {
		t.Fatalf("预创建会话对应的写入请求数=%d，期望邮箱/手机/并发请求共4个", len(ids))
	}
	for _, id := range ids {
		if id == winnerID {
			continue
		}
		if count, err := client.Exists(ctx, keys.UserSessionKeys(id)...).Result(); err != nil || count != 0 {
			t.Errorf("失败注册残留会话 user=%d count=%d err=%v", id, count, err)
		}
	}
	requireSessionToken(t, svcCtx, client, registered.User.ID, registered.Token)
}

// TestAuthMainFlowIntegration 用 SQLite、miniredis 和内存事件采集器覆盖认证逻辑，不经过 HTTP 中间件。
func TestAuthMainFlowIntegration(t *testing.T) {
	// 注册阶段验证用户落库、会话创建和资料缓存延迟构建。
	svcCtx, rds, seen := newAuthFlowTestService(t)

	registerCtx := authFlowContext(AuthEventActionRegisterSuccess, string(routealias.AuthRegister), http.MethodPost, "/api/auth/register", "10.0.0.8")
	registerResp := requireAuthTokenResp(t, NewAuthLogic(registerCtx, svcCtx).Register(&types.RegisterReq{
		Username: "demo_user",
		Password: "P@ssw0rd!",
		Nickname: "Demo",
		Email:    "demo@example.com",
		Phone:    "13800000000",
	}), codes.CreateSuccess)
	if registerResp.User == nil || registerResp.User.ID <= 0 || registerResp.User.Email != "de***o@example.com" || registerResp.User.Phone != "138****0000" {
		t.Fatalf("register user = %+v, want created profile", registerResp.User)
	}
	registerSID := requireSessionToken(t, svcCtx, rds, registerResp.User.ID, registerResp.Token)
	requireSessionIndexMembers(t, rds, registerResp.User.ID, []string{registerSID})
	profileKey := keys.WithPrefix(fmt.Sprintf(keys.UserProfile, registerResp.User.ID))
	if exists := rds.Exists(t.Context(), profileKey).Val(); exists != 0 {
		t.Fatalf("profile cache exists after register = %d, want lazy rebuild", exists)
	}

	// 登录阶段验证新会话、最后登录信息和身份索引一致性。
	loginCtx := authFlowContext(AuthEventActionLoginSuccess, string(routealias.AuthLogin), http.MethodPost, "/api/auth/login", "10.0.0.9")
	loginResp := requireAuthTokenResp(t, NewAuthLogic(loginCtx, svcCtx).Login(&types.LoginReq{
		IdentityType:  types.LoginIdentityTypeEmail,
		IdentityValue: "demo@example.com",
		Password:      "P@ssw0rd!",
	}), codes.Success)
	if loginResp.Token == registerResp.Token {
		t.Fatal("login token should differ from register token")
	}
	loginSID := requireSessionToken(t, svcCtx, rds, loginResp.User.ID, loginResp.Token)
	requireSessionIndexMembers(t, rds, loginResp.User.ID, []string{registerSID, loginSID})

	cfg := svcCtx.CurrentConfig()
	user, err := model.FindUserByIdentity(svcCtx.WriteDB(svc.DatabaseMain), model.UserIdentityTypeUsername, model.UserIdentityProviderLocal, "demo_user", cfg.AppKey, cfg.User.RouteShardCount)
	if err != nil {
		t.Fatalf("FindUserByIdentity(username) error = %v", err)
	}
	if user == nil || user.LastLoginIP != "10.0.0.9" {
		t.Fatalf("user after login = %+v, want last_login_ip 10.0.0.9", user)
	}
	identity, err := model.FindUserIdentity(svcCtx.WriteDB(svc.DatabaseMain), model.UserIdentityTypePhone, model.UserIdentityProviderLocal, "13800000000", svcCtx.CurrentConfig().AppKey)
	if err != nil {
		t.Fatalf("FindUserIdentity(phone) error = %v", err)
	}
	if identity == nil || identity.UserID != user.ID || identity.UserShardNo != user.ShardNo {
		t.Fatalf("user identity = %+v, want user_id=%d user_shard_no=%d", identity, user.ID, user.ShardNo)
	}
	if err := model.UpdateUserProfileWithIdentities(svcCtx.WriteDB(svc.DatabaseMain), user.ID, map[string]any{"email": "changed@example.com"}, cfg.AppKey, cfg.User.RouteShardCount); err != nil {
		t.Fatalf("UpdateUserProfileWithIdentities(email) error = %v", err)
	}

	// 资料读取必须命中刚提交的主库值，不能回写登录前缓存。
	profileCtx := authFlowAuthenticatedContext(string(routealias.UserProfile), http.MethodGet, "/api/user/profile", "10.0.0.9", loginResp)
	profile := requireUserProfile(t, userlogic.NewUserLogic(profileCtx, svcCtx).Profile(), codes.FetchSuccess)
	if profile.Email != "ch****d@example.com" {
		t.Fatalf("profile email = %q, want database value ch****d@example.com", profile.Email)
	}

	// 刷新复用鉴权快照且不重复查库，只轮换同一 sid 下的 token。
	refreshCtx := authFlowAuthenticatedContext(string(routealias.AuthRefresh), http.MethodPost, "/api/auth/refresh", "10.0.0.9", loginResp)
	refreshUser, err := userlogic.NewUserLogic(refreshCtx, svcCtx).GetActiveUserForAuth(user.ID)
	if err != nil {
		t.Fatalf("GetActiveUserForAuth(refresh) error = %v", err)
	}
	setAuthFlowUserSnapshot(refreshCtx, refreshUser)
	db := svcCtx.WriteDB(svc.DatabaseMain)
	const refreshQueryCallback = "test:count_auth_refresh_query"
	refreshQueryCount := 0
	if err = db.Callback().Query().Before("gorm:query").Register(refreshQueryCallback, func(*gorm.DB) {
		refreshQueryCount++
	}); err != nil {
		t.Fatalf("register refresh query callback: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Callback().Query().Remove(refreshQueryCallback)
	})
	refreshResp := requireAuthTokenResp(t, NewAuthLogic(refreshCtx, svcCtx).Refresh(), codes.Success)
	if refreshQueryCount != 0 {
		t.Fatalf("Refresh() database queries = %d, want 0", refreshQueryCount)
	}
	if refreshResp.Token == loginResp.Token {
		t.Fatal("refresh token should differ from login token")
	}
	if refreshResp.User == nil || refreshResp.User.Email != "ch****d@example.com" {
		t.Fatalf("refresh user = %+v, want latest primary DB profile", refreshResp.User)
	}
	refreshSID := requireSessionToken(t, svcCtx, rds, refreshResp.User.ID, refreshResp.Token)
	_, loginJTI := tokenSessionClaimsForTest(loginResp.Token, svcCtx.CurrentConfig().JwtSecret)
	_, refreshJTI := tokenSessionClaimsForTest(refreshResp.Token, svcCtx.CurrentConfig().JwtSecret)
	if refreshSID != loginSID || refreshJTI == "" || refreshJTI == loginJTI {
		t.Fatalf("refresh claims sid=%q jti=%q, want sid=%q and a new jti", refreshSID, refreshJTI, loginSID)
	}
	requireSessionTokenNotCurrent(t, rds, refreshResp.User.ID, loginSID, loginResp.Token)
	requireSessionIndexMembers(t, rds, refreshResp.User.ID, []string{registerSID, refreshSID})

	// 退出只删除当前 sid，并最终核对整条主链路的风控事件。
	logoutCtx := authFlowAuthenticatedContext(string(routealias.AuthLogout), http.MethodPost, "/api/auth/logout", "10.0.0.9", refreshResp)
	logoutResult := NewAuthLogic(logoutCtx, svcCtx).Logout()
	if logoutResult == nil || !logoutResult.IsSuccess() || logoutResult.Code != codes.Success {
		t.Fatalf("Logout() = %+v, want success", logoutResult)
	}
	requireSessionMissing(t, rds, refreshResp.User.ID, refreshSID)
	requireSessionIndexMembers(t, rds, refreshResp.User.ID, []string{registerSID})

	requireAuthFlowEvents(t, *seen, []authFlowEventWant{
		{action: AuthEventActionRegisterSuccess, reason: AuthEventReasonSessionCreated, route: string(routealias.AuthRegister)},
		{action: AuthEventActionLoginSuccess, reason: AuthEventReasonSessionCreated, route: string(routealias.AuthLogin)},
		{action: AuthEventActionRefreshSuccess, reason: AuthEventReasonSessionRotated, route: string(routealias.AuthRefresh)},
		{action: AuthEventActionLogoutSuccess, reason: AuthEventReasonCurrentSessionDeleted, route: string(routealias.AuthLogout)},
	})
}

// TestRegisterSkipsDatabaseWhenSessionCreationFails 确保 Redis 会话失败时不进入用户数据库写入。
func TestRegisterSkipsDatabaseWhenSessionCreationFails(t *testing.T) {
	svcCtx, _, _ := newAuthFlowTestService(t)
	svcCtx.Rds = nil
	result := NewAuthLogic(authFlowContext(AuthEventActionRegisterSuccess, string(routealias.AuthRegister), http.MethodPost, "/api/auth/register", "10.0.0.8"), svcCtx).Register(&types.RegisterReq{
		Username: "rollback_user",
		Password: "P@ssw0rd!",
	})
	if result == nil || result.Code != codes.RedisUnavailable {
		t.Fatalf("Register() = %+v, want Redis unavailable", result)
	}
	identity, err := model.FindUserIdentity(svcCtx.WriteDB(svc.DatabaseMain), model.UserIdentityTypeUsername, model.UserIdentityProviderLocal, "rollback_user", svcCtx.CurrentConfig().AppKey)
	if err != nil {
		t.Fatalf("FindUserIdentity() error = %v", err)
	}
	if identity != nil {
		t.Fatalf("identity = %+v, want no database write", identity)
	}
}

// TestRegisterCreatesSessionBeforeDatabaseWrite 确保 Redis 等待不进入数据库事务，且写库失败会撤销预创建状态。
func TestRegisterCreatesSessionBeforeDatabaseWrite(t *testing.T) {
	// GORM 回调在身份写入点注入失败，并观察事务外会话是否已创建。
	svcCtx, rds, _ := newAuthFlowTestService(t)
	db := svcCtx.WriteDB(svc.DatabaseMain)
	const callbackName = "test:reject_register_identity_create"
	var userID int64
	var sessionObserved bool
	if err := db.Callback().Create().Before("gorm:create").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table != model.TableNameUserIdentityUsername {
			return
		}
		identity, ok := tx.Statement.Dest.(*model.UserIdentity)
		if !ok || identity.UserID <= 0 {
			tx.AddError(fmt.Errorf("missing username identity in create callback"))
			return
		}
		userID = identity.UserID
		sessionObserved = rds.HLen(t.Context(), keys.UserSessionHashKey(userID)).Val() == 1
		tx.AddError(fmt.Errorf("injected register database failure"))
	}); err != nil {
		t.Fatalf("register create callback: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Callback().Create().Remove(callbackName)
	})

	// 数据库失败后注册接口必须回滚预创建会话和相关缓存状态。
	result := NewAuthLogic(t.Context(), svcCtx).Register(&types.RegisterReq{
		Username: "database_failure_register_user",
		Password: "P@ssw0rd!",
	})
	if result == nil || result.Code != codes.MySQLUnavailable {
		t.Fatalf("Register() = %+v, want MySQL unavailable", result)
	}
	if userID <= 0 || !sessionObserved {
		t.Fatalf("database callback user_id=%d session_observed=%t, want pre-created session", userID, sessionObserved)
	}
	requireNoSessionState(t, rds, userID)
	if exists := rds.Exists(t.Context(), keys.UserSessionKeys(userID)...).Val(); exists != 0 {
		t.Fatalf("registration session keys exist = %d, want 0", exists)
	}
	profileKey := keys.WithPrefix(fmt.Sprintf(keys.UserProfile, userID))
	if exists := rds.Exists(t.Context(), profileKey).Val(); exists != 0 {
		t.Fatalf("profile cache exists = %d, want 0", exists)
	}
}

// TestLoginInvalidatesProfileCache 防止登录前快照覆盖并发提交后的用户资料。
func TestLoginInvalidatesProfileCache(t *testing.T) {
	// 先注册并主动写入旧资料缓存，再执行一次成功登录。
	svcCtx, rds, _ := newAuthFlowTestService(t)
	registered := requireAuthTokenResp(t, NewAuthLogic(authFlowContext(
		AuthEventActionRegisterSuccess,
		string(routealias.AuthRegister),
		http.MethodPost,
		"/api/auth/register",
		"10.0.0.8",
	), svcCtx).Register(&types.RegisterReq{
		Username: "profile_cache_login_user",
		Password: "P@ssw0rd!",
		Nickname: "before",
	}), codes.CreateSuccess)
	if err := userlogic.NewUserLogic(t.Context(), svcCtx).CacheUserProfile(registered.User.ID, registered.User); err != nil {
		t.Fatalf("CacheUserProfile() error = %v", err)
	}

	// 登录提交最后登录信息后必须删除旧缓存，交由后续读取重建。
	result := NewAuthLogic(authFlowContext(
		AuthEventActionLoginSuccess,
		string(routealias.AuthLogin),
		http.MethodPost,
		"/api/auth/login",
		"10.0.0.9",
	), svcCtx).Login(&types.LoginReq{
		IdentityType:  types.LoginIdentityTypeUsername,
		IdentityValue: "profile_cache_login_user",
		Password:      "P@ssw0rd!",
	})
	if result == nil || result.Code != codes.Success || result.IsFailure() {
		t.Fatalf("Login() = %+v, want success", result)
	}
	profileKey := keys.WithPrefix(fmt.Sprintf(keys.UserProfile, registered.User.ID))
	if exists := rds.Exists(t.Context(), profileKey).Val(); exists != 0 {
		t.Fatalf("profile cache exists = %d, want invalidated", exists)
	}
}

// TestRegisterPasswordMinimumCountsCharacters 验证最小长度按 Unicode 字符数而非 UTF-8 字节数计算。
func TestRegisterPasswordMinimumCountsCharacters(t *testing.T) {
	svcCtx, _, _ := newAuthFlowTestService(t)
	short := NewAuthLogic(context.Background(), svcCtx).Register(&types.RegisterReq{
		Username: "short_password_user",
		Password: "密码密",
	})
	if short == nil || short.Code != codes.ParamError {
		t.Fatalf("Register(short multibyte password) = %+v, want param error", short)
	}
	accepted := NewAuthLogic(context.Background(), svcCtx).Register(&types.RegisterReq{
		Username: "valid_password_user",
		Password: "密码密码密码密码",
	})
	if accepted == nil || accepted.Code != codes.CreateSuccess || accepted.IsFailure() {
		t.Fatalf("Register(8-character multibyte password) = %+v, want success", accepted)
	}
}

// TestRefreshUsesAuthUserSnapshotWhenDatabaseUnavailable 验证中间件完成主库鉴权后刷新不再依赖数据库。
func TestRefreshUsesAuthUserSnapshotWhenDatabaseUnavailable(t *testing.T) {
	svcCtx, _, _ := newAuthFlowTestService(t)
	registerResp := requireAuthTokenResp(t, NewAuthLogic(context.Background(), svcCtx).Register(&types.RegisterReq{
		Username: "refresh_database_failure_user",
		Password: "P@ssw0rd!",
	}), codes.CreateSuccess)
	refreshCtx := authFlowAuthenticatedContext(string(routealias.AuthRefresh), http.MethodPost, "/api/auth/refresh", "10.0.0.9", registerResp)
	user, err := userlogic.NewUserLogic(refreshCtx, svcCtx).GetActiveUserForAuth(registerResp.User.ID)
	if err != nil {
		t.Fatalf("GetActiveUserForAuth() error = %v", err)
	}
	setAuthFlowUserSnapshot(refreshCtx, user)
	sqlDB, err := svcCtx.WriteDB(svc.DatabaseMain).DB()
	if err != nil {
		t.Fatalf("DB() error = %v", err)
	}
	if err = sqlDB.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	refreshResp := requireAuthTokenResp(t, NewAuthLogic(refreshCtx, svcCtx).Refresh(), codes.Success)
	oldSID, oldJTI := tokenSessionClaimsForTest(registerResp.Token, svcCtx.CurrentConfig().JwtSecret)
	newSID, newJTI := tokenSessionClaimsForTest(refreshResp.Token, svcCtx.CurrentConfig().JwtSecret)
	if newSID != oldSID || newJTI == "" || newJTI == oldJTI {
		t.Fatalf("refresh claims sid=%q jti=%q, want sid=%q and a new jti", newSID, newJTI, oldSID)
	}
}

// TestRefreshRejectsMissingAuthUserSnapshot 验证绕过鉴权中间件的直接调用按未登录边界失败。
func TestRefreshRejectsMissingAuthUserSnapshot(t *testing.T) {
	ctx, _ := requestctx.New(t.Context())
	requestctx.SetUser(ctx, 42, "demo", "10.0.0.9")
	result := NewAuthLogic(ctx, nil).Refresh()
	if result == nil || result.Code != codes.Unauthorized || !result.IsFailure() {
		t.Fatalf("Refresh() = %+v, want unauthorized", result)
	}
}

// TestLoginKeepsDatabaseStateWhenSessionCreationFails 确保 Redis 不可用时不会把未完成的登录写成最后登录记录。
func TestLoginKeepsDatabaseStateWhenSessionCreationFails(t *testing.T) {
	// 注册成功后保存主库最后登录快照，作为失败前基线。
	svcCtx, _, _ := newAuthFlowTestService(t)
	registerResp := requireAuthTokenResp(t, NewAuthLogic(authFlowContext(
		AuthEventActionRegisterSuccess,
		string(routealias.AuthRegister),
		http.MethodPost,
		"/api/auth/register",
		"10.0.0.8",
	), svcCtx).Register(&types.RegisterReq{
		Username: "session_failure_user",
		Password: "P@ssw0rd!",
	}), codes.CreateSuccess)
	cfg := svcCtx.CurrentConfig()
	before, err := model.FindUserByIdentity(
		svcCtx.WriteDB(svc.DatabaseMain),
		model.UserIdentityTypeUsername,
		model.UserIdentityProviderLocal,
		"session_failure_user",
		cfg.AppKey,
		cfg.User.RouteShardCount,
	)
	if err != nil || before == nil {
		t.Fatalf("FindUserByIdentity(before) user=%+v error=%v", before, err)
	}
	if before.ID != registerResp.User.ID {
		t.Fatalf("registered user id=%d, database user id=%d", registerResp.User.ID, before.ID)
	}

	// 移除 Redis 注入会话创建失败，数据库最后登录字段不得变化。
	svcCtx.Rds = nil
	result := NewAuthLogic(authFlowContext(
		AuthEventActionLoginSuccess,
		string(routealias.AuthLogin),
		http.MethodPost,
		"/api/auth/login",
		"10.0.0.9",
	), svcCtx).Login(&types.LoginReq{
		IdentityType:  types.LoginIdentityTypeUsername,
		IdentityValue: "session_failure_user",
		Password:      "P@ssw0rd!",
	})
	if result == nil || result.Code != codes.RedisUnavailable {
		t.Fatalf("Login() = %+v, want Redis unavailable", result)
	}
	after, err := model.FindUserByIdentity(
		svcCtx.WriteDB(svc.DatabaseMain),
		model.UserIdentityTypeUsername,
		model.UserIdentityProviderLocal,
		"session_failure_user",
		cfg.AppKey,
		cfg.User.RouteShardCount,
	)
	if err != nil || after == nil {
		t.Fatalf("FindUserByIdentity(after) user=%+v error=%v", after, err)
	}
	if after.LastLoginIP != before.LastLoginIP || !after.LastLoginAt.Equal(before.LastLoginAt) {
		t.Fatalf("last login changed after session failure: before=(%s,%s) after=(%s,%s)",
			before.LastLoginIP, before.LastLoginAt, after.LastLoginIP, after.LastLoginAt)
	}
}

// TestLoginHidesDisabledAccountState 验证正确密码也不能通过登录响应枚举账号禁用状态。
func TestLoginHidesDisabledAccountState(t *testing.T) {
	svcCtx, _, _ := newAuthFlowTestService(t)
	registered := requireAuthTokenResp(t, NewAuthLogic(context.Background(), svcCtx).Register(&types.RegisterReq{
		Username: "disabled_login_user",
		Password: "P@ssw0rd!",
	}), codes.CreateSuccess)
	// 测试直接模拟后台敏感状态事务已提交；生产通用 UpdateUser 会拒绝直接修改 status。
	if err := svcCtx.WriteDB(svc.DatabaseMain).Table(model.TableNameUser).Where("id = ?", registered.User.ID).Updates(map[string]any{
		"status":     model.UserStatusDisabled,
		"updated_at": time.Now(),
	}).Error; err != nil {
		t.Fatalf("禁用测试用户失败: %v", err)
	}

	result := NewAuthLogic(context.Background(), svcCtx).Login(&types.LoginReq{
		IdentityType:  types.LoginIdentityTypeUsername,
		IdentityValue: "disabled_login_user",
		Password:      "P@ssw0rd!",
	})
	if result == nil || result.Code != codes.InvalidPassword || !result.IsFailure() {
		t.Fatalf("Login(disabled)=%+v，期望统一 InvalidPassword", result)
	}
}

// TestLoginRemovesCreatedSessionWhenDatabaseUpdateFails 确保最后登录信息提交失败不会遗留未返回给客户端的新会话。
func TestLoginRemovesCreatedSessionWhenDatabaseUpdateFails(t *testing.T) {
	// 先把用户会话填满，记录登录前应完整保留的 sid 集合。
	svcCtx, rds, _ := newAuthFlowTestService(t)
	registerResp := requireAuthTokenResp(t, NewAuthLogic(authFlowContext(
		AuthEventActionRegisterSuccess,
		string(routealias.AuthRegister),
		http.MethodPost,
		"/api/auth/register",
		"10.0.0.8",
	), svcCtx).Register(&types.RegisterReq{
		Username: "database_failure_user",
		Password: "P@ssw0rd!",
	}), codes.CreateSuccess)
	registerSID := requireSessionToken(t, svcCtx, rds, registerResp.User.ID, registerResp.Token)
	cfg := svcCtx.CurrentConfig()
	user, err := model.FindUserByIdentity(
		svcCtx.WriteDB(svc.DatabaseMain),
		model.UserIdentityTypeUsername,
		model.UserIdentityProviderLocal,
		"database_failure_user",
		cfg.AppKey,
		cfg.User.RouteShardCount,
	)
	if err != nil || user == nil {
		t.Fatalf("FindUserByIdentity() user=%+v error=%v", user, err)
	}
	originalSessionIDs := []string{registerSID}
	for len(originalSessionIDs) < maxUserSessions {
		created, createErr := NewAuthLogic(context.Background(), svcCtx).createSession(user, sessionRollbackExistingUser)
		if createErr != nil {
			t.Fatalf("createSession() error = %v", createErr)
		}
		originalSessionIDs = append(originalSessionIDs, created.SessionID)
	}
	requireSessionIndexMembers(t, rds, registerResp.User.ID, originalSessionIDs)

	// 在最后登录更新点注入数据库错误，覆盖会话创建后的补偿路径。
	db := svcCtx.WriteDB(svc.DatabaseMain)
	const callbackName = "test:reject_login_last_login_update"
	if err := db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if tx.Statement.Table == model.TableNameUser {
			tx.AddError(fmt.Errorf("injected last login update failure"))
		}
	}); err != nil {
		t.Fatalf("register update callback: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Callback().Update().Remove(callbackName)
	})

	// 登录失败后新会话必须撤销，原有会话集合保持不变。
	result := NewAuthLogic(authFlowContext(
		AuthEventActionLoginSuccess,
		string(routealias.AuthLogin),
		http.MethodPost,
		"/api/auth/login",
		"10.0.0.9",
	), svcCtx).Login(&types.LoginReq{
		IdentityType:  types.LoginIdentityTypeUsername,
		IdentityValue: "database_failure_user",
		Password:      "P@ssw0rd!",
	})
	if result == nil || result.Code != codes.MySQLUnavailable {
		t.Fatalf("Login() = %+v, want MySQL unavailable", result)
	}
	requireSessionIndexMembers(t, rds, registerResp.User.ID, originalSessionIDs)
}

// TestRuntimeSyncRequiresCommittedAuthVersion 确保内网会话失效只接受主库已提交的新认证版本。
func TestRuntimeSyncRequiresCommittedAuthVersion(t *testing.T) {
	svcCtx, rds, _ := newAuthFlowTestService(t)
	registerResp := requireAuthTokenResp(t, NewAuthLogic(authFlowContext(AuthEventActionRegisterSuccess, string(routealias.AuthRegister), http.MethodPost, "/api/auth/register", "10.0.0.8"), svcCtx).Register(&types.RegisterReq{
		Username: "version_user",
		Password: "P@ssw0rd!",
	}), codes.CreateSuccess)
	sessionID := requireSessionToken(t, svcCtx, rds, registerResp.User.ID, registerResp.Token)

	// 测试直接模拟后台敏感变更事务已提交；生产通用 UpdateUser 不允许修改认证版本。
	if err := svcCtx.WriteDB(svc.DatabaseMain).Table(model.TableNameUser).Where("id = ?", registerResp.User.ID).Update("auth_version", uint64(2)).Error; err != nil {
		t.Fatalf("commit auth_version error = %v", err)
	}
	stale := NewAuthLogic(context.Background(), svcCtx).SyncUserRuntime(&types.UserRuntimeSyncReq{
		ID: registerResp.User.ID, Sessions: true, AuthVersion: 3,
	})
	if stale == nil || stale.Code != codes.ServerError {
		t.Fatalf("SyncUserRuntime(stale) = %+v, want server error", stale)
	}
	requireSessionToken(t, svcCtx, rds, registerResp.User.ID, registerResp.Token)

	synced := NewAuthLogic(context.Background(), svcCtx).SyncUserRuntime(&types.UserRuntimeSyncReq{
		ID: registerResp.User.ID, Sessions: true, AuthVersion: 2,
	})
	if synced == nil || synced.Code != codes.UpdateSuccess {
		t.Fatalf("SyncUserRuntime(committed) = %+v, want update success", synced)
	}
	requireSessionMissing(t, rds, registerResp.User.ID, sessionID)
}

// authFlowEventWant 表示认证主流程期望采集到的风控事件关键字段。
type authFlowEventWant struct {
	action string // 区分注册、登录、刷新与退出成功事件。
	reason string // 事件必须保留当前会话变化的原因。
	route  string // 与本次调用注入的路由别名逐字匹配。
}

// newAuthFlowTestService 创建认证主流程测试所需的 SQLite、Redis 和采集器依赖。
func newAuthFlowTestService(t *testing.T) (*svc.ServiceContext, redis.UniversalClient, *[]collectorx.Event) {
	t.Helper()
	if err := idgen.ConfigureWorkerID(1); err != nil {
		t.Fatalf("ConfigureWorkerID() error = %v", err)
	}
	// SQLite 内存库按测试实例隔离，避免 go test -count 重复运行时复用旧数据。
	dsn := "file:" + strings.ReplaceAll(t.Name(), "/", "_") + "_" + strconv.FormatInt(time.Now().UnixNano(), 10) + "?mode=memory&cache=shared"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		SkipDefaultTransaction: true, // 与正式工厂一致，注册原子性只能由业务显式事务保证。
		Logger:                 logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("gorm.Open(sqlite) error = %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("db.DB() error = %v", err)
	}
	t.Cleanup(func() {
		_ = sqlDB.Close()
	})
	if err := db.AutoMigrate(&authFlowUserSQLite{}); err != nil {
		t.Fatalf("AutoMigrate(User) error = %v", err)
	}
	if err := migrateAuthFlowUserIdentityTables(db); err != nil {
		t.Fatalf("migrateAuthFlowUserIdentityTables() error = %v", err)
	}

	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() {
		_ = client.Close()
	})

	cfg := config.Config{
		AppID:        "site-a",
		AppKey:       "event-secret",
		JwtSecret:    "test-secret-please-change",
		JwtExpiresIn: 3600,
		User:         config.UserConfig{RouteShardCount: model.UserRouteShardCountDefault},
		Auth: config.AuthConfig{
			RegisterEnabled:        true,
			SessionTTLSeconds:      1200,
			ProfileCacheTTLSeconds: 300,
			PasswordMinLength:      8,
		},
		Collector: config.CollectorConfig{
			Enabled: true,
		},
	}
	collector := &fakeCollector{events: make([]collectorx.Event, 0, 4)}
	svcCtx := svc.NewServiceContext(cfg, "v1", svc.Dependencies{
		SiteDBs: svc.SiteDatabases{MainDB: db},
		Rds:     client,
	})
	svcCtx.Collector = collector
	return svcCtx, client, &collector.events
}

// authFlowUserSQLite 使用 SQLite 创建用户表，业务读写仍走 model.User。
type authFlowUserSQLite struct {
	ID              int64     `gorm:"column:id;type:integer;primaryKey;autoIncrement:true;index:idx_user_shard_no_id,priority:2;index:idx_user_status_id,priority:2"` // SQLite 建表使用整数主键，业务仍显式写入雪花 ID。
	ShardNo         int       `gorm:"column:shard_no;type:int;not null;default:0;index:idx_user_shard_no_id,priority:1"`                                              // 与身份索引中的用户路由分片号保持一致。
	Username        string    `gorm:"column:username;type:varchar(32);not null;uniqueIndex:uk_user_username"`                                                         // 用户名唯一约束用于注册冲突验证。
	Nickname        string    `gorm:"column:nickname;type:varchar(64);not null;default:''"`                                                                           // 未指定时由注册逻辑使用用户名。
	PasswordHash    string    `gorm:"column:password_hash;type:varchar(255);not null"`                                                                                // 只保存 bcrypt 结果，登录使用真实密码校验。
	EmailCiphertext string    `gorm:"column:email_ciphertext;type:varchar(512);not null;default:''"`                                                                  // 非空邮箱必须以 AES-GCM 密文落库。
	EmailHash       string    `gorm:"column:email_hash;type:char(64);not null;default:'';index:idx_user_email_hash"`                                                  // HMAC 支持按邮箱定位，不能用于展示。
	EmailMasked     string    `gorm:"column:email_masked;type:varchar(128);not null;default:''"`                                                                      // 资料响应直接使用脱敏值。
	EmailKeyVersion string    `gorm:"column:email_key_version;type:varchar(32);not null;default:''"`                                                                  // 随密文保存解密所需的版本标识。
	PhoneCiphertext string    `gorm:"column:phone_ciphertext;type:varchar(512);not null;default:''"`                                                                  // 非空手机号必须以 AES-GCM 密文落库。
	PhoneHash       string    `gorm:"column:phone_hash;type:char(64);not null;default:'';index:idx_user_phone_hash"`                                                  // HMAC 用于联系方式身份索引核对。
	PhoneMasked     string    `gorm:"column:phone_masked;type:varchar(32);not null;default:''"`                                                                       // 资料响应不得返回原始手机号。
	PhoneKeyVersion string    `gorm:"column:phone_key_version;type:varchar(32);not null;default:''"`                                                                  // 与手机号密文一同更新的密钥版本。
	Avatar          string    `gorm:"column:avatar;type:varchar(255);not null;default:''"`                                                                            // 未设置头像时使用空字符串。
	Status          int       `gorm:"column:status;type:tinyint;not null;default:1;index:idx_user_status_id,priority:1"`                                              // 新注册默认启用，禁用用例显式写入 0。
	AuthVersion     uint64    `gorm:"column:auth_version;type:bigint unsigned;not null;default:1"`                                                                    // 敏感变更推进后必须使旧会话失效。
	LastLoginAt     time.Time `gorm:"column:last_login_at;type:datetime"`                                                                                             // Redis 创建失败时不得提前更新。
	LastLoginIP     string    `gorm:"column:last_login_ip;type:varchar(45);not null;default:''"`                                                                      // 取自测试请求上下文，用于断言成功登录更新。
	CreatedAt       time.Time `gorm:"column:created_at;type:datetime;not null;default:CURRENT_TIMESTAMP"`                                                             // 由 GORM 创建流程填充。
	UpdatedAt       time.Time `gorm:"column:updated_at;type:datetime;not null;default:CURRENT_TIMESTAMP"`                                                             // 由 GORM 写入流程更新。
}

// TableName 返回认证流程 SQLite 用户测试模型映射的真实表名。
func (*authFlowUserSQLite) TableName() string {
	return model.TableNameUser
}

// authFlowUserIdentitySQLite 使用 SQLite 创建用户身份索引表，业务读写仍走 model.UserIdentity。
type authFlowUserIdentitySQLite struct {
	ID            uint64    `gorm:"column:id;type:integer;primaryKey;autoIncrement:true"`               // 测试库自增的身份索引主键。
	Provider      string    `gorm:"column:provider;type:varchar(32);not null;default:''"`               // OAuth 唯一键需要同时区分提供方。
	IdentityValue string    `gorm:"column:identity_value;type:varchar(191);not null;default:''"`        // 用户名或 OAuth 原始索引值，不存储原始联系方式。
	IdentityHash  string    `gorm:"column:identity_hash;type:char(64);not null;default:''"`             // 邮箱和手机号以 HMAC 建立唯一索引。
	UserID        int64     `gorm:"column:user_id;type:integer;not null"`                               // 与主用户表雪花 ID 精确对应。
	UserShardNo   int       `gorm:"column:user_shard_no;type:int;not null"`                             // 登录索引需保留主用户的路由分片号。
	CreatedAt     time.Time `gorm:"column:created_at;type:datetime;not null;default:CURRENT_TIMESTAMP"` // 由身份创建流程填充。
	UpdatedAt     time.Time `gorm:"column:updated_at;type:datetime;not null;default:CURRENT_TIMESTAMP"` // 身份绑定变化时同步更新。
}

// TableName 返回认证流程 SQLite 身份索引测试模型映射的真实表名。
func (*authFlowUserIdentitySQLite) TableName() string {
	return model.TableNameUserIdentityUsername
}

// migrateAuthFlowUserIdentityTables 创建认证流程需要的四张身份索引表。
func migrateAuthFlowUserIdentityTables(db *gorm.DB) error {
	for _, tableName := range []string{
		model.TableNameUserIdentityUsername,
		model.TableNameUserIdentityEmail,
		model.TableNameUserIdentityPhone,
		model.TableNameUserIdentityOAuth,
	} {
		if err := db.Table(tableName).AutoMigrate(&authFlowUserIdentitySQLite{}); err != nil {
			return err
		}
		if err := createAuthFlowUserIdentitySQLiteIndexes(db, tableName); err != nil {
			return err
		}
	}
	return nil
}

// createAuthFlowUserIdentitySQLiteIndexes 使用表名前缀规避 SQLite 全库索引名唯一限制。
func createAuthFlowUserIdentitySQLiteIndexes(db *gorm.DB, tableName string) error {
	var statements []string
	switch tableName {
	case model.TableNameUserIdentityUsername:
		statements = []string{
			fmt.Sprintf("CREATE UNIQUE INDEX IF NOT EXISTS `%s_identity_value` ON `%s` (`identity_value`)", tableName, tableName),
			fmt.Sprintf("CREATE UNIQUE INDEX IF NOT EXISTS `%s_user` ON `%s` (`user_id`)", tableName, tableName),
		}
	case model.TableNameUserIdentityEmail, model.TableNameUserIdentityPhone:
		statements = []string{
			fmt.Sprintf("CREATE UNIQUE INDEX IF NOT EXISTS `%s_identity_hash` ON `%s` (`identity_hash`)", tableName, tableName),
			fmt.Sprintf("CREATE UNIQUE INDEX IF NOT EXISTS `%s_user` ON `%s` (`user_id`)", tableName, tableName),
		}
	case model.TableNameUserIdentityOAuth:
		statements = []string{
			fmt.Sprintf("CREATE UNIQUE INDEX IF NOT EXISTS `%s_provider_value` ON `%s` (`provider`, `identity_value`)", tableName, tableName),
			fmt.Sprintf("CREATE UNIQUE INDEX IF NOT EXISTS `%s_user_provider` ON `%s` (`user_id`, `provider`)", tableName, tableName),
		}
	default:
		return fmt.Errorf("unknown identity table %s", tableName)
	}
	for _, statement := range statements {
		if err := db.Exec(statement).Error; err != nil {
			return err
		}
	}
	return nil
}

// authFlowContext 构造带路由、请求和链路信息的认证测试上下文。
func authFlowContext(action string, route string, method string, path string, clientIP string) context.Context {
	ctx, _ := requestctx.New(context.Background())
	requestctx.SetRoute(ctx, route)
	requestctx.SetRequest(ctx, method, path, clientIP)
	requestctx.SetTrace(ctx, "trace-"+action, "span-"+action)
	requestctx.SetNode(ctx, "node-a")
	requestctx.SetMode(ctx, "test")
	return ctx
}

// authFlowAuthenticatedContext 构造带当前用户和访问令牌的认证测试上下文。
func authFlowAuthenticatedContext(route string, method string, path string, clientIP string, tokenResp *types.AuthTokenResp) context.Context {
	ctx := authFlowContext(route, route, method, path, clientIP)
	if tokenResp != nil && tokenResp.User != nil {
		requestctx.SetUser(ctx, tokenResp.User.ID, tokenResp.User.Username, clientIP)
		requestctx.SetAccessToken(ctx, tokenResp.Token)
		sessionID, _ := tokenSessionClaimsForTest(tokenResp.Token, "test-secret-please-change")
		requestctx.SetSessionID(ctx, sessionID)
	}
	return ctx
}

// setAuthFlowUserSnapshot 模拟鉴权中间件写入主库已确认的脱敏用户快照。
func setAuthFlowUserSnapshot(ctx context.Context, user *model.User) {
	requestctx.SetAuthUser(ctx, &requestctx.AuthUser{
		Profile:     *userlogic.BuildUserProfile(user),
		AuthVersion: user.AuthVersion,
	})
}

// requireAuthTokenResp 断言业务结果为成功的认证令牌响应。
func requireAuthTokenResp(t *testing.T, result *types.BizResult, code int) *types.AuthTokenResp {
	t.Helper()
	if result == nil || !result.IsSuccess() || result.Code != code {
		t.Fatalf("auth result = %+v, want success code=%d", result, code)
	}
	resp, ok := result.Data.(*types.AuthTokenResp)
	if !ok || resp == nil || strings.TrimSpace(resp.Token) == "" || resp.ExpiresAt <= 0 || resp.User == nil {
		t.Fatalf("auth result data = %#v, want AuthTokenResp", result.Data)
	}
	return resp
}

// requireUserProfile 断言业务结果为成功的用户资料响应。
func requireUserProfile(t *testing.T, result *types.BizResult, code int) *types.UserProfile {
	t.Helper()
	if result == nil || !result.IsSuccess() || result.Code != code {
		t.Fatalf("profile result = %+v, want success code=%d", result, code)
	}
	profile, ok := result.Data.(*types.UserProfile)
	if !ok || profile == nil || profile.ID <= 0 {
		t.Fatalf("profile result data = %#v, want UserProfile", result.Data)
	}
	return profile
}

// requireSessionToken 断言 Redis session token 存在并返回 token 中的稳定 sid。
func requireSessionToken(t *testing.T, svcCtx *svc.ServiceContext, rds redis.UniversalClient, userID int64, token string) string {
	t.Helper()
	sessionID, jti := tokenSessionClaimsForTest(token, svcCtx.CurrentConfig().JwtSecret)
	if sessionID == "" || jti == "" {
		t.Fatal("token sid or jti is empty")
	}
	got, err := rds.HGet(context.Background(), keys.UserSessionHashKey(userID), sessionID).Result()
	if err != nil {
		t.Fatalf("Get(session %s) error = %v", sessionID, err)
	}
	if got != token {
		t.Fatalf("session token mismatch sid=%s", sessionID)
	}
	return sessionID
}

// requireSessionMissing 断言指定 sid 对应的 Redis session 已不存在。
func requireSessionMissing(t *testing.T, rds redis.UniversalClient, userID int64, sessionID string) {
	t.Helper()
	exists, err := rds.HExists(context.Background(), keys.UserSessionHashKey(userID), sessionID).Result()
	if err != nil {
		t.Fatalf("HExists(session %s) error = %v", sessionID, err)
	}
	if exists {
		t.Fatalf("session %s exists, want missing", sessionID)
	}
}

// requireSessionTokenNotCurrent 断言指定旧 token 已被同 sid 下的新 token 覆盖。
func requireSessionTokenNotCurrent(t *testing.T, rds redis.UniversalClient, userID int64, sessionID string, oldToken string) {
	t.Helper()
	current, err := rds.HGet(context.Background(), keys.UserSessionHashKey(userID), sessionID).Result()
	if err != nil {
		t.Fatalf("HGet(session %s) error = %v", sessionID, err)
	}
	if current == oldToken {
		t.Fatalf("session %s still stores the old token", sessionID)
	}
}

// requireSessionIndexMembers 断言用户 session 索引中仅包含期望的 sid 集合。
func requireSessionIndexMembers(t *testing.T, rds redis.UniversalClient, userID int64, want []string) {
	t.Helper()
	got, err := rds.ZRange(context.Background(), keys.UserSessionIndexKey(userID), 0, -1).Result()
	if err != nil {
		t.Fatalf("ZRange(session index) error = %v", err)
	}
	if !sameStringSet(got, want) {
		t.Fatalf("session index = %v, want %v", got, want)
	}
}

// requireAuthFlowEvents 断言认证流程采集事件顺序、字段和脱敏结果。
func requireAuthFlowEvents(t *testing.T, events []collectorx.Event, wants []authFlowEventWant) {
	t.Helper()
	if len(events) != len(wants) {
		t.Fatalf("auth events = %d, want %d", len(events), len(wants))
	}
	for index, want := range wants {
		var payload authEventPayload
		if err := json.Unmarshal(events[index].Payload, &payload); err != nil {
			t.Fatalf("Unmarshal(event[%d]) error = %v", index, err)
		}
		if payload.Action != want.action || payload.Reason != want.reason || payload.Route != want.route {
			t.Fatalf("event[%d] payload = %+v, want action=%s reason=%s route=%s", index, payload, want.action, want.reason, want.route)
		}
		raw := string(events[index].Payload)
		for _, forbidden := range []string{"demo_user", "P@ssw0rd!", "10.0.0."} {
			if strings.Contains(raw, forbidden) {
				t.Fatalf("event[%d] leaked raw value %q: %s", index, forbidden, raw)
			}
		}
	}
}

// sameStringSet 判断两个字符串切片是否包含相同元素集合。
func sameStringSet(got []string, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	counts := make(map[string]int, len(want))
	for _, item := range got {
		counts[item]++
	}
	for _, item := range want {
		counts[item]--
		if counts[item] < 0 {
			return false
		}
	}
	return true
}
