package health

import (
	"context"
	"strings"
	"testing"
	"time"

	codes "api/common/codes"
	"api/internal/config"
	"api/internal/svc"
	"api/internal/types"

	"github.com/Is999/go-utils/errors"
)

// TestReadinessUsesComponentRegistry 确保 ready 检查来自组件生命周期注册表。
func TestReadinessUsesComponentRegistry(t *testing.T) {
	// 内存回调模拟一好一坏两个依赖，不探测真实 MySQL 或 Redis。
	svcCtx := svc.NewServiceContext(config.Config{}, "test-version", svc.Dependencies{})
	registry, err := svc.NewComponentRegistry(
		svc.Component{
			Name:      "mysql",
			ErrorCode: codes.MySQLUnavailable,
			Check: func(context.Context) error {
				return nil
			},
		},
		svc.Component{
			Name:      "redis",
			ErrorCode: codes.RedisUnavailable,
			Check: func(context.Context) error {
				return errors.Errorf("redis down")
			},
		},
	)
	if err != nil {
		t.Fatalf("NewComponentRegistry() error = %v", err)
	}
	svcCtx.SetComponentRegistry(registry)

	// 整体状态应失败，但仍完整返回两个依赖的有序结果。
	resp, err := NewHealthLogic(context.Background(), svcCtx).Readiness(context.Background())
	if err == nil {
		t.Fatal("expected readiness error")
	}
	if resp == nil || resp.Status != healthStatusError {
		t.Fatalf("readiness status = %+v, want error", resp)
	}
	if len(resp.Dependencies) != 2 {
		t.Fatalf("dependency count = %d, want 2", len(resp.Dependencies))
	}
	if resp.Dependencies[0].Name != "mysql" || resp.Dependencies[0].Status != healthStatusOK {
		t.Fatalf("mysql dependency = %+v", resp.Dependencies[0])
	}
	if resp.Dependencies[1].Name != "redis" || resp.Dependencies[1].Code != codes.RedisUnavailable {
		t.Fatalf("redis dependency = %+v", resp.Dependencies[1])
	}
	// 公开响应使用稳定文案，底层驱动错误不得泄漏。
	if resp.Dependencies[1].Message != healthDependencyUnavailableMessage || strings.Contains(resp.Dependencies[1].Message, "redis down") {
		t.Fatalf("redis public message = %q, want stable masked message", resp.Dependencies[1].Message)
	}
}

// TestRunDependencyChecksRunsConcurrently 确保慢依赖不会串行放大 readiness 延迟。
func TestRunDependencyChecksRunsConcurrently(t *testing.T) {
	// 两个探测都到达屏障后才放行，以启动顺序而非耗时阈值证明并发。
	started := make(chan string, 2)
	release := make(chan struct{})
	checks := []dependencyCheck{
		func() (types.HealthDependencyStatus, error) {
			started <- "mysql"
			<-release
			return dependencyOK("mysql"), nil
		},
		func() (types.HealthDependencyStatus, error) {
			started <- "redis"
			<-release
			return dependencyOK("redis"), nil
		},
	}
	done := make(chan []types.HealthDependencyStatus, 1)
	go func() {
		statuses, _ := runDependencyChecks(checks)
		done <- statuses
	}()

	for range checks {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("依赖探测未并发启动")
		}
	}
	close(release)
	statuses := <-done
	if len(statuses) != 2 || statuses[0].Name != "mysql" || statuses[1].Name != "redis" {
		t.Fatalf("依赖状态顺序不稳定: %+v", statuses)
	}
}

// TestReadinessRejectsMissingComponentRegistry 确保组件清单缺失时不会误报 ready。
func TestReadinessRejectsMissingComponentRegistry(t *testing.T) {
	svcCtx := svc.NewServiceContext(config.Config{}, "test-version", svc.Dependencies{})
	resp, err := NewHealthLogic(context.Background(), svcCtx).Readiness(context.Background())
	if err == nil {
		t.Fatal("expected readiness error")
	}
	if resp == nil || resp.Status != healthStatusError || len(resp.Dependencies) != 1 {
		t.Fatalf("readiness response = %+v", resp)
	}
	if resp.Dependencies[0].Name != "component_registry" {
		t.Fatalf("dependency name = %s, want component_registry", resp.Dependencies[0].Name)
	}
}
