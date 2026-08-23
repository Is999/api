package auth

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	keys "api/common/rediskeys"
	"api/internal/config"
	userlogic "api/internal/logic/user"
	"api/internal/model"
	"api/internal/requestctx"
	"api/internal/svc"

	"github.com/Is999/go-utils/errors"
	"github.com/alicebob/miniredis/v2"
	"github.com/golang-jwt/jwt/v4"
	"github.com/redis/go-redis/v9"
)

// TestGenerateJWTUsesStringSnowflakeSubjectAndAuthVersion 确保 JWT 无损承载用户 ID 和认证版本。
func TestGenerateJWTUsesStringSnowflakeSubjectAndAuthVersion(t *testing.T) {
	const userID int64 = 9_007_199_254_740_993
	logicObj := newAuthLogicForSession(nil, config.AuthConfig{SessionTTLSeconds: 60})
	tokenString, _, err := logicObj.generateJWT(userID, "demo", 7, "test-sid", "test-jti")
	if err != nil {
		t.Fatalf("generateJWT() error = %v", err)
	}
	claims := jwt.MapClaims{}
	if _, _, err = jwt.NewParser().ParseUnverified(tokenString, claims); err != nil {
		t.Fatalf("ParseUnverified() error = %v", err)
	}
	if claims["sub"] != strconv.FormatInt(userID, 10) {
		t.Fatalf("claims[sub] = %#v, want %q", claims["sub"], strconv.FormatInt(userID, 10))
	}
	if claims["auth_version"] != float64(7) {
		t.Fatalf("claims[auth_version] = %#v, want 7", claims["auth_version"])
	}
	if claims["sid"] != "test-sid" || claims["jti"] != "test-jti" {
		t.Fatalf("session claims = sid:%#v jti:%#v", claims["sid"], claims["jti"])
	}
}

// TestCreateSessionWritesAtomicState 确保创建会话同步写入 Hash、索引和认证版本。
func TestCreateSessionWritesAtomicState(t *testing.T) {
	logicObj, client := newAuthSessionTest(t, 60)
	user := sessionTestUser(1)
	created, err := logicObj.createSession(user, sessionRollbackExistingUser)
	if err != nil {
		t.Fatalf("createSession() error = %v", err)
	}

	if got := client.HGet(context.Background(), keys.UserSessionHashKey(user.ID), created.SessionID).Val(); got != created.Response.Token {
		t.Fatalf("session token = %q, want created token", got)
	}
	if members := client.ZRange(context.Background(), keys.UserSessionIndexKey(user.ID), 0, -1).Val(); len(members) != 1 || members[0] != created.SessionID {
		t.Fatalf("index members = %v, want [%s]", members, created.SessionID)
	}
	if version := client.Get(context.Background(), keys.UserSessionAuthVersionKey(user.ID)).Val(); version != "1" {
		t.Fatalf("auth version = %q, want 1", version)
	}
	hashTTL := client.TTL(context.Background(), keys.UserSessionHashKey(user.ID)).Val()
	if hashTTL <= 0 {
		t.Fatalf("session hash ttl = %v, want positive", hashTTL)
	}
	versionTTL := client.TTL(context.Background(), keys.UserSessionAuthVersionKey(user.ID)).Val()
	if versionTTL < hashTTL-time.Second || versionTTL > hashTTL+time.Second {
		t.Fatalf("auth version ttl = %v, want synchronized with session ttl %v", versionTTL, hashTTL)
	}
}

// TestCreateSessionCleansAppliedWriteWhenResultFails 覆盖 Lua 执行后的网络错误和结果损坏补偿。
func TestCreateSessionCleansAppliedWriteWhenResultFails(t *testing.T) {
	testCases := []struct {
		name      string                   // 用于 t.Run 区分客户端错误和结果损坏
		hook      *createSessionResultHook // 在 Lua 写入后注入响应故障
		wantError string                   // 断言故障向外保留的错误特征
	}{
		{
			name:      "client_error",
			hook:      &createSessionResultHook{resultErr: errors.New(createSessionResultErrorText)},
			wantError: createSessionResultErrorText,
		},
		{
			name:      "malformed_result",
			hook:      &createSessionResultHook{result: []any{int64(1)}},
			wantError: "用户会话创建结果长度非法",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			// 先保留一个合法旧会话，补偿不得误删已有状态。
			server := miniredis.RunT(t)
			client := redis.NewClient(&redis.Options{Addr: server.Addr()})
			t.Cleanup(func() { _ = client.Close() })
			logicObj := newAuthLogicForSession(client, config.AuthConfig{SessionTTLSeconds: 60})
			user := sessionTestUser(1)
			existing, err := logicObj.createSession(user, sessionRollbackExistingUser)
			if err != nil {
				t.Fatalf("createSession(existing) error = %v", err)
			}

			// Hook 在 Lua 写入完成后破坏返回结果，模拟不确定执行结果。
			client.AddHook(testCase.hook)
			if _, err = logicObj.createSession(user, sessionRollbackExistingUser); err == nil || !strings.Contains(err.Error(), testCase.wantError) {
				t.Fatalf("createSession() error = %v, want %q", err, testCase.wantError)
			}
			if !testCase.hook.injected.Load() {
				t.Fatal("create session result was not changed after Lua execution")
			}
			// 补偿完成后 Hash 和索引都只能留下原会话。
			ctx := t.Context()
			if got := client.HGet(ctx, keys.UserSessionHashKey(user.ID), existing.SessionID).Val(); got != existing.Response.Token {
				t.Fatalf("existing session token = %q, want preserved token", got)
			}
			if count := client.HLen(ctx, keys.UserSessionHashKey(user.ID)).Val(); count != 1 {
				t.Fatalf("session hash count = %d, want only existing session", count)
			}
			if members := client.ZRange(ctx, keys.UserSessionIndexKey(user.ID), 0, -1).Val(); len(members) != 1 || members[0] != existing.SessionID {
				t.Fatalf("session index members = %v, want only %s", members, existing.SessionID)
			}
		})
	}
}

