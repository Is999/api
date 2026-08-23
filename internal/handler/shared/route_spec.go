package shared

import (
	"net/http"
	"path"
	"strings"

	"api/internal/middleware"
	"api/internal/requestctx"
	"api/internal/routealias"
	"api/internal/security"
	"api/internal/svc"

	"github.com/Is999/go-utils/errors"
	"github.com/zeromicro/go-zero/rest"
	"github.com/zeromicro/go-zero/rest/router"
)

// 接口文档路径常量，供路由规格、契约和文档漂移测试复用。
const (
	// RouteDocHealth 表示前台健康检查接口文档路径。
	RouteDocHealth = "docs/site/接口文档/前台系统/健康检查接口.md"
	// RouteDocAuth 表示前台认证接口文档路径。
	RouteDocAuth = "docs/site/接口文档/前台系统/认证接口.md"
	// RouteDocUser 表示前台用户接口文档路径。
	RouteDocUser = "docs/site/接口文档/前台系统/用户接口.md"
	// RouteDocSystem 表示前台系统接口文档路径。
	RouteDocSystem = "docs/site/接口文档/前台系统/系统接口.md"
)

// RouteSecurityChain 表示路由实际挂载的安全链路。
type RouteSecurityChain string

// 路由安全链路枚举常量。
const (
	// RouteSecurityNone 表示路由不经过前台签名、加密或 JWT 链路。
	RouteSecurityNone RouteSecurityChain = "none"
	// RouteSecurityPublic 表示路由经过签名和加密链路，但不校验 JWT。
	RouteSecurityPublic RouteSecurityChain = "public"
	// RouteSecurityAuth 表示路由必须校验 JWT 与 Redis session。
	RouteSecurityAuth RouteSecurityChain = "auth"
	// RouteSecurityInternal 表示路由必须校验内网来源、Ops HMAC 和 nonce 防重放。
	RouteSecurityInternal RouteSecurityChain = "internal"
)

// RouteHandler 根据服务上下文构造未包裹安全链路的业务 Handler。
type RouteHandler func(*svc.ServiceContext) http.HandlerFunc

// RouteSpec 是路由注册、契约、安全链路和文档同步的单一规格。
type RouteSpec struct {
	Method        string             // HTTP 方法
	Path          string             // HTTP 路径
	Meta          RouteMeta          // 路由元数据
	DocumentPath  string             // 仓库根目录下的接口文档路径
	Chain         RouteSecurityChain // 实际安全链路
	InternalOnly  bool               // 是否只注册到内网监听器；不代表必须使用 Ops HMAC
	SkipAccessLog bool               // 是否跳过普通访问日志，适用于 live/ready/metrics 等高频探针
	Handler       RouteHandler       // 真实 Handler 构造函数
}

// RestRoute 将路由规格转换为 go-zero 路由；规格错误返回启动错误，禁止用 panic 终止进程。
func (s RouteSpec) RestRoute(svcCtx *svc.ServiceContext, authMw *middleware.AuthMiddleware, opsMw *middleware.OpsMiddleware) (rest.Route, error) {
	// 注册字段、依赖和安全边界必须在监听前一次性校验完成。
	if err := s.validateRegistrationFields(); err != nil {
		return rest.Route{}, errors.Tag(err)
	}
	if s.Handler == nil {
		return rest.Route{}, errors.Errorf("路由规格缺少 Handler: %s %s", s.Method, s.Path)
	}
	if svcCtx == nil {
		return rest.Route{}, errors.Errorf("路由规格缺少 ServiceContext: %s %s", s.Method, s.Path)
	}
	if err := s.validateSecurityBoundary(); err != nil {
		return rest.Route{}, errors.Tag(err)
	}
	handler := s.Handler(svcCtx)
	if handler == nil {
		return rest.Route{}, errors.Errorf("路由规格构造出空 Handler: %s %s", s.Method, s.Path)
	}
	// 安全链按规格精确包裹，未声明的链路不能静默降级。
	switch s.Chain {
	case RouteSecurityNone:
	case RouteSecurityPublic:
		if authMw == nil {
			return rest.Route{}, errors.Errorf("公开安全路由缺少 AuthMiddleware: %s %s", s.Method, s.Path)
		}
		handler = authMw.PublicHandle(handler, s.Meta.Alias)
	case RouteSecurityAuth:
		if authMw == nil {
			return rest.Route{}, errors.Errorf("登录态路由缺少 AuthMiddleware: %s %s", s.Method, s.Path)
		}
		handler = authMw.Handle(handler, s.Meta.Alias)
	case RouteSecurityInternal:
		if opsMw == nil {
			return rest.Route{}, errors.Errorf("内网路由缺少 OpsMiddleware: %s %s", s.Method, s.Path)
		}
		handler = opsMw.Handle(handler)
	default:
		return rest.Route{}, errors.Errorf("未知路由安全链路: %s", s.Chain)
	}
	// 路由别名和访问日志策略就近写入请求上下文，供后续中间件读取。
	return rest.Route{
		Method: s.Method,
		Path:   s.Path,
		Handler: func(w http.ResponseWriter, r *http.Request) {
			if s.Meta.Alias != "" {
				requestctx.SetRoute(r.Context(), string(s.Meta.Alias))
			}
			if s.SkipAccessLog {
				requestctx.SetSkipAccessLog(r.Context(), true)
			}
			handler(w, r)
		},
	}, nil
}

