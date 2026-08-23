package auth

import (
	"context"
	"net/http"
	"testing"
	"time"

	codes "api/common/codes"
	keys "api/common/rediskeys"
	"api/internal/middleware"
	"api/internal/model"
	"api/internal/routealias"
	"api/internal/svc"
	"api/internal/types"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// TestLoginCompensationPreservesLogout 固定退出与登录失败的交错顺序，补偿只恢复未主动退出的淘汰会话。
func TestLoginCompensationPreservesLogout(t *testing.T) {
	for _, logoutEvicted := range []bool{true, false} {
		name := "logout_other_session"
		if logoutEvicted {
			name = "logout_evicted_session"
		}
		t.Run(name, func(t *testing.T) {
			svcCtx, client, _ := newAuthFlowTestService(t)
			cfg := svcCtx.CurrentConfig()
			cfg.Auth.Issuer = "compensation-test"
			svcCtx.UpdateConfig(cfg)
			registered := requireAuthTokenResp(t, NewAuthLogic(t.Context(), svcCtx).Register(&types.RegisterReq{
				Username: "logout_compensation",
				Password: "P@ssw0rd!",
			}), codes.CreateSuccess)
			user, err := model.FindUserByID(svcCtx.WriteDB(svc.DatabaseMain), registered.User.ID, cfg.User.RouteShardCount)
			if err != nil {
				t.Fatal(err)
			}
			oldSID := requireSessionToken(t, svcCtx, client, user.ID, registered.Token)
			// 固定淘汰目标，避免同一毫秒创建的会话依赖随机 sid 排序。
			if err = client.ZAdd(t.Context(), keys.UserSessionIndexKey(user.ID), redis.Z{
				Score: float64(time.Now().Add(30 * time.Second).UnixMilli()), Member: oldSID,
			}).Err(); err != nil {
				t.Fatal(err)
			}
			logoutToken := registered
			for range maxUserSessions - 1 {
				created, createErr := NewAuthLogic(t.Context(), svcCtx).createSession(user, sessionRollbackExistingUser)
				if createErr != nil {
					t.Fatal(createErr)
				}
				if !logoutEvicted {
					logoutToken = created.Response
				}
			}
			// 退出请求先通过真实 JWT/session 校验，再让新登录淘汰旧会话。
			identity, err := middleware.VerifyUserToken(t.Context(), svcCtx, logoutToken.Token, true)
			if err != nil {
				t.Fatal(err)
			}
			logoutCtx := authFlowAuthenticatedContext(string(routealias.AuthLogout), http.MethodPost, "/api/auth/logout", "10.0.0.9", logoutToken)
			loginCtx, cancelLogin := context.WithCancel(t.Context())
			defer cancelLogin()
			db := svcCtx.WriteDB(svc.DatabaseMain)
			const callback = "test:logout_before_login_update"
			logoutCompleted := false
			if err = db.Callback().Update().Before("gorm:update").Register(callback, func(tx *gorm.DB) {
				if client.HExists(t.Context(), keys.UserSessionHashKey(user.ID), oldSID).Val() {
					t.Fatal("新登录应已淘汰最早过期的会话")
				}
				result := NewAuthLogic(logoutCtx, svcCtx).Logout()
				if result == nil || !result.IsSuccess() {
					t.Fatalf("Logout() = %+v", result)
				}
				logoutCompleted = true
				// 只取消登录请求，Redis 与数据库保持健康，触发独立上下文补偿。
				cancelLogin()
			}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = db.Callback().Update().Remove(callback) })
			result := NewAuthLogic(loginCtx, svcCtx).Login(&types.LoginReq{
				IdentityType: types.LoginIdentityTypeUsername, IdentityValue: user.Username, Password: "P@ssw0rd!",
			})
			if !logoutCompleted || result == nil || result.IsSuccess() {
				t.Fatalf("logout completed = %t, Login() = %+v", logoutCompleted, result)
			}
			if _, err = middleware.VerifyUserToken(t.Context(), svcCtx, logoutToken.Token, true); err == nil {
				t.Fatal("退出成功的 token 被登录失败补偿恢复，鉴权再次放行")
			}
			if client.HExists(t.Context(), keys.UserSessionHashKey(user.ID), identity.SessionID).Val() {
				t.Fatal("退出的 sid 仍存在于会话 Hash")
			}
			// 无关会话退出不能影响淘汰项恢复，两种交错最终都应只减少一个有效会话。
			if !logoutEvicted {
				if _, err = middleware.VerifyUserToken(t.Context(), svcCtx, registered.Token, true); err != nil {
					t.Fatalf("未退出的淘汰会话未恢复: %v", err)
				}
			}
			if count := client.HLen(t.Context(), keys.UserSessionHashKey(user.ID)).Val(); count != maxUserSessions-1 {
				t.Fatalf("session count = %d, want %d", count, maxUserSessions-1)
			}
			if count := client.ZCard(t.Context(), keys.UserSessionIndexKey(user.ID)).Val(); count != maxUserSessions-1 {
				t.Fatalf("session index count = %d, want %d", count, maxUserSessions-1)
			}
		})
	}
}
