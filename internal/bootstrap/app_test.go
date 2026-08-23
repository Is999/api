package bootstrap

import (
	"context"
	stderrors "errors"
	"testing"

	"api/common/runtimecfg"
	bootstrapresources "api/internal/bootstrap/resources"
	"api/internal/config"
	"api/internal/svc"
)

// TestBuildServiceContextDoesNotPublishRuntimeConfigOnFailure 确保启动失败不会污染进程级运行配置。
func TestBuildServiceContextDoesNotPublishRuntimeConfigOnFailure(t *testing.T) {
	prev := runtimecfg.Get()
	runtimecfg.Set(runtimecfg.Snapshot{AppID: "stable-app"})
	t.Cleanup(func() {
		runtimecfg.Restore(prev)
	})

	ctx := t.Context()
	svcCtx, shutdown, err := BuildServiceContext(ctx, config.Config{AppID: "failed-app"}, "failed-version", nil)
	if err == nil {
		if svcCtx != nil {
			_ = bootstrapresources.CloseServiceContextResources(ctx, svcCtx)
		}
		if shutdown != nil {
			_ = shutdown(ctx)
		}
		t.Fatal("期望缺少 MySQL 配置时启动失败")
	}
	if got := runtimecfg.AppID(); got != "stable-app" {
		t.Fatalf("启动失败后 runtimecfg.AppID() = %q, want stable-app", got)
	}
}

// TestCleanupServiceContextAfterStartFailureKeepsAllErrors 确保启动失败清理保留业务资源和 tracing 错误。
func TestCleanupServiceContextAfterStartFailureKeepsAllErrors(t *testing.T) {
	resourceErr := stderrors.New("injected resource cleanup failure")
	tracingErr := stderrors.New("injected tracing cleanup failure")
	registry, err := svc.NewComponentRegistry(svc.Component{
		Name: "test_resource",
		Close: func(context.Context) error {
			return resourceErr
		},
	})
	if err != nil {
		t.Fatalf("NewComponentRegistry() error = %v", err)
	}
	svcCtx := &svc.ServiceContext{}
	svcCtx.SetComponentRegistry(registry)

	err = cleanupServiceContextAfterStartFailure(t.Context(), svcCtx, func(context.Context) error {
		return tracingErr
	})
	if !stderrors.Is(err, resourceErr) || !stderrors.Is(err, tracingErr) {
		t.Fatalf("cleanup error = %v, want both resource and tracing errors", err)
	}
}