// TestRegistrationSessionFailureDeletesEmptyFence 防止未落库用户遗留认证版本 Key。
func TestRegistrationSessionFailureDeletesEmptyFence(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	client.AddHook(&createSessionResultHook{resultErr: errors.New(createSessionResultErrorText)})
	logicObj := newAuthLogicForSession(client, config.AuthConfig{SessionTTLSeconds: 60})
	user := sessionTestUser(1)

	if _, err := logicObj.createSession(user, sessionRollbackUncommittedRegistration); err == nil {
		t.Fatal("createSession() expected injected result error")
	}
	if exists := client.Exists(t.Context(), keys.UserSessionKeys(user.ID)...).Val(); exists != 0 {
		t.Fatalf("registration session keys exist = %d, want 0", exists)
	}
}

// TestCreateSessionKeepsLongestTTL 确保新短会话不会缩短仍有效旧会话的容器 TTL。
func TestCreateSessionKeepsLongestTTL(t *testing.T) {
	logicObj, client := newAuthSessionTest(t, 3600)
	user := sessionTestUser(1)
	if _, err := logicObj.createSession(user, sessionRollbackExistingUser); err != nil {
		t.Fatalf("createSession(first) error = %v", err)
	}
	firstTTL := client.TTL(context.Background(), keys.UserSessionHashKey(user.ID)).Val()

	cfg := logicObj.Svc.CurrentConfig()
	cfg.Auth.SessionTTLSeconds = 60
	logicObj.Svc.UpdateConfig(cfg)
	if _, err := logicObj.createSession(user, sessionRollbackExistingUser); err != nil {
		t.Fatalf("createSession(second) error = %v", err)
	}
	if remainingTTL := client.TTL(context.Background(), keys.UserSessionHashKey(user.ID)).Val(); remainingTTL < firstTTL-time.Second {
		t.Fatalf("short session reduced hash ttl: before=%v after=%v", firstTTL, remainingTTL)
	}
}

// TestCreateSessionKeepsNewShortSessionAtCapacity 确保热更新缩短 TTL 后的新登录不会被容量淘汰算法立即删除。
func TestCreateSessionKeepsNewShortSessionAtCapacity(t *testing.T) {
	// 预置达到上限的旧会话，再创建一个剩余时间更短的新会话。
	logicObj, client := newAuthSessionTest(t, 3600)
	user := sessionTestUser(1)
	oldSessionIDs := make([]string, 0, maxUserSessions)
	for index := 0; index < maxUserSessions; index++ {
		created, err := logicObj.createSession(user, sessionRollbackExistingUser)
		if err != nil {
			t.Fatalf("createSession(%d) error = %v", index, err)
		}
		oldSessionIDs = append(oldSessionIDs, created.SessionID)
	}
	cfg := logicObj.Svc.CurrentConfig()
	cfg.Auth.SessionTTLSeconds = 60
	logicObj.Svc.UpdateConfig(cfg)
	created, err := logicObj.createSession(user, sessionRollbackExistingUser)
	if err != nil {
		t.Fatalf("createSession(short) error = %v", err)
	}
	ctx := context.Background()
	if !client.HExists(ctx, keys.UserSessionHashKey(user.ID), created.SessionID).Val() {
		t.Fatal("new short session was evicted before it could be returned")
	}
	if count := client.HLen(ctx, keys.UserSessionHashKey(user.ID)).Val(); count != maxUserSessions {
		t.Fatalf("session count = %d, want %d", count, maxUserSessions)
	}
	evictedOldCount := 0
	for _, sessionID := range oldSessionIDs {
		if !client.HExists(ctx, keys.UserSessionHashKey(user.ID), sessionID).Val() {
			evictedOldCount++
		}
	}
	if evictedOldCount != 1 {
		t.Fatalf("evicted old sessions = %d, want 1", evictedOldCount)
	}
}

// TestCreateSessionPrunesExpiredAndCapsSessions 确保创建时清理过期会话并把每用户有效会话限制在硬上限内。
func TestCreateSessionPrunesExpiredAndCapsSessions(t *testing.T) {
	logicObj, client := newAuthSessionTest(t, 3600)
	user := sessionTestUser(1)
	ctx := context.Background()
	if err := client.HSet(ctx, keys.UserSessionHashKey(user.ID), "expired", "expired-token").Err(); err != nil {
		t.Fatalf("HSet(expired) error = %v", err)
	}
	if err := client.ZAdd(ctx, keys.UserSessionIndexKey(user.ID), redis.Z{Score: float64(time.Now().Add(-time.Minute).UnixMilli()), Member: "expired"}).Err(); err != nil {
		t.Fatalf("ZAdd(expired) error = %v", err)
	}

	sessionIDs := make([]string, 0, maxUserSessions+2)
	for index := 0; index < maxUserSessions+2; index++ {
		created, err := logicObj.createSession(user, sessionRollbackExistingUser)
		if err != nil {
			t.Fatalf("createSession(%d) error = %v", index, err)
		}
		sessionIDs = append(sessionIDs, created.SessionID)
		time.Sleep(time.Millisecond)
	}
	if client.HExists(ctx, keys.UserSessionHashKey(user.ID), "expired").Val() {
		t.Fatal("expired session still exists")
	}
	if count := client.HLen(ctx, keys.UserSessionHashKey(user.ID)).Val(); count != maxUserSessions {
		t.Fatalf("session count = %d, want %d", count, maxUserSessions)
	}
	for _, evicted := range sessionIDs[:2] {
		if client.HExists(ctx, keys.UserSessionHashKey(user.ID), evicted).Val() {
			t.Fatalf("oldest session %s still exists", evicted)
		}
	}
}

