package hotreload

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"api/internal/config"
	"api/internal/svc"
)

// State 保存配置热加载运行态资源，零值可用。
type State struct {
	watcher   *watcherRun  // 当前 watcher；停止完成前保持占位，阻止并发重启
	stateMu   sync.RWMutex // 保护 watcher 生命周期
	statusMu  sync.Mutex   // 保护热加载状态快照更新
	execMu    sync.Mutex   // 串行化实际配置重载
	logMu     sync.Mutex   // 保护重复失败日志限频状态
	lastError string       // 最近一次失败日志签名
	lastLogAt time.Time    // 最近一次失败日志输出时间
}

// watcherRun 保存单个 watcher 的取消与退出信号，避免 WaitGroup 的 Add/Wait 并发竞态。
type watcherRun struct {
	cancel context.CancelFunc // 取消当前 watcher
	done   chan struct{}      // watcher 完全退出后关闭
}

// LockExec 锁定配置重载执行通道。
func (s *State) LockExec() {
	if s == nil {
		return
	}
	s.execMu.Lock()
}

// UnlockExec 释放配置重载执行通道。
func (s *State) UnlockExec() {
	if s == nil {
		return
	}
	s.execMu.Unlock()
}

// StartWatcher 启动热加载 watcher，并保证同一时间只有一个 watcher。
func (s *State) StartWatcher(run func(context.Context)) bool {
	if s == nil || run == nil {
		return false
	}
	s.stateMu.Lock()
	if s.watcher != nil {
		s.stateMu.Unlock()
		return false
	}
	ctx, cancel := context.WithCancel(context.Background())
	watcher := &watcherRun{cancel: cancel, done: make(chan struct{})}
	// 启动 goroutine 前先占位，并发 Start 不会再创建第二个 watcher。
	s.watcher = watcher
	s.stateMu.Unlock()
	go func() {
		defer s.clearWatcher(watcher)
		run(ctx)
	}()
	return true
}

// StopWatcher 停止热加载 watcher，并受应用统一停止期限约束。
func (s *State) StopWatcher(ctx context.Context) error {
	if s == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	s.stateMu.Lock()
	watcher := s.watcher
	if watcher == nil {
		s.stateMu.Unlock()
		return nil
	}
	watcher.cancel()
	s.stateMu.Unlock()
	// 退出前保留 watcher 占位，Stop 超时后也不允许另一个实例并行启动。
	select {
	case <-watcher.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// WatcherRunning 返回当前是否已有热加载 watcher 在运行。
func (s *State) WatcherRunning() bool {
	if s == nil {
		return false
	}
	s.stateMu.RLock()
	defer s.stateMu.RUnlock()
	return s.watcher != nil
}

// clearWatcher 在 watcher 自然退出后清理运行标记，允许后续重新启动。
func (s *State) clearWatcher(watcher *watcherRun) {
	s.stateMu.Lock()
	// 占位释放与退出通知在同一锁内完成，新一轮启动不会观察到半清理状态。
	if s.watcher == watcher {
		s.watcher = nil
	}
	close(watcher.done)
	s.stateMu.Unlock()
}

// UpdateStatus 在当前状态基础上执行原子更新。
func (s *State) UpdateStatus(svcCtx *svc.ServiceContext, mutator func(svc.HotReloadStatus) svc.HotReloadStatus) {
	if s == nil || svcCtx == nil || mutator == nil {
		return
	}
	s.statusMu.Lock()
	defer s.statusMu.Unlock()
	// 读旧值与发布新值必须串行，避免手动重载和 watcher 丢失彼此的状态更新。
	status := svcCtx.CurrentHotReloadStatus()
	svcCtx.UpdateHotReloadStatus(mutator(status))
}

// SuppressFailure 判断本次失败日志是否应被限频抑制。
func (s *State) SuppressFailure(errorKey string, now time.Time, window time.Duration) bool {
	if s == nil {
		return false
	}
	s.logMu.Lock()
	defer s.logMu.Unlock()
	sameError := errorKey == s.lastError && !s.lastLogAt.IsZero() && now.Sub(s.lastLogAt) < window
	if sameError {
		// 抑制重复日志不延后窗口，持续故障仍会定期输出。
		return true
	}
	s.lastError = errorKey
	s.lastLogAt = now
	return false
}

// ResetFailureLog 清理重复失败限频状态。
func (s *State) ResetFailureLog() {
	if s == nil {
		return
	}
	s.logMu.Lock()
	s.lastError = ""
	s.lastLogAt = time.Time{}
	s.logMu.Unlock()
}

// Summary 生成运行期配置摘要，便于接口展示和日志排查。
func Summary(cfg config.Config) string {
	return fmt.Sprintf("mode=%s app_id=%s user_route=%d sign=%d crypto=%d collector=%t", cfg.Mode, strings.TrimSpace(cfg.AppID), cfg.User.RouteShardCount, cfg.Security.SecretKey.SignStatus, cfg.Security.SecretKey.CryptoStatus, cfg.Collector.Enabled)
}

// CheckInterval 返回热加载轮询间隔，默认 5 秒。
func CheckInterval(seconds int) time.Duration {
	if seconds <= 0 {
		seconds = 5
	}
	return time.Duration(seconds) * time.Second
}

// Source 归一化热加载触发来源。
func Source(source string) string {
	source = strings.TrimSpace(source)
	if source == "" {
		return "manual"
	}
	return source
}

// FailureCategory 归一化热加载失败分类。
func FailureCategory(category string) string {
	category = strings.TrimSpace(category)
	if category == "" {
		return "reload"
	}
	return category
}
