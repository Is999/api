package auth

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"api/internal/config"
	"api/internal/infra/collectorx"
	"api/internal/requestctx"
	"api/internal/routealias"
	"api/internal/svc"

	"github.com/Is999/go-utils/errors"
)

// TestRecordAuthEventEnqueuesSanitizedPayload 确保认证事件只投递脱敏后的结构化负载。
func TestRecordAuthEventEnqueuesSanitizedPayload(t *testing.T) {
	// 请求上下文覆盖链路、路由、节点和客户端地址等事件来源。
	cfg := authEventTestConfig(true)
	svcCtx, collector := newAuthEventTestService(cfg)
	ctx, _ := requestctx.New(context.Background())
	requestctx.SetTrace(ctx, "trace-demo", "span-demo")
	requestctx.SetRoute(ctx, string(routealias.AuthLogin))
	requestctx.SetRequest(ctx, "POST", "/api/auth/login", "127.0.0.1")
	requestctx.SetNode(ctx, "node-a")
	requestctx.SetMode(ctx, "dev")

	// 输入包含必须哈希的身份、会话和客户端信息。
	RecordAuthEvent(ctx, svcCtx, AuthEventInput{
		Action:    AuthEventActionLoginSuccess,
		UserID:    42,
		Identity:  "username:Demo_User",
		SessionID: "session-id",
		Reason:    AuthEventReasonSessionCreated,
	})

	if len(collector.events) != 1 {
		t.Fatalf("collector events = %d, want 1", len(collector.events))
	}
	remaining := time.Until(collector.enqueueDeadline)
	if collector.enqueueDeadline.IsZero() || remaining <= 0 || remaining > authEventEnqueueTimeout {
		t.Fatalf("collector enqueue deadline remaining = %s, want (0,%s]", remaining, authEventEnqueueTimeout)
	}
	event := collector.events[0]
	if event.BizType != AuthCollectorBizType {
		t.Fatalf("biz type = %q, want %q", event.BizType, AuthCollectorBizType)
	}
	if event.PartitionKey != "site-a:42" {
		t.Fatalf("partition key = %q, want site-a:42", event.PartitionKey)
	}
	// 核对事件动作与请求路由，避免业务失败被归入其它入口。
	var payload authEventPayload
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatalf("Unmarshal(payload) error = %v", err)
	}
	if payload.Action != AuthEventActionLoginSuccess || payload.UserID != "42" || payload.Reason != AuthEventReasonSessionCreated {
		t.Fatalf("payload core fields = %+v", payload)
	}
	if payload.AppID != "site-a" || payload.Route != string(routealias.AuthLogin) || payload.TraceID != "trace-demo" || payload.SpanID != "span-demo" {
		t.Fatalf("payload trace fields = %+v", payload)
	}
	// 身份按规范化值计算 HMAC，原始 IP 和 sid 也只能以摘要形式投递。
	if payload.IdentityHash != authSensitiveValueHash(cfg, "username:demo_user") {
		t.Fatalf("identity hash = %q, want deterministic hmac", payload.IdentityHash)
	}
	if payload.ClientIPHash == "" || payload.SessionHash == "" {
		t.Fatalf("payload hashes missing = %+v", payload)
	}
	raw := string(event.Payload)
	for _, forbidden := range []string{"Demo_User", "127.0.0.1", "session-id"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("payload leaked raw value %q: %s", forbidden, raw)
		}
	}
}

// TestAuthEventSnowflakeUserIDUsesJSONString 验证超过 JavaScript 安全整数范围的用户 ID 不会按 JSON number 投递。
func TestAuthEventSnowflakeUserIDUsesJSONString(t *testing.T) {
	const userID int64 = 9_007_199_254_740_993
	payload := buildAuthEventPayload(context.Background(), authEventTestConfig(true), AuthEventInput{
		Action: AuthEventActionLoginSuccess,
		UserID: userID,
	})
	raw, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("Marshal(payload) error = %v", err)
	}
	if !strings.Contains(string(raw), `"user_id":"9007199254740993"`) {
		t.Fatalf("user_id must be a decimal JSON string: %s", raw)
	}
	if got := authEventPartitionKey(payload); got != "site-a:9007199254740993" {
		t.Fatalf("partition key = %q", got)
	}
}

// TestAuthEventPartitionKeyFitsCollectorContract 确保最大 AppID 与脱敏哈希不会超过 Collector 分区键上限。
func TestAuthEventPartitionKeyFitsCollectorContract(t *testing.T) {
	// payload 模拟最大 AppID 和完整 SHA-256 十六进制哈希。
	payload := authEventPayload{
		AppID:        strings.Repeat("a", 64),
		IdentityHash: strings.Repeat("b", 64),
	}
	// partitionKey 是最终写入 Collector Event 的业务分区键。
	partitionKey := authEventPartitionKey(payload)
	if len(partitionKey) > 128 {
		t.Fatalf("partition key bytes = %d, want <= 128", len(partitionKey))
	}
	if want := payload.AppID + ":" + strings.Repeat("b", authEventPartitionHashLength); partitionKey != want {
		t.Fatalf("partition key = %q, want %q", partitionKey, want)
	}
}