// TestRollbackCreatedSessionHonorsAdvancedAuthVersion 确保失败补偿不会恢复已被新认证版本失效的旧登录态。
func TestRollbackCreatedSessionHonorsAdvancedAuthVersion(t *testing.T) {
	logicObj, client := newAuthSessionTest(t, 60)
	user := sessionTestUser(1)
	var latest *createdSession
	for index := 0; index <= maxUserSessions; index++ {
		created, err := logicObj.createSession(user, sessionRollbackExistingUser)
		if err != nil {
			t.Fatalf("createSession(%d) error = %v", index, err)
		}
		latest = created
	}
	if latest == nil || len(latest.Evicted) != 1 {
		t.Fatalf("latest evicted sessions = %+v, want one", latest)
	}
	if err := logicObj.InvalidateUserSessions(user.ID, 2); err != nil {
		t.Fatalf("InvalidateUserSessions() error = %v", err)
	}
	if err := logicObj.rollbackCreatedSession(user.ID, user.AuthVersion, latest, sessionRollbackUncommittedRegistration); err != nil {
		t.Fatalf("rollbackCreatedSession() error = %v", err)
	}
	if count := client.HLen(context.Background(), keys.UserSessionHashKey(user.ID)).Val(); count != 0 {
		t.Fatalf("session count after version advance rollback = %d, want 0", count)
	}
	if version := client.Get(context.Background(), keys.UserSessionAuthVersionKey(user.ID)).Val(); version != "2" {
		t.Fatalf("auth version after rollback = %q, want 2", version)
	}
}

// TestRegistrationRollbackKeepsOtherSession 确保注册补偿只在会话集合为空时删除版本栅栏。
func TestRegistrationRollbackKeepsOtherSession(t *testing.T) {
	logicObj, client := newAuthSessionTest(t, 60)
	user := sessionTestUser(1)
	existing, err := logicObj.createSession(user, sessionRollbackExistingUser)
	if err != nil {
		t.Fatalf("createSession(existing) error = %v", err)
	}
	created, err := logicObj.createSession(user, sessionRollbackUncommittedRegistration)
	if err != nil {
		t.Fatalf("createSession(registration) error = %v", err)
	}
	if err = logicObj.rollbackCreatedSession(user.ID, user.AuthVersion, created, sessionRollbackUncommittedRegistration); err != nil {
		t.Fatalf("rollbackCreatedSession() error = %v", err)
	}
	if got := client.HGet(t.Context(), keys.UserSessionHashKey(user.ID), existing.SessionID).Val(); got != existing.Response.Token {
		t.Fatalf("existing session token = %q, want preserved token", got)
	}
	if version := client.Get(t.Context(), keys.UserSessionAuthVersionKey(user.ID)).Val(); version != "1" {
		t.Fatalf("auth version = %q, want 1", version)
	}
}

// TestCreateSessionAdvancesVersionAndRejectsStaleSnapshot 确保新数据库版本原子清旧会话，旧快照不能覆盖新版本。
func TestCreateSessionAdvancesVersionAndRejectsStaleSnapshot(t *testing.T) {
	logicObj, client := newAuthSessionTest(t, 60)
	oldUser := sessionTestUser(1)
	oldSession, err := logicObj.createSession(oldUser, sessionRollbackExistingUser)
	if err != nil {
		t.Fatalf("create old session error = %v", err)
	}
	newUser := sessionTestUser(2)
	newSession, err := logicObj.createSession(newUser, sessionRollbackExistingUser)
	if err != nil {
		t.Fatalf("create new version session error = %v", err)
	}
	ctx := context.Background()
	if client.HExists(ctx, keys.UserSessionHashKey(oldUser.ID), oldSession.SessionID).Val() {
		t.Fatal("old auth version session still exists")
	}
	if !client.HExists(ctx, keys.UserSessionHashKey(newUser.ID), newSession.SessionID).Val() {
		t.Fatal("new auth version session missing")
	}
	if _, err = logicObj.createSession(oldUser, sessionRollbackExistingUser); !errors.Is(err, ErrAuthVersionMismatch) {
		t.Fatalf("stale create error = %v, want ErrAuthVersionMismatch", err)
	}
}

// TestSessionAuthVersionKeepsUint64Precision 确保 Lua 不把 uint64 认证版本转换为双精度浮点数。
func TestSessionAuthVersionKeepsUint64Precision(t *testing.T) {
	logicObj, client := newAuthSessionTest(t, 60)
	user := sessionTestUser(^uint64(0))
	if _, err := logicObj.createSession(user, sessionRollbackExistingUser); err != nil {
		t.Fatalf("createSession(max uint64) error = %v", err)
	}
	if version := client.Get(context.Background(), keys.UserSessionAuthVersionKey(user.ID)).Val(); version != "18446744073709551615" {
		t.Fatalf("auth version = %q, want max uint64", version)
	}
	if err := logicObj.InvalidateUserSessions(user.ID, ^uint64(0)-1); !errors.Is(err, ErrAuthVersionMismatch) {
		t.Fatalf("stale max uint64 invalidate error = %v, want ErrAuthVersionMismatch", err)
	}
}

