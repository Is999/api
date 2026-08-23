package bootstrap

import (
	"context"
	"fmt"
	"net/http"
	"time"

	i18n "api/common/i18n"
	"api/common/idgen"
	"api/internal/bootstrap/appalert"
	"api/internal/bootstrap/hotreload"
	"api/internal/bootstrap/register"
	bootstrapresources "api/internal/bootstrap/resources"
	"api/internal/config"
	"api/internal/handler"
	"api/internal/infra/loggerx"
	authlogic "api/internal/logic/auth"
	"api/internal/security"
	"api/internal/svc"

	"github.com/Is999/go-utils/errors"
	"github.com/zeromicro/go-zero/core/logx"
	"github.com/zeromicro/go-zero/rest"
)

const (
	// httpDrainTimeout 限制发布停机时等待在途 HTTP 请求的最长时间。
	httpDrainTimeout = 5 * time.Second
)

// App 聚合服务运行所需的配置、HTTP Server 和关闭钩子。
type App struct {
	Server         *rest.Server                // HTTP 服务实例
	InternalServer *rest.Server                // 只注册内网路由的独立 HTTP 服务
	ServiceContext *svc.ServiceContext         // 全局服务上下文
	ConfigFile     string                      // 当前应用对应的配置文件路径
	shutdown       func(context.Context) error // tracing 等基础设施关闭钩子
	hotReload      hotreload.State             // 配置热加载运行态资源
	runtimeAlerts  *runtimeAlertSink           // API 运行异常 Lark 告警发送器
	publicHTTP     *httpServerRun              // 公网监听器运行态，用于启动探测和局部失败关闭
	internalHTTP   *httpServerRun              // 内网监听器运行态，用于启动探测和局部失败关闭
}

// New 使用同轮配置校验生成的密钥快照完成依赖装配和 HTTP 服务注册。
func New(ctx context.Context, c config.Config, version string, securityKeys *security.KeyRegistry) (*App, error) {
	if err := i18n.ValidateCatalog(); err != nil {
		return nil, errors.Wrap(err, "校验内嵌多语言资产失败")
	}
	runtimeAlerts, err := newRuntimeAlertSink(c)
	if err != nil {
		return nil, errors.Tag(err)
	}
	// 指标在资源和监听器创建前注册，冲突时直接拒绝启动。
	if err := idgen.RegisterMetrics(); err != nil {
		runtimeAlerts.notify(context.Background(), appalert.LifecycleFailure("start", "metrics_registry", err))
		return nil, errors.Wrap(err, "注册 ID 生成指标失败")
	}
	if err := authlogic.RegisterMetrics(); err != nil {
		runtimeAlerts.notify(context.Background(), appalert.LifecycleFailure("start", "metrics_registry", err))
		return nil, errors.Wrap(err, "注册认证运行指标失败")
	}
	// 基础设施创建成功后，后续装配失败由本方法负责回收。
	svcCtx, shutdown, err := BuildServiceContext(ctx, c, version, securityKeys)
	if err != nil {
		runtimeAlerts.notify(context.Background(), appalert.LifecycleFailure("start", "build_service_context", err))
		return nil, errors.Tag(err)
	}
	// 路由名称在监听器创建前完成去重，避免部分路由生效。
	routeModules := handler.BuiltinRouteModules()
	if err := register.ValidateNamesUnique(register.KindRoute, register.RouteModuleNames(routeModules)); err != nil {
		runtimeAlerts.notify(context.Background(), appalert.LifecycleFailure("start", "route_registry", err))
		return nil, errors.Join(errors.Tag(err), cleanupServiceContextAfterStartFailure(ctx, svcCtx, shutdown))
	}

	restConf := c.RestConf
	// 项目已接入自定义 access log 中间件，关闭 go-zero 默认 HTTP 日志。
	restConf.Middlewares.Log = false
	// 项目 Trace 统一继承 W3C 或 X-Trace-Id，避免框架提前创建不同链路。
	restConf.Middlewares.Trace = false
	server, err := rest.NewServer(restConf)
	if err != nil {
		runtimeAlerts.notify(context.Background(), appalert.LifecycleFailure("start", "http_server", err))
		startErr := errors.Wrapf(err, "创建 HTTP 服务失败 host=%s port=%d", restConf.Host, restConf.Port)
		return nil, errors.Join(startErr, cleanupServiceContextAfterStartFailure(ctx, svcCtx, shutdown))
	}
	internalServer, err := newInternalServer(c)
	if err != nil {
		return nil, errors.Join(errors.Tag(err), cleanupServiceContextAfterStartFailure(ctx, svcCtx, shutdown))
	}
	app := &App{
		Server:         server,
		InternalServer: internalServer,
		ServiceContext: svcCtx,
		shutdown:       shutdown,
		runtimeAlerts:  runtimeAlerts,
		publicHTTP:     newHTTPServerRun("公网", c.Host, c.Port, server),
		internalHTTP:   newHTTPServerRun("内网", c.InternalServer.Host, c.InternalServer.Port, internalServer),
	}
	svcCtx.ConfigReload = app
	app.bindCollectorRuntimeAlerts()
	// 公网和内网路由必须在 Start 前全部注册成功。
	if err := handler.RegisterPublicHandlersWithModules(server, svcCtx, routeModules...); err != nil {
		runtimeAlerts.notify(context.Background(), appalert.LifecycleFailure("start", "route_registry", err))
		startErr := errors.Wrap(err, "注册公网 HTTP 路由失败")
		return nil, errors.Join(startErr, cleanupServiceContextAfterStartFailure(ctx, svcCtx, shutdown))
	}
	if err := handler.RegisterInternalHandlersWithModules(internalServer, svcCtx, routeModules...); err != nil {
		runtimeAlerts.notify(context.Background(), appalert.LifecycleFailure("start", "route_registry", err))
		startErr := errors.Wrap(err, "注册内网 HTTP 路由失败")
		return nil, errors.Join(startErr, cleanupServiceContextAfterStartFailure(ctx, svcCtx, shutdown))
	}
	return app, nil
}

