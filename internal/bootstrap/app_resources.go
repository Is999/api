package bootstrap

import (
	"context"

	"api/common/runtimecfg"
	"api/internal/bootstrap/components"
	bootstrapresources "api/internal/bootstrap/resources"
	"api/internal/config"
	"api/internal/infra/collectorx"
	"api/internal/security"
	"api/internal/svc"

	"github.com/Is999/go-utils/errors"
)

// BuildServiceContext 统一完成基础设施初始化，并发布当前进程运行配置快照。
func BuildServiceContext(ctx context.Context, c config.Config, version string, securityKeys *security.KeyRegistry) (*svc.ServiceContext, func(context.Context) error, error) {
	svcCtx, shutdown, err := bootstrapresources.BuildServiceContext(ctx, c, version, securityKeys)
	if err != nil {
		return nil, nil, errors.Tag(err)
	}
	// 基础设施就绪后发布 Redis Key 等进程快照，后续失败必须恢复。
	previousRuntime := publishRuntimeConfig(c)
	// Collector 在组件注册前构造，失败时回滚快照和基础设施。
	collectorManager, err := collectorx.New(c.Collector)
	if err != nil {
		runtimecfg.Restore(previousRuntime)
		return nil, nil, errors.Join(errors.Tag(err), cleanupServiceContextAfterStartFailure(ctx, svcCtx, shutdown))
	}
	svcCtx.Collector = collectorManager
	// 组件注册表接管 Collector、租约和外部依赖的关闭顺序。
	componentRegistry, err := components.NewRegistry(svcCtx)
	if err != nil {
		runtimecfg.Restore(previousRuntime)
		startErr := errors.Wrapf(err, "构建组件生命周期注册表失败")
		return nil, nil, errors.Join(startErr, cleanupServiceContextAfterStartFailure(ctx, svcCtx, shutdown))
	}
	// 注册表完整后才发布到请求共享上下文。
	svcCtx.SetComponentRegistry(componentRegistry)
	return svcCtx, shutdown, nil
}

// cleanupServiceContextAfterStartFailure 关闭启动中已创建的业务资源和 tracing。
func cleanupServiceContextAfterStartFailure(ctx context.Context, svcCtx *svc.ServiceContext, shutdown func(context.Context) error) error {
	resourceErr := bootstrapresources.CloseServiceContextResources(ctx, svcCtx)
	var tracingErr error
	if shutdown != nil {
		tracingErr = shutdown(context.Background())
	}
	return errors.Join(resourceErr, tracingErr)
}