// TestCreateAndInvalidateVersionIsolation 确保旧数据库快照与新版本失效并发时不会留下逃逸会话。
func TestCreateAndInvalidateVersionIsolation(t *testing.T) {
	// 先创建旧认证版本会话，再用已提交的新版本执行全量失效。
	logicObj, client := newAuthSessionTest(t, 60)
	oldUser := sessionTestUser(1)
	ctx := context.Background()
	for round := 0; round < 50; round++ {
		if err := client.Del(ctx, keys.UserSessionKeys(oldUser.ID)...).Err(); err != nil {
			t.Fatalf("Del(session state) error = %v", err)
		}
		if _, err := logicObj.createSession(oldUser, sessionRollbackExistingUser); err != nil {
			t.Fatalf("create initial session error = %v", err)
		}

		start := make(chan struct{})
		var createErr error
		var invalidateErr error
		var workers sync.WaitGroup
		workers.Add(2)
		go func() {
			defer workers.Done()
			<-start
			_, createErr = logicObj.createSession(oldUser, sessionRollbackExistingUser)
		}()
		go func() {
			defer workers.Done()
			<-start
			invalidateErr = logicObj.InvalidateUserSessions(oldUser.ID, 2)
		}()
		close(start)
		workers.Wait()
		if createErr != nil && !errors.Is(createErr, ErrAuthVersionMismatch) {
			t.Fatalf("round %d create error = %v", round, createErr)
		}
		if invalidateErr != nil {
			t.Fatalf("round %d invalidate error = %v", round, invalidateErr)
		}
		if version := client.Get(ctx, keys.UserSessionAuthVersionKey(oldUser.ID)).Val(); version != "2" {
			t.Fatalf("round %d auth version = %q, want 2", round, version)
		}
		if count := client.HLen(ctx, keys.UserSessionHashKey(oldUser.ID)).Val(); count != 0 {
			t.Fatalf("round %d stale session count = %d, want 0", round, count)
		}
	}
}

// TestRotateSessionConsumesPreviousTokenOnce 验证轮换保留 sid、生成新 jti，并拒绝同一旧 token 再次刷新。
func TestRotateSessionConsumesPreviousTokenOnce(t *testing.T) {
	logicObj, client := newAuthSessionTest(t, 60)
	user := sessionTestUser(1)
	previous, err := logicObj.createSession(user, sessionRollbackExistingUser)
	if err != nil {
		t.Fatalf("createSession() error = %v", err)
	}
	authUser := sessionTestAuthUser(user)
	if _, err = logicObj.rotateSession(authUser, previous.SessionID, " "+previous.Response.Token+" "); !errors.Is(err, ErrSessionStale) {
		t.Fatalf("rotateSession(padded token) error = %v, want ErrSessionStale", err)
	}
	first, err := logicObj.rotateSession(authUser, previous.SessionID, previous.Response.Token)
	if err != nil {
		t.Fatalf("rotateSession(first) error = %v", err)
	}
	if _, err = logicObj.rotateSession(authUser, previous.SessionID, previous.Response.Token); !errors.Is(err, ErrSessionStale) {
		t.Fatalf("rotateSession(second) error = %v, want ErrSessionStale", err)
	}
	_, previousJTI := tokenSessionClaimsForTest(previous.Response.Token, logicObj.Svc.CurrentConfig().JwtSecret)
	newSID, newJTI := tokenSessionClaimsForTest(first.Token, logicObj.Svc.CurrentConfig().JwtSecret)
	if newSID != previous.SessionID || newJTI == previousJTI {
		t.Fatalf("rotated claims sid=%q jti=%q, want sid=%q and a new jti", newSID, newJTI, previous.SessionID)
	}
	members := client.ZRange(context.Background(), keys.UserSessionIndexKey(user.ID), 0, -1).Val()
	if len(members) != 1 || members[0] != previous.SessionID {
		t.Fatalf("index members = %v, want [%s]", members, previous.SessionID)
	}
}

// TestRotateSessionPreservesNewerProfileCache 防止刷新请求把旧鉴权快照写回共享资料缓存。
func TestRotateSessionPreservesNewerProfileCache(t *testing.T) {
	logicObj, _ := newAuthSessionTest(t, 60)
	user := sessionTestUser(1)
	created, err := logicObj.createSession(user, sessionRollbackExistingUser)
	if err != nil {
		t.Fatalf("createSession() error = %v", err)
	}
	authUser := sessionTestAuthUser(user)
	newerProfile := authUser.Profile
	newerProfile.Nickname = "newer"
	profileLogic := userlogic.NewUserLogic(t.Context(), logicObj.Svc)
	if err = profileLogic.CacheUserProfile(user.ID, &newerProfile); err != nil {
		t.Fatalf("CacheUserProfile() error = %v", err)
	}
	if _, err = logicObj.rotateSession(authUser, created.SessionID, created.Response.Token); err != nil {
		t.Fatalf("rotateSession() error = %v", err)
	}
	cached, err := profileLogic.GetUserProfile(user.ID)
	if err != nil {
		t.Fatalf("GetUserProfile() error = %v", err)
	}
	if cached == nil || cached.Nickname != newerProfile.Nickname {
		t.Fatalf("cached profile = %+v, want nickname %q", cached, newerProfile.Nickname)
	}
}

