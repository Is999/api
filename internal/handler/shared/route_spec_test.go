package shared

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"api/internal/config"
	"api/internal/requestctx"
	"api/internal/svc"

	"github.com/zeromicro/go-zero/rest"
)

// TestRouteSpecRestRouteWritesRequestMeta 确保路由规格在进入 handler 前写入统一请求元数据。
func TestRouteSpecRestRouteWritesRequestMeta(t *testing.T) {
	// 业务 Handler 直接读取上下文，确认规格包装发生在调用前。
	ctx, _ := requestctx.New(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/live", nil).WithContext(ctx)
	called := false
	spec := RouteSpec{
		Method:        http.MethodGet,
		Path:          "/api/live",
		Meta:          HealthLive,
		DocumentPath:  RouteDocHealth,
		Chain:         RouteSecurityNone,
		SkipAccessLog: true,
		Handler: func(_ *svc.ServiceContext) http.HandlerFunc {
			return func(w http.ResponseWriter, r *http.Request) {
				_ = w
				called = true
				meta := requestctx.FromContext(r.Context())
				if meta == nil {
					t.Fatal("请求元数据不能为空")
				}
				if meta.Route != string(HealthLive.Alias) {
					t.Fatalf("路由别名 = %q, want %q", meta.Route, HealthLive.Alias)
				}
				if !meta.SkipAccessLog {
					t.Fatal("期望路由规格写入跳过访问日志标记")
				}
			}
		},
	}

	// 转换后的真实路由必须执行 Handler 并携带别名和日志策略。
	service := svc.NewServiceContext(config.Config{}, "test-version", svc.Dependencies{})
	route, err := spec.RestRoute(service, nil, nil)
	if err != nil {
		t.Fatalf("RestRoute() error = %v", err)
	}
	route.Handler(httptest.NewRecorder(), req)
	if !called {
		t.Fatal("期望执行路由 handler")
	}
}

// TestRouteSpecRestRouteRejectsSecurityBoundaryDrift 确保内网契约与安全链不一致时返回启动错误，不触发 panic。
func TestRouteSpecRestRouteRejectsSecurityBoundaryDrift(t *testing.T) {
	spec := RouteSpec{
		Method:       http.MethodPost,
		Path:         "/internal/config-reload",
		Meta:         SystemConfigReloadRun,
		DocumentPath: RouteDocSystem,
		Chain:        RouteSecurityNone,
		Handler: func(*svc.ServiceContext) http.HandlerFunc {
			return func(http.ResponseWriter, *http.Request) {}
		},
	}

	service := svc.NewServiceContext(config.Config{}, "test-version", svc.Dependencies{})
	// 其它注册字段完整，错误必须来自访问级别与安全链冲突，不能被更早的字段校验替代。
	if _, err := spec.RestRoute(service, nil, nil); err == nil || !strings.Contains(err.Error(), "路由访问类型与安全链不一致") {
		t.Fatalf("安全边界冲突未命中目标校验: %v", err)
	}
}

// TestRouteSpecRestRouteRejectsMissingMetaAccess 确保扩展路由不能绕过访问边界声明。
func TestRouteSpecRestRouteRejectsMissingMetaAccess(t *testing.T) {
	spec := RouteSpec{
		Method:       http.MethodGet,
		Path:         "/api/custom",
		Meta:         RouteMeta{Alias: HealthLive.Alias, Describe: "缺少访问级别的测试路由"},
		DocumentPath: RouteDocHealth,
		Chain:        RouteSecurityNone,
		Handler: func(*svc.ServiceContext) http.HandlerFunc {
			return func(http.ResponseWriter, *http.Request) {}
		},
	}
	service := svc.NewServiceContext(config.Config{}, "test-version", svc.Dependencies{})
	if _, err := spec.RestRoute(service, nil, nil); err == nil || !strings.Contains(err.Error(), "路由必须声明访问类型") {
		t.Fatalf("缺少访问类型未命中目标校验: %v", err)
	}
}

// TestRouteSpecRestRouteRejectsInvalidRegistrationFields 确保非法 method/path 不进入 go-zero 注册表。
func TestRouteSpecRestRouteRejectsInvalidRegistrationFields(t *testing.T) {
	service := svc.NewServiceContext(config.Config{}, "test-version", svc.Dependencies{})
	for _, spec := range []RouteSpec{
		{Method: "get", Path: "/api/live", Meta: HealthLive, DocumentPath: RouteDocHealth, Chain: RouteSecurityNone},
		{Method: http.MethodGet, Path: "api/live", Meta: HealthLive, DocumentPath: RouteDocHealth, Chain: RouteSecurityNone},
		{Method: http.MethodGet, Path: "/api/live?debug=1", Meta: HealthLive, DocumentPath: RouteDocHealth, Chain: RouteSecurityNone},
		{Method: http.MethodGet, Path: "/api/../live", Meta: HealthLive, DocumentPath: RouteDocHealth, Chain: RouteSecurityNone},
		{Method: http.MethodGet, Path: "/api//live", Meta: HealthLive, DocumentPath: RouteDocHealth, Chain: RouteSecurityNone},
	} {
		spec.Handler = func(*svc.ServiceContext) http.HandlerFunc { return func(http.ResponseWriter, *http.Request) {} }
		if _, err := spec.RestRoute(service, nil, nil); err == nil {
			t.Fatalf("非法路由字段应返回错误: %+v", spec)
		}
	}
}

// TestAddRouteSpecsRejectsConflictBeforeMutatingServer 确保路由冲突在监听前返回错误，且整批规格不写入 Server。
func TestAddRouteSpecsRejectsConflictBeforeMutatingServer(t *testing.T) {
	server, err := rest.NewServer(rest.RestConf{Host: "127.0.0.1", Port: 0})
	if err != nil {
		t.Fatalf("rest.NewServer() error = %v", err)
	}
	t.Cleanup(server.Stop)
	service := svc.NewServiceContext(config.Config{}, "test-version", svc.Dependencies{})
	newSpec := func(routePath string) RouteSpec {
		return RouteSpec{
			Method:       http.MethodGet,
			Path:         routePath,
			Meta:         HealthLive,
			DocumentPath: RouteDocHealth,
			Chain:        RouteSecurityNone,
			Handler: func(*svc.ServiceContext) http.HandlerFunc {
				return func(http.ResponseWriter, *http.Request) {}
			},
		}
	}
	if err := AddRouteSpecs(server, service, nil, nil, []RouteSpec{newSpec("/api/users/:id"), newSpec("/api/users/:name")}); err == nil {
		t.Fatal("参数模式冲突的路由必须在写入 Server 前返回错误")
	}
	if got := len(server.Routes()); got != 0 {
		t.Fatalf("冲突批次写入路由数 = %d, want 0", got)
	}
}