// TestRecordAuthEventSkipsWhenCollectorDisabled 确保关闭 Collector 时认证主流程不会产生副作用。
func TestRecordAuthEventSkipsWhenCollectorDisabled(t *testing.T) {
	svcCtx, collector := newAuthEventTestService(authEventTestConfig(false))

	RecordAuthEvent(context.Background(), svcCtx, AuthEventInput{
		Action:   AuthEventActionLoginFailed,
		Identity: "username:demo",
		Reason:   AuthEventReasonInvalidPassword,
	})

	if len(collector.events) != 0 {
		t.Fatalf("collector events = %d, want 0", len(collector.events))
	}
}

// TestRecordAuthEventIgnoresCollectorError 确保 Collector 投递失败不影响认证主流程。
func TestRecordAuthEventIgnoresCollectorError(t *testing.T) {
	svcCtx, collector := newAuthEventTestService(authEventTestConfig(true))
	collector.enqueueErr = errors.Errorf("kafka timeout")

	RecordAuthEvent(context.Background(), svcCtx, AuthEventInput{
		Action:   AuthEventActionRateLimited,
		Identity: "username:demo",
		Reason:   AuthEventReasonLoginIdentityRateLimited,
	})

	if len(collector.events) != 0 {
		t.Fatalf("collector events = %d, want 0", len(collector.events))
	}
}

// TestRuntimeRegistrySpecsValid 确保认证业务运行时注册入口规格完整且名称唯一。
func TestRuntimeRegistrySpecsValid(t *testing.T) {
	specs := RuntimeRegistrySpecs()
	if len(specs) == 0 {
		t.Fatal("RuntimeRegistrySpecs() 不能为空")
	}
	seen := make(map[string]struct{}, len(specs))
	for _, spec := range specs {
		if spec.Name == "" || spec.File == "" || spec.Method == "" || spec.Description == "" {
			t.Fatalf("运行时注册规格字段不完整: %+v", spec)
		}
		if _, ok := seen[spec.Name]; ok {
			t.Fatalf("运行时注册规格名称重复: %s", spec.Name)
		}
		seen[spec.Name] = struct{}{}
	}
}

// authEventTestConfig 返回可切换 Collector 开关的最小事件配置。
func authEventTestConfig(enabled bool) config.Config {
	return config.Config{
		AppID:     "site-a",
		AppKey:    "event-secret",
		JwtSecret: "jwt-secret",
		Collector: config.CollectorConfig{
			Enabled: enabled,
		},
	}
}

// fakeCollector 记录业务投递的 Collector 事件。
type fakeCollector struct {
	events          []collectorx.Event   // 顺序保存成功投递；夹具不供并发调用。
	enqueueErr      error                // 非空时在保存事件前模拟队列失败。
	alertHook       collectorx.AlertHook // 只保留回调，不连接真实告警组件。
	closed          bool                 // 记录生命周期关闭调用。
	enqueueDeadline time.Time            // 断言生产投递是否设置有限等待时间。
}

// Enqueue 记录一条事件。
func (f *fakeCollector) Enqueue(ctx context.Context, event collectorx.Event) (string, error) {
	f.enqueueDeadline, _ = ctx.Deadline()
	if f.enqueueErr != nil {
		return "", f.enqueueErr
	}
	if event.EventID == "" {
		event.EventID = "test-event"
	}
	f.events = append(f.events, event)
	return event.EventID, nil
}

// SetAlertHook 保存告警钩子。
func (f *fakeCollector) SetAlertHook(hook collectorx.AlertHook) {
	f.alertHook = hook
}

// Ready 返回测试 Collector 的就绪状态。
func (f *fakeCollector) Ready(context.Context) error {
	return nil
}

// Close 标记收集器已关闭。
func (f *fakeCollector) Close(context.Context) error {
	f.closed = true
	return nil
}

// newAuthEventTestService 使用内存 Collector 观察认证事件，不启动真实 Kafka 或任务消费者。
func newAuthEventTestService(cfg config.Config) (*svc.ServiceContext, *fakeCollector) {
	collector := &fakeCollector{events: make([]collectorx.Event, 0, 1)}
	svcCtx := svc.NewServiceContext(cfg, "v1", svc.Dependencies{})
	svcCtx.Collector = collector
	return svcCtx, collector
}