// TestRotateSessionRejectsStaleAuthSnapshot 确保中间件取数后认证版本推进时，Redis 栅栏拒绝旧快照刷新。
func TestRotateSessionRejectsStaleAuthSnapshot(t *testing.T) {
	logicObj, client := newAuthSessionTest(t, 60)
	user := sessionTestUser(1)
	created, err := logicObj.createSession(user, sessionRollbackExistingUser)
	if err != nil {
		t.Fatalf("createSession() error = %v", err)
	}
	if err = logicObj.InvalidateUserSessions(user.ID, 2); err != nil {
		t.Fatalf("InvalidateUserSessions() error = %v", err)
	}
	if _, err = logicObj.rotateSession(sessionTestAuthUser(user), created.SessionID, created.Response.Token); !errors.Is(err, ErrAuthVersionMismatch) {
		t.Fatalf("rotateSession() error = %v, want ErrAuthVersionMismatch", err)
	}
	// 版本围栏必须推进到新值，旧版本不能再创建或轮换会话。
	if version := client.Get(context.Background(), keys.UserSessionAuthVersionKey(user.ID)).Val(); version != "2" {
		t.Fatalf("auth version = %q, want 2", version)
	}
	requireNoSessionState(t, client, user.ID)
}

// TestRotateSessionConcurrentCAS 验证 16 个 goroutine 刷新同一旧 token 时只有一个 Lua CAS 成功。
func TestRotateSessionConcurrentCAS(t *testing.T) {
	logicObj, _ := newAuthSessionTest(t, 60)
	user := sessionTestUser(1)
	previous, err := logicObj.createSession(user, sessionRollbackExistingUser)
	if err != nil {
		t.Fatalf("createSession() error = %v", err)
	}
	authUser := sessionTestAuthUser(user)

	var succeeded atomic.Int64
	var stale atomic.Int64
	var unexpected atomic.Int64
	var workers sync.WaitGroup
	for index := 0; index < 16; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			_, rotateErr := logicObj.rotateSession(authUser, previous.SessionID, previous.Response.Token)
			switch {
			case rotateErr == nil:
				succeeded.Add(1)
			case errors.Is(rotateErr, ErrSessionStale):
				stale.Add(1)
			default:
				unexpected.Add(1)
			}
		}()
	}
	workers.Wait()
	if succeeded.Load() != 1 || stale.Load() != 15 || unexpected.Load() != 0 {
		t.Fatalf("refresh results success=%d stale=%d unexpected=%d, want 1/15/0", succeeded.Load(), stale.Load(), unexpected.Load())
	}
}

// TestRefreshThenLogoutDeletesRotatedSession 确保刷新成功后退出仍按稳定 sid 删除新 token。
func TestRefreshThenLogoutDeletesRotatedSession(t *testing.T) {
	logicObj, client := newAuthSessionTest(t, 60)
	user := sessionTestUser(1)
	created, err := logicObj.createSession(user, sessionRollbackExistingUser)
	if err != nil {
		t.Fatalf("createSession() error = %v", err)
	}
	if _, err = logicObj.rotateSession(sessionTestAuthUser(user), created.SessionID, created.Response.Token); err != nil {
		t.Fatalf("rotateSession() error = %v", err)
	}
	if err = logicObj.deleteUserSession(user.ID, created.SessionID); err != nil {
		t.Fatalf("deleteUserSession() error = %v", err)
	}
	requireNoSessionState(t, client, user.ID)
	if ttl := client.TTL(context.Background(), keys.UserSessionAuthVersionKey(user.ID)).Val(); ttl <= 0 {
		t.Fatalf("auth version fence ttl after logout = %v, want positive", ttl)
	}
}

// TestLogoutThenRefreshRejectsDeletedSession 确保退出先完成时刷新完整旧 token 的 CAS 失败。
func TestLogoutThenRefreshRejectsDeletedSession(t *testing.T) {
	logicObj, client := newAuthSessionTest(t, 60)
	user := sessionTestUser(1)
	created, err := logicObj.createSession(user, sessionRollbackExistingUser)
	if err != nil {
		t.Fatalf("createSession() error = %v", err)
	}
	if err = logicObj.deleteUserSession(user.ID, created.SessionID); err != nil {
		t.Fatalf("deleteUserSession() error = %v", err)
	}
	if _, err = logicObj.rotateSession(sessionTestAuthUser(user), created.SessionID, created.Response.Token); !errors.Is(err, ErrSessionStale) {
		t.Fatalf("rotateSession() error = %v, want ErrSessionStale", err)
	}
	requireNoSessionState(t, client, user.ID)
}

