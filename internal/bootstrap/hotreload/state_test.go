package hotreload

import (
	"context"
	"testing"
	"time"
)

// TestStateWatcherLifecycle 确保零值状态可以启动和停止 watcher。
func TestStateWatcherLifecycle(t *testing.T) {
	var state State
	started := make(chan struct{})
	done := make(chan struct{})
	if ok := state.StartWatcher(func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		close(done)
	}); !ok {
		t.Fatal("expected watcher to start")
	}
	<-started
	if !state.WatcherRunning() {
		t.Fatal("expected watcher to be running")
	}
	if ok := state.StartWatcher(func(context.Context) {}); ok {
		t.Fatal("expected duplicate watcher start to be ignored")
	}
	if err := state.StopWatcher(context.Background()); err != nil {
		t.Fatalf("StopWatcher() error = %v", err)
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("watcher did not stop")
	}
	if state.WatcherRunning() {
		t.Fatal("expected watcher to stop")
	}
}

// TestStateStartWatcherClearsAfterRunReturns 确保 watcher 自然退出后可重新启动。
func TestStateStartWatcherClearsAfterRunReturns(t *testing.T) {
	var state State
	done := make(chan struct{})
	if ok := state.StartWatcher(func(context.Context) {
		close(done)
	}); !ok {
		t.Fatal("expected watcher to start")
	}
	<-done
	waitForWatcherState(t, func() bool {
		return !state.WatcherRunning()
	})

	started := make(chan struct{})
	stopped := make(chan struct{})
	if ok := state.StartWatcher(func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		close(stopped)
	}); !ok {
		t.Fatal("expected watcher to restart after natural exit")
	}
	<-started
	if err := state.StopWatcher(context.Background()); err != nil {
		t.Fatalf("StopWatcher() error = %v", err)
	}
	<-stopped
}

// TestStateRejectsRestartUntilStoppingWatcherExits 验证 Stop 等待清理期间不会发布新的 watcher。
func TestStateRejectsRestartUntilStoppingWatcherExits(t *testing.T) {
	// watcher 收到取消后继续等待 release，模拟尚未完成的清理阶段。
	var state State
	started := make(chan struct{})
	cancelled := make(chan struct{})
	release := make(chan struct{})
	if ok := state.StartWatcher(func(ctx context.Context) {
		close(started)
		<-ctx.Done()
		close(cancelled)
		<-release
	}); !ok {
		t.Fatal("expected watcher to start")
	}
	<-started
	// Stop 进入等待后，生命周期槽位仍必须保持占用。
	stopDone := make(chan struct{})
	go func() {
		_ = state.StopWatcher(context.Background())
		close(stopDone)
	}()
	<-cancelled
	if ok := state.StartWatcher(func(context.Context) {}); ok {
		t.Fatal("stopping watcher must keep the lifecycle slot")
	}
	select {
	case <-stopDone:
		t.Fatal("StopWatcher returned before watcher cleanup completed")
	default:
	}
	// 清理释放后 Stop 和下一次 Start 才允许成功。
	close(release)
	select {
	case <-stopDone:
	case <-time.After(time.Second):
		t.Fatal("StopWatcher did not finish after cleanup release")
	}
	if ok := state.StartWatcher(func(context.Context) {}); !ok {
		t.Fatal("expected watcher restart after stop completed")
	}
	if err := state.StopWatcher(context.Background()); err != nil {
		t.Fatalf("StopWatcher() error = %v", err)
	}
}

// TestStateImmediateStartStopStress 验证快速启动停止不会触发 Add/Wait 类生命周期竞态。
func TestStateImmediateStartStopStress(t *testing.T) {
	var state State
	for range 500 {
		if ok := state.StartWatcher(func(ctx context.Context) { <-ctx.Done() }); !ok {
			t.Fatal("expected watcher to start")
		}
		_ = state.StopWatcher(context.Background())
	}
}

// TestStateSuppressFailureWindow 验证重复失败不延长限频窗口，错误变化或恢复后立即重新记录。
func TestStateSuppressFailureWindow(t *testing.T) {
	var state State
	now := time.Unix(100, 0)
	const window = 30 * time.Second

	if state.SuppressFailure("boom", now, window) {
		t.Fatal("首次失败不应被抑制")
	}
	if !state.SuppressFailure("boom", now.Add(29*time.Second), window) {
		t.Fatal("窗口内重复失败应被抑制")
	}
	// 窗口从首次输出起算，而不是从上次被抑制的失败起算。
	if state.SuppressFailure("boom", now.Add(window), window) {
		t.Fatal("到达窗口边界应重新记录")
	}
	// 不同错误不共用限频窗口，避免新故障被旧故障掩盖。
	if state.SuppressFailure("other", now.Add(31*time.Second), window) {
		t.Fatal("错误变化后应立即记录")
	}
	if !state.SuppressFailure("other", now.Add(32*time.Second), window) {
		t.Fatal("新错误的重复失败应被抑制")
	}
	// 成功重载会清空限频状态，同一错误再次发生也应立即记录。
	state.ResetFailureLog()
	if state.SuppressFailure("other", now.Add(33*time.Second), window) {
		t.Fatal("恢复后再次失败应立即记录")
	}
}

// TestCheckInterval 验证热加载轮询间隔默认值和显式配置值。
func TestCheckInterval(t *testing.T) {
	if got := CheckInterval(-1); got != 5*time.Second {
		t.Fatalf("interval -1 = %s, want 5s", got)
	}
	if got := CheckInterval(0); got != 5*time.Second {
		t.Fatalf("interval 0 = %s, want 5s", got)
	}
	if got := CheckInterval(1); got != time.Second {
		t.Fatalf("interval 1 = %s, want 1s", got)
	}
	if got := CheckInterval(2); got != 2*time.Second {
		t.Fatalf("interval 2 = %s, want 2s", got)
	}
}

// waitForWatcherState 等待异步 watcher 状态进入预期。
func waitForWatcherState(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.After(time.Second)
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		if ok() {
			return
		}
		select {
		case <-deadline:
			t.Fatal("等待 watcher 状态变化超时")
		case <-ticker.C:
		}
	}
}
