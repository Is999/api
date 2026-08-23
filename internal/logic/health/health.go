package health

import (
	"context"
	"os"
	"strings"
	"sync"
	"time"

	codes "api/common/codes"
	"api/internal/config"
	corelogic "api/internal/logic"
	"api/internal/svc"
	"api/internal/types"

	"github.com/Is999/go-utils/errors"
)

// 健康检查固定状态和超时阈值。
const (
	healthCheckTimeout = 2 * time.Second // 健康检查单项依赖超时时间
	healthStatusOK     = "ok"            // 健康检查成功状态
	healthStatusError  = "error"         // 健康检查失败状态
	// healthDependencyUnavailableMessage 是公开 readiness 响应的稳定文案，原始驱动错误只进入服务端日志和 trace。
	healthDependencyUnavailableMessage = "依赖不可用"
)

// HealthLogic 负责 live/ready 健康检查。
type HealthLogic struct {
	*corelogic.BaseLogic // BaseLogic 提供统一上下文、日志和 ServiceContext 访问能力。
}

// dependencyCheck 表示一个互不依赖的 readiness 探测。
type dependencyCheck func() (types.HealthDependencyStatus, error)

// NewHealthLogic 创建健康检查 logic。
func NewHealthLogic(ctx context.Context, svcCtx *svc.ServiceContext) *HealthLogic {
	return &HealthLogic{BaseLogic: corelogic.NewBaseLogicWithContext(ctx, svcCtx)}
}

// Liveness 返回进程存活状态。
func (l *HealthLogic) Liveness() *types.HealthStatusResp {
	return &types.HealthStatusResp{
		Status:  healthStatusOK,
		Mode:    "api",
		Node:    l.nodeName(),
		Version: l.currentVersion(),
	}
}

// Readiness 检查核心依赖是否可用。
func (l *HealthLogic) Readiness(ctx context.Context) (*types.HealthStatusResp, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	// 先按组件注册顺序构造探测项，缺失注册表也作为明确依赖返回。
	checks := make([]dependencyCheck, 0, 5)

	if l.service() == nil {
		checks = append(checks, func() (types.HealthDependencyStatus, error) {
			return dependencyError("service_context", codes.DependencyUnavailable, errors.Errorf("ServiceContext未初始化"))
		})
	} else {
		components := l.service().ComponentRegistry()
		items := components.Items()
		if len(items) == 0 {
			checks = append(checks, func() (types.HealthDependencyStatus, error) {
				return dependencyError("component_registry", codes.DependencyUnavailable, errors.Errorf("组件生命周期注册表未初始化"))
			})
		}
		for _, component := range items {
			checks = append(checks, func() (types.HealthDependencyStatus, error) {
				return l.checkComponent(ctx, component)
			})
		}
	}
	// 各依赖并行探测，结果仍按注册顺序返回，便于监控稳定展示。
	statuses, firstErr := runDependencyChecks(checks)

	resp := &types.HealthStatusResp{
		Status:       healthStatusOK,
		Mode:         "api",
		Node:         l.nodeName(),
		Version:      l.currentVersion(),
		Dependencies: statuses,
	}
	// 任一核心依赖失败即将整体 readiness 标记为不可用。
	if firstErr != nil {
		resp.Status = healthStatusError
		return resp, firstErr
	}
	return resp, nil
}

// runDependencyChecks 并行探测独立依赖，并按注册顺序返回状态。
func runDependencyChecks(checks []dependencyCheck) ([]types.HealthDependencyStatus, error) {
	type result struct {
		status types.HealthDependencyStatus // 当前依赖的健康状态
		err    error                        // 当前依赖的探测错误
	}
	results := make([]result, len(checks))
	var wg sync.WaitGroup
	wg.Add(len(checks))
	for index, check := range checks {
		go func() {
			defer wg.Done()
			// 每个探测只写自己的下标，Wait 后统一读取，无须额外互斥锁。
			results[index].status, results[index].err = check()
		}()
	}
	wg.Wait()

	statuses := make([]types.HealthDependencyStatus, len(results))
	var firstErr error
	for index, item := range results {
		statuses[index] = item.status
		if item.err != nil && firstErr == nil {
			firstErr = errors.Tag(item.err)
		}
	}
	return statuses, firstErr
}

// currentConfig 返回健康检查使用的当前配置快照。
func (l *HealthLogic) currentConfig() config.Config {
	if l == nil || l.service() == nil {
		return config.Config{}
	}
	return l.service().CurrentConfig()
}

// currentVersion 返回当前配置版本，缺省时显示 unknown。
func (l *HealthLogic) currentVersion() string {
	if l == nil || l.service() == nil {
		return "unknown"
	}
	version := strings.TrimSpace(l.service().CurrentVersion())
	if version == "" {
		return "unknown"
	}
	return version
}

// nodeName 优先使用配置实例 ID，缺省时回退主机名。
func (l *HealthLogic) nodeName() string {
	cfg := l.currentConfig()
	if strings.TrimSpace(cfg.InstanceID) != "" {
		return strings.TrimSpace(cfg.InstanceID)
	}
	if name, err := os.Hostname(); err == nil && strings.TrimSpace(name) != "" {
		return strings.TrimSpace(name)
	}
	return "unknown"
}

// service 安全返回 ServiceContext，避免健康检查空指针。
func (l *HealthLogic) service() *svc.ServiceContext {
	if l == nil || l.Svc == nil {
		return nil
	}
	return l.Svc
}

// checkComponent 在受控超时时间内探测单个注册组件。
func (l *HealthLogic) checkComponent(ctx context.Context, component svc.Component) (types.HealthDependencyStatus, error) {
	name := strings.TrimSpace(component.Name)
	if name == "" {
		name = "unknown"
	}
	if component.Check == nil {
		// 无探测器表示该组件没有外部就绪条件，不额外发起网络请求。
		return dependencyOK(name), nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	checkCtx, cancel := context.WithTimeout(ctx, healthCheckTimeout)
	defer cancel()
	// 组件实现必须响应 context；这里不通过遗留后台协程强行中断探测。
	if err := component.Check(checkCtx); err != nil {
		code := component.ErrorCode
		if code == 0 {
			code = codes.DependencyUnavailable
		}
		return dependencyError(name, code, err)
	}
	return dependencyOK(name), nil
}

// dependencyOK 构造 ready 依赖正常状态。
func dependencyOK(name string) types.HealthDependencyStatus {
	return types.HealthDependencyStatus{Name: name, Status: healthStatusOK}
}

// dependencyError 构造 ready 依赖异常状态和可追踪错误。
func dependencyError(name string, code int, err error) (types.HealthDependencyStatus, error) {
	status := types.HealthDependencyStatus{Name: name, Status: healthStatusError, Code: code, Message: healthDependencyUnavailableMessage}
	return status, errors.Wrapf(err, "ready依赖检查失败 name=%s code=%d", name, code)
}