// TestRefreshLogoutConcurrentLeavesNoSession 确保刷新与退出真实并发后不会留下逃逸会话。
func TestRefreshLogoutConcurrentLeavesNoSession(t *testing.T) {
	// 多轮复现刷新 CAS 与退出删除的真实竞争顺序。
	logicObj, client := newAuthSessionTest(t, 60)
	user := sessionTestUser(1)
	authUser := sessionTestAuthUser(user)
	for round := 0; round < 50; round++ {
		created, err := logicObj.createSession(user, sessionRollbackExistingUser)
		if err != nil {
			t.Fatalf("round %d createSession() error = %v", round, err)
		}

		// 同一屏障同时释放两个操作，避免测试自行串行化。
		start := make(chan struct{})
		var rotateErr error
		var deleteErr error
		var workers sync.WaitGroup
		workers.Add(2)
		go func() {
			defer workers.Done()
			<-start
			_, rotateErr = logicObj.rotateSession(authUser, created.SessionID, created.Response.Token)
		}()
		go func() {
			defer workers.Done()
			<-start
			deleteErr = logicObj.deleteUserSession(user.ID, created.SessionID)
		}()
		close(start)
		workers.Wait()

		// 无论谁先成功，最终状态都必须没有可用会话。
		if rotateErr != nil && !errors.Is(rotateErr, ErrSessionStale) {
			t.Fatalf("round %d rotate error = %v", round, rotateErr)
		}
		if deleteErr != nil {
			t.Fatalf("round %d delete error = %v", round, deleteErr)
		}
		requireNoSessionState(t, client, user.ID)
	}
}

// TestDeleteUserSessionRemovesHashAndIndex 确保退出登录原子删除 Hash 字段和索引成员。
func TestDeleteUserSessionRemovesHashAndIndex(t *testing.T) {
	logicObj, client := newAuthSessionTest(t, 60)
	user := sessionTestUser(1)
	created, err := logicObj.createSession(user, sessionRollbackExistingUser)
	if err != nil {
		t.Fatalf("createSession() error = %v", err)
	}
	if err := logicObj.deleteUserSession(user.ID, created.SessionID); err != nil {
		t.Fatalf("deleteUserSession() error = %v", err)
	}
	ctx := context.Background()
	if client.HExists(ctx, keys.UserSessionHashKey(user.ID), created.SessionID).Val() {
		t.Fatal("deleted session still exists")
	}
	if members := client.ZRange(ctx, keys.UserSessionIndexKey(user.ID), 0, -1).Val(); len(members) != 0 {
		t.Fatalf("index members = %v, want empty", members)
	}
}

// TestSessionRevocationCoversLongestJWT 确保缩短配置不会提前移除旧会话的撤销标记。
func TestSessionRevocationCoversLongestJWT(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	logicObj := newAuthLogicForSession(client, config.AuthConfig{SessionTTLSeconds: config.MaxJWTExpiresInSeconds})
	cfg := logicObj.Svc.CurrentConfig()
	cfg.JwtExpiresIn = config.MaxJWTExpiresInSeconds
	logicObj.Svc.UpdateConfig(cfg)
	user := sessionTestUser(1)
	created, err := logicObj.createSession(user, sessionRollbackExistingUser)
	if err != nil {
		t.Fatal(err)
	}
	// 模拟已签发长 token 的进程切到短配置，撤销时仍须覆盖原 token 的最长存活期。
	cfg.JwtExpiresIn = 1
	cfg.Auth.SessionTTLSeconds = 1
	logicObj.Svc.UpdateConfig(cfg)
	if err = logicObj.deleteUserSession(user.ID, created.SessionID); err != nil {
		t.Fatal(err)
	}
	revokedKey := keys.UserSessionRevokedKey(user.ID, created.SessionID)
	wantTTL := time.Duration(config.MaxJWTExpiresInSeconds+1) * time.Second
	if ttl := client.TTL(t.Context(), revokedKey).Val(); ttl != wantTTL {
		t.Fatalf("revocation ttl = %v, want %v", ttl, wantTTL)
	}
	// 标记独立于有效会话容器存活；达到硬上限后由 Redis 自动回收。
	server.FastForward(time.Second)
	if exists := client.Exists(t.Context(), revokedKey).Val(); exists != 1 {
		t.Fatal("撤销标记随短配置提前到期")
	}
	server.FastForward(wantTTL - time.Second)
	if exists := client.Exists(t.Context(), revokedKey).Val(); exists != 0 {
		t.Fatal("撤销标记超过硬上限仍未到期")
	}
}

// TestRollbackCreatedSessionCannotBeRestored 覆盖两个失败登录互相淘汰时的两种补偿顺序。
func TestRollbackCreatedSessionCannotBeRestored(t *testing.T) {
	for _, earlierFirst := range []bool{true, false} {
		t.Run(strconv.FormatBool(earlierFirst), func(t *testing.T) {
			logicObj, client := newAuthSessionTest(t, 3600)
			user := sessionTestUser(1)
			for range maxUserSessions {
				if _, err := logicObj.createSession(user, sessionRollbackExistingUser); err != nil {
					t.Fatal(err)
				}
			}
			earlier, err := logicObj.createSession(user, sessionRollbackExistingUser)
			if err != nil {
				t.Fatal(err)
			}
			// 固定后一个登录淘汰前一个未提交会话，不依赖随机 sid 或 goroutine 调度。
			if err = client.ZAdd(t.Context(), keys.UserSessionIndexKey(user.ID), redis.Z{
				Score: float64(time.Now().Add(time.Minute).UnixMilli()), Member: earlier.SessionID,
			}).Err(); err != nil {
				t.Fatal(err)
			}
			later, err := logicObj.createSession(user, sessionRollbackExistingUser)
			if err != nil {
				t.Fatal(err)
			}
			if len(later.Evicted) != 1 || later.Evicted[0].SessionID != earlier.SessionID {
				t.Fatalf("later evicted = %+v, want earlier sid", later.Evicted)
			}
			ordered := []*createdSession{later, earlier}
			wantCount := int64(maxUserSessions)
			if earlierFirst {
				ordered = []*createdSession{earlier, later}
				// 较早补偿时容量仍满，按原规则不恢复其淘汰项。
				wantCount--
			}
			for _, created := range ordered {
				if err = logicObj.rollbackCreatedSession(user.ID, user.AuthVersion, created, sessionRollbackExistingUser); err != nil {
					t.Fatal(err)
				}
			}
			for _, created := range ordered {
				if client.HExists(t.Context(), keys.UserSessionHashKey(user.ID), created.SessionID).Val() {
					t.Fatal("失败登录的 sid 被另一补偿恢复")
				}
				if _, err = client.ZScore(t.Context(), keys.UserSessionIndexKey(user.ID), created.SessionID).Result(); !errors.Is(err, redis.Nil) {
					t.Fatalf("失败登录的索引仍存在: %v", err)
				}
			}
			if count := client.HLen(t.Context(), keys.UserSessionHashKey(user.ID)).Val(); count != wantCount {
				t.Fatalf("session count = %d, want %d", count, wantCount)
			}
		})
	}
}