// validateRegistrationFields 校验 go-zero 注册前必须稳定的路由标识，避免错误延迟到监听阶段 panic。
func (s RouteSpec) validateRegistrationFields() error {
	if s.Method == "" || s.Method != strings.TrimSpace(s.Method) || s.Method != strings.ToUpper(s.Method) {
		return errors.Errorf("路由 HTTP 方法必须是无空白的大写规范值: %q", s.Method)
	}
	if s.Path == "" || s.Path != strings.TrimSpace(s.Path) || !strings.HasPrefix(s.Path, "/") || strings.ContainsAny(s.Path, "?#") {
		return errors.Errorf("路由路径必须是无查询参数的绝对路径: %q", s.Path)
	}
	if path.Clean(s.Path) != s.Path {
		return errors.Errorf("路由路径必须是规范绝对路径: %q", s.Path)
	}
	if s.Meta.Alias == "" || string(s.Meta.Alias) != strings.TrimSpace(string(s.Meta.Alias)) {
		return errors.Errorf("路由别名不能为空或包含首尾空白: %s %s", s.Method, s.Path)
	}
	if s.Meta.Describe == "" || s.Meta.Describe != strings.TrimSpace(s.Meta.Describe) {
		return errors.Errorf("路由说明不能为空或包含首尾空白: %s %s", s.Method, s.Path)
	}
	if s.DocumentPath == "" || s.DocumentPath != strings.TrimSpace(s.DocumentPath) {
		return errors.Errorf("路由文档路径不能为空或包含首尾空白: %s %s", s.Method, s.Path)
	}
	return nil
}

// validateSecurityBoundary 校验契约访问级别与真实中间件链一致，错误必须阻断启动而不是触发 panic。
func (s RouteSpec) validateSecurityBoundary() error {
	if s.Meta.Access == "" {
		return errors.Errorf("路由必须声明访问类型: %s %s", s.Method, s.Path)
	}
	valid := false
	switch s.Meta.Access {
	case RouteAccessPublic:
		valid = s.Chain == RouteSecurityNone || s.Chain == RouteSecurityPublic
	case RouteAccessAuth:
		valid = s.Chain == RouteSecurityAuth
	case RouteAccessInternal:
		valid = s.Chain == RouteSecurityInternal
	default:
		return errors.Errorf("未知路由访问类型: %s", s.Meta.Access)
	}
	if !valid {
		return errors.Errorf("路由访问类型与安全链不一致: %s/%s", s.Meta.Access, s.Chain)
	}
	policy, policyDeclared := security.RouteSecurityPolicies[s.Meta.Alias]
	if s.Chain == RouteSecurityNone && !publicRouteAllowsNoSecurity(s.Meta.Alias) {
		return errors.Errorf("仅健康探针允许跳过安全链路 alias=%s", s.Meta.Alias)
	}
	if (s.Chain == RouteSecurityPublic || s.Chain == RouteSecurityAuth) && !policyDeclared {
		return errors.Errorf("前台路由必须声明安全策略 alias=%s", s.Meta.Alias)
	}
	if policyDeclared {
		if err := security.ValidateRoutePolicy(s.Meta.Alias, policy); err != nil {
			return errors.Tag(err)
		}
	}
	return nil
}

// publicRouteAllowsNoSecurity 只允许固定高频健康探针不进入签名、加密和登录态链路。
func publicRouteAllowsNoSecurity(alias routealias.Alias) bool {
	switch alias {
	case routealias.HealthLive, routealias.HealthReady, routealias.HealthMetrics:
		return true
	default:
		return false
	}
}

// AddRouteSpecs 按声明顺序转换并注册一组路由规格；任一规格无效时整组不写入 Server。
func AddRouteSpecs(server *rest.Server, svcCtx *svc.ServiceContext, authMw *middleware.AuthMiddleware, opsMw *middleware.OpsMiddleware, specs []RouteSpec) error {
	if server == nil {
		return errors.Errorf("注册路由规格时 HTTP Server 为空")
	}
	routes := make([]rest.Route, 0, len(specs))
	for _, spec := range specs {
		route, err := spec.RestRoute(svcCtx, authMw, opsMw)
		if err != nil {
			return errors.Tag(err)
		}
		routes = append(routes, route)
	}
	// 临时路由器先验证现有与新增路由的整体冲突，失败不会留下半注册状态。
	validator := router.NewRouter()
	patterns := make(map[string]string, len(server.Routes())+len(routes))
	for _, route := range append(server.Routes(), routes...) {
		pattern := routePatternKey(route.Method, route.Path)
		if previous, exists := patterns[pattern]; exists {
			return errors.Errorf("路由参数模式冲突 method=%s paths=%s,%s", route.Method, previous, route.Path)
		}
		patterns[pattern] = route.Path
		if err := validator.Handle(route.Method, route.Path, route.Handler); err != nil {
			return errors.Wrapf(err, "路由注册冲突 method=%s path=%s", route.Method, route.Path)
		}
	}
	server.AddRoutes(routes)
	return nil
}

// routePatternKey 抹平动态段名称，拒绝会由 go-zero map 遍历随机命中的等价路由。
func routePatternKey(method string, routePath string) string {
	segments := strings.Split(routePath, "/")
	for index, segment := range segments {
		if strings.HasPrefix(segment, ":") {
			segments[index] = ":"
		}
	}
	return method + " " + strings.Join(segments, "/")
}
