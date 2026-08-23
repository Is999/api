package bootstrap

import (
	"context"
	"testing"
	"time"

	"api/internal/config"
	"api/internal/infra/larkx"
)

// fakeRuntimeAlertNotifier 记录运行异常告警，避免单测真实请求 Lark。
type fakeRuntimeAlertNotifier struct {
	alerts []larkx.RuntimeAlert // 已发送的告警
}

// SendRuntimeAlert 记录本次告警。
func (f *fakeRuntimeAlertNotifier) SendRuntimeAlert(_ context.Context, alert larkx.RuntimeAlert) error {
	f.alerts = append(f.alerts, alert)
	return nil
}

// TestRuntimeAlertSinkEnrichesSuppressesAndRefreshesConfig 验证告警补全、限频和配置快照刷新。
func TestRuntimeAlertSinkEnrichesSuppressesAndRefreshesConfig(t *testing.T) {
	// 固定时钟和初始配置使限频窗口及补全字段可重复验证。
	notifier := &fakeRuntimeAlertNotifier{}
	now := time.Date(2026, 7, 2, 10, 0, 0, 0, time.UTC)
	sink := &runtimeAlertSink{
		cfg: config.Config{
			AppID: "1",
			Observability: config.ObservabilityConfig{
				ServiceName: "api-old",
			},
		},
		notifier: notifier,
		now:      func() time.Time { return now },
		states:   make(map[string]runtimeAlertState),
	}
	sink.cfg.Name = "api"
	sink.cfg.Mode = "dev"

	alert := larkx.RuntimeAlert{
		Kind:      "collector_enqueue_failed",
		Component: "collector",
		Operation: "publish_kafka",
		UniqueKey: "collector_enqueue_failed:auth.security:kafka",
		Reason:    "kafka publish failed",
	}
	// 首次触发必须发送，并使用当前配置补全服务上下文。
	sink.notify(context.Background(), alert)
	if len(notifier.alerts) != 1 {
		t.Fatalf("首次告警应发送一次，实际 %d", len(notifier.alerts))
	}
	first := notifier.alerts[0]
	if first.ServiceName != "api-old" || first.Environment != "dev" || first.AppID != "1" {
		t.Fatalf("告警上下文补全不符合预期: %+v", first)
	}
	if first.TriggerCount != 1 || first.OccurredAt.IsZero() {
		t.Fatalf("首次告警触发次数或时间不符合预期: %+v", first)
	}

	// 窗口内相同指纹只累计次数，不重复发送。
	now = now.Add(time.Minute)
	sink.notify(context.Background(), alert)
	if len(notifier.alerts) != 1 {
		t.Fatalf("限频窗口内重复告警应被合并，实际发送 %d 次", len(notifier.alerts))
	}

	// 窗口结束后使用最新配置再次发送，并保留累计触发次数。
	nextCfg := sink.cfg
	nextCfg.AppID = "2"
	nextCfg.Mode = "pro"
	nextCfg.Observability.ServiceName = "api-new"
	sink.updateConfig(nextCfg)
	now = now.Add(runtimeAlertSuppressWindow + time.Second)
	sink.notify(context.Background(), alert)
	if len(notifier.alerts) != 2 {
		t.Fatalf("限频窗口后告警应再次发送，实际 %d", len(notifier.alerts))
	}
	second := notifier.alerts[1]
	if second.ServiceName != "api-new" || second.Environment != "pro" || second.AppID != "2" {
		t.Fatalf("告警配置快照未刷新: %+v", second)
	}
	if second.TriggerCount != 2 {
		t.Fatalf("期望累计触发次数为 2，实际 %d", second.TriggerCount)
	}
}