// TestRollbackCreatedSessionKeepsRotatedToken 确保旧 token 的补偿不撤销同 sid 下已成功轮换的 token。
func TestRollbackCreatedSessionKeepsRotatedToken(t *testing.T) {
	logicObj, client := newAuthSessionTest(t, 60)
	user := sessionTestUser(1)
	created, err := logicObj.createSession(user, sessionRollbackExistingUser)
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := logicObj.rotateSession(sessionTestAuthUser(user), created.SessionID, created.Response.Token)
	if err != nil {
		t.Fatal(err)
	}
	// 补偿持有旧 token，完整值 CAS 应同时保护当前 Hash 与撤销状态。
	if err = logicObj.rollbackCreatedSession(user.ID, user.AuthVersion, created, sessionRollbackExistingUser); err != nil {
		t.Fatal(err)
	}
	if token := client.HGet(t.Context(), keys.UserSessionHashKey(user.ID), created.SessionID).Val(); token != rotated.Token {
		t.Fatal("旧补偿删除了轮换后的 token")
	}
	if exists := client.Exists(t.Context(), keys.UserSessionRevokedKey(user.ID, created.SessionID)).Val(); exists != 0 {
		t.Fatal("旧补偿错误标记已轮换的 sid 为撤销")
	}
}

// TestRollbackCreatedSessionAfterDBFailureIgnoresCanceledRequest 确保请求取消不阻断会话回滚，也不删除资料缓存。
func TestRollbackCreatedSessionAfterDBFailureIgnoresCanceledRequest(t *testing.T) {
	logicObj, client := newAuthSessionTest(t, 60)
	user := sessionTestUser(1)
	created, err := logicObj.createSession(user, sessionRollbackExistingUser)
	if err != nil {
		t.Fatalf("createSession() error = %v", err)
	}
	if err := userlogic.NewUserLogic(logicObj.Ctx, logicObj.Svc).CacheUserProfile(user.ID, userlogic.BuildUserProfile(user)); err != nil {
		t.Fatalf("CacheUserProfile() error = %v", err)
	}
	profileKey := logicObj.AppRedisKey(fmt.Sprintf(keys.UserProfile, user.ID))
	if exists := client.Exists(context.Background(), profileKey).Val(); exists != 1 {
		t.Fatalf("profile cache exists = %d, want 1", exists)
	}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	canceledLogic := NewAuthLogic(canceledCtx, logicObj.Svc)
	if err := canceledLogic.rollbackCreatedSessionWithIndependentTimeout(user.ID, user.AuthVersion, created, sessionRollbackExistingUser); err != nil {
		t.Fatalf("rollbackCreatedSessionWithIndependentTimeout() error = %v", err)
	}
	requireNoSessionState(t, client, user.ID)
	if version := client.Get(t.Context(), keys.UserSessionAuthVersionKey(user.ID)).Val(); version != "1" {
		t.Fatalf("auth version = %q, want rollback fence 1", version)
	}
	if exists := client.Exists(t.Context(), profileKey).Val(); exists != 1 {
		t.Fatalf("profile cache exists = %d, want preserved", exists)
	}
}

// TestInvalidateUserSessionsUsesCommittedVersion 确保全量失效原子清理会话并拒绝旧版本覆盖。
func TestInvalidateUserSessionsUsesCommittedVersion(t *testing.T) {
	logicObj, client := newAuthSessionTest(t, 60)
	user := sessionTestUser(1)
	for index := 0; index < 2; index++ {
		if _, err := logicObj.createSession(user, sessionRollbackExistingUser); err != nil {
			t.Fatalf("createSession(%d) error = %v", index, err)
		}
	}
	if err := logicObj.InvalidateUserSessions(user.ID, 2); err != nil {
		t.Fatalf("InvalidateUserSessions() error = %v", err)
	}
	ctx := context.Background()
	if count := client.HLen(ctx, keys.UserSessionHashKey(user.ID)).Val(); count != 0 {
		t.Fatalf("session count = %d, want 0", count)
	}
	if version := client.Get(ctx, keys.UserSessionAuthVersionKey(user.ID)).Val(); version != "2" {
		t.Fatalf("auth version = %q, want 2", version)
	}
	versionTTL := client.TTL(ctx, keys.UserSessionAuthVersionKey(user.ID)).Val()
	if want := time.Duration(logicObj.authVersionFenceTTL()) * time.Second; versionTTL < want-time.Second || versionTTL > want {
		t.Fatalf("auth version ttl = %v, want %v", versionTTL, want)
	}
	if err := logicObj.InvalidateUserSessions(user.ID, 1); !errors.Is(err, ErrAuthVersionMismatch) {
		t.Fatalf("stale invalidate error = %v, want ErrAuthVersionMismatch", err)
	}
}