// Start 启动 HTTP 服务。
func (a *App) Start() error {
	if a == nil || a.Server == nil || a.InternalServer == nil {
		err := errors.Errorf("HTTP 服务未初始化")
		if a != nil {
			a.notifyLifecycleFailure(context.Background(), "start", "http_server", err)
		}
		return err
	}
	// watcher 必须在阻塞式 HTTP 启动前创建。
	a.startConfigHotReload()
	cfg := a.ServiceContext.CurrentConfig()
	loggerx.Infow(context.Background(), "应用 HTTP 服务开始监听",
		logx.Field("service", cfg.Name),
		logx.Field("host", fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)),
		logx.Field("internal_host", fmt.Sprintf("%s:%d", cfg.InternalServer.Host, cfg.InternalServer.Port)),
		logx.Field("mode", cfg.Mode),
		logx.Field("version", a.ServiceContext.CurrentVersion()),
	)
	// 任一监听器退出时统一排空并关闭同组监听器。
	err := runHTTPServers([]*httpServerRun{a.internalHTTP, a.publicHTTP}, httpDrainTimeout)
	if err != nil {
		a.notifyLifecycleFailure(context.Background(), "start", "http_server", err)
	}
	return errors.Tag(err)
}

// limitHTTPDrain 到期后关闭仍未结束的连接，确保后续资源关闭可以继续执行。
func limitHTTPDrain(server *http.Server, timeout time.Duration, done <-chan struct{}) {
	if server == nil || timeout <= 0 {
		return
	}
	server.RegisterOnShutdown(func() {
		timer := time.NewTimer(timeout)
		defer timer.Stop()
		select {
		case <-timer.C:
			_ = server.Close()
		case <-done:
		}
	})
}

// Stop 释放服务资源。
func (a *App) Stop(ctx context.Context) error {
	if a == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	var firstErr error
	// 清理继续执行全部阶段，只返回第一处错误。
	recordErr := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = errors.Tag(err)
		}
	}
	// 先停止接收请求并排空在途流量。
	recordErr(shutdownHTTPServers(ctx, a.publicHTTP, a.internalHTTP))
	// watcher 停止后不再发布新的配置快照。
	recordErr(a.stopConfigHotReload(ctx))
	// 请求与 watcher 退出后再关闭业务资源。
	recordErr(bootstrapresources.CloseServiceContextResources(ctx, a.ServiceContext))
	if a.shutdown != nil {
		// tracing 最后关闭，保留资源清理阶段的观测能力。
		recordErr(a.shutdown(ctx))
	}
	if firstErr != nil {
		a.notifyLifecycleFailure(ctx, "stop", "resources", firstErr)
	}
	return firstErr
}
