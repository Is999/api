package main

import (
	"context"
	"testing"

	"github.com/Is999/go-utils/errors"
)

// lifecycleAppStub 保存进程入口测试的启动、停止结果和停止调用次数。
type lifecycleAppStub struct {
	startErr error // startErr 是监听阶段的注入错误。
	stopErr  error // stopErr 是资源释放阶段的注入错误。
	stopped  int   // stopped 证明启动成功或失败后都执行了清理。
}

// Start 返回测试注入的监听结果。
func (a *lifecycleAppStub) Start() error {
	return a.startErr
}

// Stop 记录清理调用并返回测试注入的资源释放结果。
func (a *lifecycleAppStub) Stop(context.Context) error {
	a.stopped++
	return a.stopErr
}

// TestRunAppLifecycleReturnsFailureWhenStopFails 验证停止失败会覆盖原本成功的进程退出码。
func TestRunAppLifecycleReturnsFailureWhenStopFails(t *testing.T) {
	app := &lifecycleAppStub{stopErr: errors.New("injected stop failure")}
	if code := runAppLifecycle(context.Background(), app); code != 1 {
		t.Fatalf("runAppLifecycle() code = %d, want 1", code)
	}
	if app.stopped != 1 {
		t.Fatalf("Stop() calls = %d, want 1", app.stopped)
	}
}

// TestRunAppLifecycleStopsAfterStartFailure 验证监听失败仍会释放已经装配的基础设施。
func TestRunAppLifecycleStopsAfterStartFailure(t *testing.T) {
	app := &lifecycleAppStub{startErr: errors.New("injected start failure")}
	if code := runAppLifecycle(context.Background(), app); code != 1 {
		t.Fatalf("runAppLifecycle() code = %d, want 1", code)
	}
	if app.stopped != 1 {
		t.Fatalf("Stop() calls = %d, want 1", app.stopped)
	}
}

// TestRunAppLifecycleReturnsSuccessAfterCleanStop 验证启动正常退出且资源释放成功时返回零。
func TestRunAppLifecycleReturnsSuccessAfterCleanStop(t *testing.T) {
	app := &lifecycleAppStub{}
	if code := runAppLifecycle(context.Background(), app); code != 0 {
		t.Fatalf("runAppLifecycle() code = %d, want 0", code)
	}
	if app.stopped != 1 {
		t.Fatalf("Stop() calls = %d, want 1", app.stopped)
	}
}