// TestSessionKeysShareClusterHashTag 确保会话 Lua 使用的全部 Key 可在 Redis Cluster 同槽执行。
func TestSessionKeysShareClusterHashTag(t *testing.T) {
	got := append(keys.UserSessionKeys(42), keys.UserSessionRevokedKey(42, "sid"))
	want := []string{
		"app:site-a:user:session:{42}",
		"app:site-a:user:session:index:{42}",
		"app:site-a:user:session:auth_version:{42}",
		"app:site-a:user:session:revoked:{42}:sid",
	}
	sort.Strings(got)
	sort.Strings(want)
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("session key[%d] = %q, want %q", index, got[index], want[index])
		}
	}
}

// TestSessionTTLDoesNotExceedJWT 确保 Redis 会话 TTL 不超过 JWT 过期时间。
func TestSessionTTLDoesNotExceedJWT(t *testing.T) {
	logicObj := newAuthLogicForSession(nil, config.AuthConfig{SessionTTLSeconds: 7200})
	if got, want := logicObj.sessionTTL(), int64(3600); got != want {
		t.Fatalf("sessionTTL() = %d, want %d", got, want)
	}
	logicObj = newAuthLogicForSession(nil, config.AuthConfig{SessionTTLSeconds: 1200})
	if got, want := logicObj.sessionTTL(), int64(1200); got != want {
		t.Fatalf("sessionTTL() = %d, want %d", got, want)
	}
}

// requireNoSessionState 断言会话 Hash 和 sid 索引均为空。
func requireNoSessionState(t *testing.T, client redis.UniversalClient, userID int64) {
	t.Helper()
	ctx := t.Context()
	if count := client.HLen(ctx, keys.UserSessionHashKey(userID)).Val(); count != 0 {
		t.Fatalf("session hash count = %d, want 0", count)
	}
	if count := client.ZCard(ctx, keys.UserSessionIndexKey(userID)).Val(); count != 0 {
		t.Fatalf("session index count = %d, want 0", count)
	}
}

// newAuthSessionTest 构造带 miniredis 的会话测试依赖。
func newAuthSessionTest(t *testing.T, ttlSeconds int64) (*AuthLogic, redis.UniversalClient) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return newAuthLogicForSession(client, config.AuthConfig{SessionTTLSeconds: ttlSeconds}), client
}

// newAuthLogicForSession 固定 JWT 一小时上限，会话期限由各用例覆盖且不依赖数据库。
func newAuthLogicForSession(client redis.UniversalClient, authCfg config.AuthConfig) *AuthLogic {
	cfg := config.Config{
		AppID:        "site-a",
		JwtSecret:    "test-secret-please-change",
		JwtExpiresIn: 3600,
		Auth:         authCfg,
	}
	return NewAuthLogic(context.Background(), svc.NewServiceContext(cfg, "v1", svc.Dependencies{Rds: client}))
}

// sessionTestUser 返回指定认证版本的测试用户。
func sessionTestUser(authVersion uint64) *model.User {
	return &model.User{ID: 42, Username: "demo", Status: model.UserStatusEnabled, AuthVersion: authVersion}
}

// sessionTestAuthUser 构造中间件已确认的请求级用户快照。
func sessionTestAuthUser(user *model.User) *requestctx.AuthUser {
	return &requestctx.AuthUser{
		Profile:     *userlogic.BuildUserProfile(user),
		AuthVersion: user.AuthVersion,
	}
}

// createSessionResultErrorText 模拟 Lua 已执行但客户端读取结果失败。
const createSessionResultErrorText = "injected create session result failure"

// createSessionResultHook 模拟 Lua 已落库后的响应错误或格式损坏。
type createSessionResultHook struct {
	result    []any       // 非空时替换脚本返回值
	resultErr error       // 非空时替换客户端返回错误
	injected  atomic.Bool // 保证只拦截首次成功的脚本响应
}

// DialHook 不注入连接阶段错误。
func (*createSessionResultHook) DialHook(next redis.DialHook) redis.DialHook {
	return next
}

// ProcessHook 在首次成功执行 Lua 后替换客户端结果。
func (h *createSessionResultHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		err := next(ctx, cmd)
		if err != nil || (cmd.Name() != "eval" && cmd.Name() != "evalsha") || !h.injected.CompareAndSwap(false, true) {
			return err
		}
		if h.resultErr != nil {
			return h.resultErr
		}
		resultCmd, ok := cmd.(*redis.Cmd)
		if !ok {
			return errors.Errorf("unexpected script command type %T", cmd)
		}
		resultCmd.SetVal(h.result)
		return nil
	}
}

// ProcessPipelineHook 不参与单命令响应故障注入。
func (*createSessionResultHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

// tokenSessionClaimsForTest 解析测试 token 中的 sid 和 jti。
func tokenSessionClaimsForTest(tokenString string, secret string) (string, string) {
	claims := jwt.MapClaims{}
	parser := jwt.NewParser(jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	token, err := parser.ParseWithClaims(strings.TrimSpace(tokenString), claims, func(*jwt.Token) (interface{}, error) {
		return []byte(strings.TrimSpace(secret)), nil
	})
	if err != nil || token == nil || !token.Valid {
		return "", ""
	}
	sessionID, _ := claims["sid"].(string)
	jti, _ := claims["jti"].(string)
	return strings.TrimSpace(sessionID), strings.TrimSpace(jti)
}
