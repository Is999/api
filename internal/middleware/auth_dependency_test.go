package middleware

import (
	"context"
	"encoding/json"
	stderrors "errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	codes "api/common/codes"
	"api/internal/httpresp"
	"api/internal/requestctx"
	"api/internal/routealias"
	"api/internal/svc"
	"api/internal/types"

	"github.com/Is999/go-utils/errors"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// authDependencyRuntime 只替换主库用户查询；其它方法若被错误调用会通过 nil 嵌入立即暴露测试接线问题。
type authDependencyRuntime struct {
	Runtime                              // Runtime 不应在本测试的无签名、无加密请求中被调用。
	activeUser      *requestctx.AuthUser // activeUser 模拟主库返回的鉴权快照。
	activeUserError error                // activeUserError 模拟主库用户查询的稳定错误分类。
}

// ActiveUser 返回测试注入的主库快照或稳定错误。
func (r *authDependencyRuntime) ActiveUser(context.Context, int64) (*requestctx.AuthUser, error) {
	return r.activeUser, r.activeUserError
}

// TestAuthMiddlewarePassesActiveUserSnapshot 确保主库鉴权快照原样进入受保护的业务上下文。
func TestAuthMiddlewarePassesActiveUserSnapshot(t *testing.T) {
	useTestAppID(t, "site-a")
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	token := signedUserToken(t, "test-secret-please-change", "site-a")
	seedUserSession(t, client, 42, "testsid", token, 1)
	svcCtx := svc.NewServiceContext(tokenTestConfig("site-a"), "v1", svc.Dependencies{Rds: client})
	want := &requestctx.AuthUser{
		Profile:     types.UserProfile{ID: 42, Username: "demo", Nickname: "snapshot"},
		AuthVersion: 1,
	}
	runtime := &authDependencyRuntime{activeUser: want}
	nextCalled := false
	handler := NewAuthMiddleware(svcCtx, runtime).Handle(func(_ http.ResponseWriter, r *http.Request) {
		nextCalled = true
		got := requestctx.AuthUserFromContext(r.Context())
		if got == nil || got.Profile != want.Profile || got.AuthVersion != want.AuthVersion {
			t.Fatalf("AuthUserFromContext() = %+v, want %+v", got, want)
		}
	}, routealias.UserProfile)
	request := httptest.NewRequest(http.MethodGet, "/api/user/profile", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	handler(httptest.NewRecorder(), request)
	if !nextCalled {
		t.Fatal("valid authenticated request did not reach protected handler")
	}
}

// TestAuthMiddlewareReturnsServiceBusyWhenRedisUnavailable 验证 Redis 故障不会被伪装成 token 无效。
func TestAuthMiddlewareReturnsServiceBusyWhenRedisUnavailable(t *testing.T) {
	useTestAppID(t, "site-a")
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{
		Addr:         server.Addr(),
		DialTimeout:  20 * time.Millisecond,
		ReadTimeout:  20 * time.Millisecond,
		WriteTimeout: 20 * time.Millisecond,
		MaxRetries:   -1,
	})
	t.Cleanup(func() {
		_ = client.Close()
	})
	token := signedUserToken(t, "test-secret-please-change", "site-a")
	svcCtx := svc.NewServiceContext(tokenTestConfig("site-a"), "v1", svc.Dependencies{Rds: client})
	server.Close()

	requireAuthDependencyResponse(t, NewAuthMiddleware(svcCtx, &authDependencyRuntime{}), token, http.StatusServiceUnavailable, codes.ServiceBusy)
}

// TestAuthMiddlewareReturnsServiceBusyWhenUserStoreUnavailable 验证主库故障保留已校验 token 身份但返回受控 503。
func TestAuthMiddlewareReturnsServiceBusyWhenUserStoreUnavailable(t *testing.T) {
	useTestAppID(t, "site-a")
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() {
		_ = client.Close()
	})
	token := signedUserToken(t, "test-secret-please-change", "site-a")
	seedUserSession(t, client, 42, "testsid", token, 1)
	svcCtx := svc.NewServiceContext(tokenTestConfig("site-a"), "v1", svc.Dependencies{Rds: client})
	runtime := &authDependencyRuntime{
		activeUserError: stderrors.Join(ErrRuntimeDependencyUnavailable, errors.New("database unavailable")),
	}

	requireAuthDependencyResponse(t, NewAuthMiddleware(svcCtx, runtime), token, http.StatusServiceUnavailable, codes.ServiceBusy)
}

// requireAuthDependencyResponse 执行鉴权请求并校验依赖故障不会进入下游业务处理器。
func requireAuthDependencyResponse(t *testing.T, middleware *AuthMiddleware, token string, wantHTTPStatus int, wantCode int) {
	t.Helper()
	nextCalled := false
	handler := middleware.Handle(func(http.ResponseWriter, *http.Request) {
		nextCalled = true
	}, routealias.UserProfile)
	request := httptest.NewRequest(http.MethodGet, "/api/user/profile", nil)
	request.Header.Set("Authorization", "Bearer "+token)
	recorder := httptest.NewRecorder()

	handler(recorder, request)

	if nextCalled {
		t.Fatal("dependency failure must not call the protected handler")
	}
	if recorder.Code != wantHTTPStatus {
		t.Fatalf("HTTP status = %d, want %d; body=%s", recorder.Code, wantHTTPStatus, recorder.Body.String())
	}
	var response httpresp.ResponseJSON
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("Unmarshal(response) error = %v; body=%s", err, recorder.Body.String())
	}
	if response.Status || response.Code != wantCode {
		t.Fatalf("response = %+v, want failure code %d", response, wantCode)
	}
}
