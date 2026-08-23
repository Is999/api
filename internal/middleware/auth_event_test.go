package middleware

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"api/internal/config"
	"api/internal/infra/collectorx"
	authlogic "api/internal/logic/auth"
	"api/internal/requestctx"
	"api/internal/routealias"
	"api/internal/svc"
)

// TestAuthMiddlewareMissingBearerEmitsAuthSecurityEvent 确保鉴权失败也会投递脱敏风控事件。
func TestAuthMiddlewareMissingBearerEmitsAuthSecurityEvent(t *testing.T) {
	// 真实认证中间件处理无 Bearer 请求，业务 Handler 不得执行。
	svcCtx, seen := newAuthMiddlewareEventService(t)
	middleware := newTestAuthMiddleware(svcCtx)
	nextCalled := false
	handler := middleware.Handle(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
	}, routealias.UserProfile)

	req := httptest.NewRequest(http.MethodGet, "/api/user/profile", nil)
	req.RemoteAddr = "127.0.0.1:12345"
	rec := httptest.NewRecorder()
	handler(rec, req)

	if nextCalled {
		t.Fatal("next handler should not be called")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	if len(*seen) != 1 {
		t.Fatalf("collector events = %d, want 1", len(*seen))
	}
	event := (*seen)[0]
	if event.BizType != authlogic.AuthCollectorBizType {
		t.Fatalf("biz type = %q, want %q", event.BizType, authlogic.AuthCollectorBizType)
	}
	// 事件必须携带失败原因和路由，客户端地址只能以哈希形式出现。
	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatalf("Unmarshal(payload) error = %v", err)
	}
	if payload["action"] != authlogic.AuthEventActionAuthFailed || payload["reason"] != authlogic.AuthEventReasonMissingBearer {
		t.Fatalf("payload action/reason = %+v", payload)
	}
	if payload["route"] != string(routealias.UserProfile) {
		t.Fatalf("payload route = %v, want %s", payload["route"], routealias.UserProfile)
	}
	if _, ok := payload["client_ip_hash"].(string); !ok {
		t.Fatalf("payload client_ip_hash missing: %+v", payload)
	}
	raw := string(event.Payload)
	if strings.Contains(raw, "127.0.0.1") {
		t.Fatalf("payload leaked raw client ip: %s", raw)
	}
}

// TestEmitAuthFailureEventIncludesKnownIdentity 确保已解析身份的失败事件可按用户聚合。
func TestEmitAuthFailureEventIncludesKnownIdentity(t *testing.T) {
	svcCtx, seen := newAuthMiddlewareEventService(t)
	middleware := newTestAuthMiddleware(svcCtx)

	middleware.emitAuthFailureEvent(context.Background(), authlogic.AuthEventReasonSessionExpired, &UserTokenIdentity{
		UserID:    42,
		UserName:  "Demo_User",
		SessionID: "session-id",
	})

	if len(*seen) != 1 {
		t.Fatalf("collector events = %d, want 1", len(*seen))
	}
	event := (*seen)[0]
	if event.PartitionKey != "site-a:42" {
		t.Fatalf("partition key = %q, want site-a:42", event.PartitionKey)
	}
	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatalf("Unmarshal(payload) error = %v", err)
	}
	if payload["user_id"] != "42" {
		t.Fatalf("payload user_id = %v, want 42", payload["user_id"])
	}
	if payload["reason"] != authlogic.AuthEventReasonSessionExpired {
		t.Fatalf("payload reason = %v, want %s", payload["reason"], authlogic.AuthEventReasonSessionExpired)
	}
	raw := string(event.Payload)
	for _, forbidden := range []string{"Demo_User", "session-id"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("payload leaked raw value %q: %s", forbidden, raw)
		}
	}
}

// TestEmitSecurityFailureEvent 确保签名和加密失败也进入 auth.security 脱敏事件。
func TestEmitSecurityFailureEvent(t *testing.T) {
	svcCtx, seen := newAuthMiddlewareEventService(t)
	ctx, _ := requestctx.New(context.Background())
	requestctx.SetRoute(ctx, string(routealias.AuthLogin))
	requestctx.SetRequest(ctx, http.MethodPost, "/api/auth/login", "127.0.0.1")
	requestctx.SetTrace(ctx, "trace-id", "span-id")

	emitSecurityFailureEvent(ctx, &testMiddlewareRuntime{svc: svcCtx}, authlogic.AuthEventReasonRequestDecryptFailed)

	if len(*seen) != 1 {
		t.Fatalf("collector events = %d, want 1", len(*seen))
	}
	var payload map[string]any
	if err := json.Unmarshal((*seen)[0].Payload, &payload); err != nil {
		t.Fatalf("Unmarshal(payload) error = %v", err)
	}
	if payload["action"] != authlogic.AuthEventActionSecurityFailed {
		t.Fatalf("payload action = %v, want %s", payload["action"], authlogic.AuthEventActionSecurityFailed)
	}
	if payload["reason"] != authlogic.AuthEventReasonRequestDecryptFailed {
		t.Fatalf("payload reason = %v, want %s", payload["reason"], authlogic.AuthEventReasonRequestDecryptFailed)
	}
	if payload["route"] != string(routealias.AuthLogin) {
		t.Fatalf("payload route = %v, want %s", payload["route"], routealias.AuthLogin)
	}
	raw := string((*seen)[0].Payload)
	if strings.Contains(raw, "127.0.0.1") {
		t.Fatalf("payload leaked raw client ip: %s", raw)
	}
}

// newAuthMiddlewareEventService 使用内存 Collector 检查脱敏载荷，不验证实际队列投递。
func newAuthMiddlewareEventService(t *testing.T) (*svc.ServiceContext, *[]collectorx.Event) {
	t.Helper()
	cfg := config.Config{
		AppID:     "site-a",
		AppKey:    "event-secret",
		JwtSecret: "jwt-secret",
		Collector: config.CollectorConfig{
			Enabled: true,
		},
	}
	collector := &fakeMiddlewareCollector{events: make([]collectorx.Event, 0, 1)}
	svcCtx := svc.NewServiceContext(cfg, "v1", svc.Dependencies{})
	svcCtx.Collector = collector
	return svcCtx, &collector.events
}

// fakeMiddlewareCollector 记录中间件投递的 Collector 事件。
type fakeMiddlewareCollector struct {
	events    []collectorx.Event   // 顺序记录测试事件；该夹具不供并发调用。
	alertHook collectorx.AlertHook // 保存注册回调，本组测试不主动触发告警。
	closed    bool                 // 记录生命周期调用，不关闭外部资源。
}

// Enqueue 记录一条事件。
func (f *fakeMiddlewareCollector) Enqueue(_ context.Context, event collectorx.Event) (string, error) {
	if event.EventID == "" {
		event.EventID = "test-event"
	}
	f.events = append(f.events, event)
	return event.EventID, nil
}

// SetAlertHook 保存告警钩子。
func (f *fakeMiddlewareCollector) SetAlertHook(hook collectorx.AlertHook) {
	f.alertHook = hook
}

// Ready 返回测试 Collector 的就绪状态。
func (f *fakeMiddlewareCollector) Ready(context.Context) error {
	return nil
}

// Close 标记收集器已关闭。
func (f *fakeMiddlewareCollector) Close(context.Context) error {
	f.closed = true
	return nil
}
