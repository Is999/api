package bootstrap

import (
	"context"
	"strings"
	"sync"
	"time"

	"api/internal/bootstrap/appalert"
	"api/internal/config"
	"api/internal/infra/collectorx"
	"api/internal/infra/larkx"
	"api/internal/infra/loggerx"

	"github.com/Is999/go-utils/errors"
	"github.com/zeromicro/go-zero/core/logx"
)

const runtimeAlertSuppressWindow = 5 * time.Minute // 同一运行异常 Lark 告警合并窗口，避免循环刷屏。

// runtimeAlertNotifier 约束 Lark 运行异常发送器，便于单测替换。
type runtimeAlertNotifier interface {
	SendRuntimeAlert(ctx context.Context, alert larkx.RuntimeAlert) error
}

// runtimeAlertState 保存单条运行异常的限频状态。
type runtimeAlertState struct {
	LastSentAt      time.Time // 最近一次发送时间
	SuppressedCount int       // 窗口内被合并的重复次数
}

// runtimeAlertSink 负责给 API 运行异常补齐服务信息、限频并发送 Lark。
type runtimeAlertSink struct {
	cfg      config.Config                // 当前启动配置快照
	notifier runtimeAlertNotifier         // Lark 运行异常发送器
	now      func() time.Time             // 当前时间函数，测试中可替换
	mu       sync.Mutex                   // 保护 cfg 和 states
	states   map[string]runtimeAlertState // 告警指纹到限频状态
}

// newRuntimeAlertSink 创建 API 运行异常告警发送器；未启用 Lark 时返回 nil。
func newRuntimeAlertSink(cfg config.Config) (*runtimeAlertSink, error) {
	notifier, err := larkx.New(cfg.Alert.Lark)
	if err != nil {
		return nil, errors.Wrap(err, "初始化 Lark 运行异常告警失败")
	}
	if notifier == nil {
		return nil, nil
	}
	return &runtimeAlertSink{
		cfg:      cfg,
		notifier: notifier,
		now:      time.Now,
		states:   make(map[string]runtimeAlertState),
	}, nil
}

// notify 发送一条运行异常告警；发送失败只记录日志，不影响原业务错误返回。
func (s *runtimeAlertSink) notify(ctx context.Context, alert larkx.RuntimeAlert) {
	if s == nil || s.notifier == nil {
		return
	}
	alert = s.prepare(alert)
	// 限频只保护外部通知，不改变触发告警的原业务结果。
	if !s.shouldSend(&alert) {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := s.notifier.SendRuntimeAlert(ctx, alert); err != nil {
		loggerx.Errorw(ctx, "Lark API运行异常告警发送失败", err,
			logx.Field("kind", alert.Kind),
			logx.Field("component", alert.Component),
			logx.Field("operation", alert.Operation),
			logx.Field("unique_key", alert.UniqueKey),
		)
	}
}

// updateConfig 刷新告警卡片展示用配置；Lark 发送端配置仍需重启后生效。
func (s *runtimeAlertSink) updateConfig(cfg config.Config) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cfg = cfg
}

// prepare 补齐运行异常的服务、环境、时间和默认指纹。
func (s *runtimeAlertSink) prepare(alert larkx.RuntimeAlert) larkx.RuntimeAlert {
	s.mu.Lock()
	cfg := s.cfg
	s.mu.Unlock()
	if alert.ServiceName == "" {
		alert.ServiceName = strings.TrimSpace(cfg.Observability.ServiceName)
	}
	if alert.ServiceName == "" {
		alert.ServiceName = strings.TrimSpace(cfg.Name)
	}
	if alert.Environment == "" {
		alert.Environment = strings.TrimSpace(cfg.Mode)
	}
	if alert.AppID == "" {
		alert.AppID = strings.TrimSpace(cfg.AppID)
	}
	if alert.OccurredAt.IsZero() {
		alert.OccurredAt = s.now()
	}
	alert.Kind = strings.TrimSpace(alert.Kind)
	alert.Component = strings.TrimSpace(alert.Component)
	alert.Operation = strings.TrimSpace(alert.Operation)
	alert.UniqueKey = strings.TrimSpace(alert.UniqueKey)
	if alert.UniqueKey == "" {
		alert.UniqueKey = runtimeAlertKey(alert)
	}
	return alert
}

// shouldSend 按告警指纹限频，窗口内重复触发只累计次数。
func (s *runtimeAlertSink) shouldSend(alert *larkx.RuntimeAlert) bool {
	if alert == nil {
		return false
	}
	key := runtimeAlertKey(*alert)
	now := s.now()
	s.mu.Lock()
	defer s.mu.Unlock()
	state := s.states[key]
	if !state.LastSentAt.IsZero() && now.Sub(state.LastSentAt) < runtimeAlertSuppressWindow {
		state.SuppressedCount++
		s.states[key] = state
		return false
	}
	alert.TriggerCount = state.SuppressedCount + 1
	// 窗口结束后的首张卡片带上压制次数，并开始新的计数窗口。
	s.states[key] = runtimeAlertState{LastSentAt: now}
	return true
}

// runtimeAlertKey 生成低基数告警指纹。
func runtimeAlertKey(alert larkx.RuntimeAlert) string {
	parts := make([]string, 0, 4)
	for _, item := range []string{alert.Kind, alert.Component, alert.Operation, alert.UniqueKey} {
		item = strings.TrimSpace(item)
		if item != "" {
			parts = append(parts, item)
		}
	}
	if len(parts) == 0 {
		return "runtime_alert"
	}
	return strings.Join(parts, ":")
}

// bindCollectorRuntimeAlerts 将 Collector 运行异常接入 API Lark 告警。
func (a *App) bindCollectorRuntimeAlerts() {
	if a == nil || a.ServiceContext == nil || a.ServiceContext.Collector == nil || a.runtimeAlerts == nil {
		return
	}
	a.ServiceContext.Collector.SetAlertHook(func(ctx context.Context, alert collectorx.RuntimeAlert) {
		a.notifyRuntimeAlert(ctx, appalert.CollectorRuntimeAlert(alert))
	})
}

// updateRuntimeAlertConfig 刷新运行异常告警卡片中的服务上下文。
func (a *App) updateRuntimeAlertConfig(cfg config.Config) {
	if a == nil || a.runtimeAlerts == nil {
		return
	}
	a.runtimeAlerts.updateConfig(cfg)
}

// notifyRuntimeAlert 上报 API 运行异常。
func (a *App) notifyRuntimeAlert(ctx context.Context, alert larkx.RuntimeAlert) {
	if a == nil || a.runtimeAlerts == nil {
		return
	}
	a.runtimeAlerts.notify(ctx, alert)
}

// notifyLifecycleFailure 上报 API 启动或平滑停止失败。
func (a *App) notifyLifecycleFailure(ctx context.Context, phase, hookName string, err error) {
	if err == nil {
		return
	}
	a.notifyRuntimeAlert(ctx, appalert.LifecycleFailure(phase, hookName, err))
}

// notifyConfigReloadFailure 上报 config.yaml 热加载失败。
func (a *App) notifyConfigReloadFailure(message string, err error, source, category, configFile string) {
	if err == nil {
		return
	}
	a.notifyRuntimeAlert(context.Background(), appalert.ConfigReloadFailure(message, err, source, category, configFile))
}
