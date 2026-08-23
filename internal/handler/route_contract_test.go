package handler

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"api/internal/config"
	"api/internal/handler/shared"
	"api/internal/routealias"
	"api/internal/security"
	"api/internal/svc"

	"github.com/zeromicro/go-zero/rest"
)

// TestBuiltinRouteModuleSpecsValid 确保内置路由模块规格字段完整且顺序可用于注册。
func TestBuiltinRouteModuleSpecsValid(t *testing.T) {
	specs := BuiltinRouteModuleSpecs()
	if len(specs) == 0 {
		t.Fatal("builtin route module specs must not be empty")
	}
	seenNames := make(map[string]struct{}, len(specs))
	for _, spec := range specs {
		if strings.TrimSpace(spec.Name) == "" || spec.File == "" || spec.Method == "" || spec.Description == "" {
			t.Fatalf("route module spec has empty field: %+v", spec)
		}
		if !strings.HasPrefix(spec.File, "internal/handler/") || !strings.HasSuffix(spec.File, "/routes.go") || strings.Contains(spec.File, " + ") {
			t.Fatalf("route module spec file invalid: %+v", spec)
		}
		if spec.Routes == nil {
			t.Fatalf("route module spec routes missing: %+v", spec)
		}
		if len(spec.Routes()) == 0 {
			t.Fatalf("route module spec routes empty: %+v", spec)
		}
		if _, ok := seenNames[spec.Name]; ok {
			t.Fatalf("duplicate route module spec name: %s", spec.Name)
		}
		seenNames[spec.Name] = struct{}{}
	}
}

// TestDefaultRouteSpecsValid 确保路由规格作为单一来源时字段完整且路由不重复。
func TestDefaultRouteSpecsValid(t *testing.T) {
	seenRoutes := make(map[string]struct{}, len(DefaultRouteSpecs()))
	for _, spec := range DefaultRouteSpecs() {
		if spec.Method == "" || spec.Path == "" || spec.DocumentPath == "" {
			t.Fatalf("route spec has empty field: %+v", spec)
		}
		if spec.Meta.Alias == "" || spec.Meta.Access == "" || spec.Meta.Describe == "" {
			t.Fatalf("route spec meta incomplete: %+v", spec)
		}
		if spec.Chain == "" {
			t.Fatalf("route spec chain missing: %+v", spec)
		}
		if spec.Handler == nil {
			t.Fatalf("route spec handler missing: %+v", spec)
		}
		key := routeKey(spec.Method, spec.Path)
		if _, ok := seenRoutes[key]; ok {
			t.Fatalf("duplicate route spec: %s", key)
		}
		seenRoutes[key] = struct{}{}
	}
}

// TestDefaultRouteContractsMatchRegisteredRoutes 确保契约表与真实注册路由一致。
func TestDefaultRouteContractsMatchRegisteredRoutes(t *testing.T) {
	// 公网和内网分别注册，避免只检查静态规格而遗漏真实装配。
	publicServer := rest.MustNewServer(rest.RestConf{Host: "127.0.0.1", Port: 0})
	defer publicServer.Stop()
	internalServer := rest.MustNewServer(rest.RestConf{Host: "127.0.0.1", Port: 0})
	defer internalServer.Stop()

	svcCtx := svc.NewServiceContext(config.Config{}, "test-version", svc.Dependencies{})
	if err := RegisterPublicHandlers(publicServer, svcCtx); err != nil {
		t.Fatalf("注册公网路由失败: %v", err)
	}
	if err := RegisterInternalHandlers(internalServer, svcCtx); err != nil {
		t.Fatalf("注册内网路由失败: %v", err)
	}

	// 先合并真实路由并确认两个监听器不存在交叉注册。
	publicRoutes := routeSet(publicServer.Routes())
	internalRoutes := routeSet(internalServer.Routes())
	registered := make(map[string]struct{}, len(publicRoutes)+len(internalRoutes))
	for key := range publicRoutes {
		registered[key] = struct{}{}
		if strings.Contains(key, " /internal/") {
			t.Fatalf("public server registered internal route: %s", key)
		}
	}
	for key := range internalRoutes {
		if _, exists := registered[key]; exists {
			t.Fatalf("route registered on both servers: %s", key)
		}
		registered[key] = struct{}{}
	}
	// 每条契约必须在正确监听器中恰好出现一次。
	contracts := DefaultRouteContracts()
	if len(registered) != len(contracts) {
		t.Fatalf("registered route count = %d, contract count = %d", len(registered), len(contracts))
	}
	for _, contract := range contracts {
		key := routeKey(contract.Method, contract.Path)
		if _, ok := registered[key]; !ok {
			t.Fatalf("contract route is not registered: %s", key)
		}
		_, onPublic := publicRoutes[key]
		_, onInternal := internalRoutes[key]
		wantInternal := contract.Meta.Access == shared.RouteAccessInternal || contract.InternalOnly
		if onInternal != wantInternal || onPublic == wantInternal {
			t.Fatalf("route listener mismatch: %s public=%t internal=%t want_internal=%t", key, onPublic, onInternal, wantInternal)
		}
	}
}

// TestRegisterHandlersAppendsRouteModules 确保外部路由模块可以通过统一入口追加注册。
func TestRegisterHandlersAppendsRouteModules(t *testing.T) {
	// 临时安全策略与自定义模块共同模拟外部扩展入口。
	server := rest.MustNewServer(rest.RestConf{Host: "127.0.0.1", Port: 0})
	defer server.Stop()

	customAlias := routealias.Alias("custom.read")
	security.RouteSecurityPolicies[customAlias] = security.RouteSecurityPolicy{RequestSign: []string{}}
	defer delete(security.RouteSecurityPolicies, customAlias)
	module := NewRouteModuleFunc("custom", func() []shared.RouteSpec {
		return []shared.RouteSpec{{
			Method:       http.MethodGet,
			Path:         "/api/custom",
			DocumentPath: shared.RouteDocSystem,
			Meta: shared.RouteMeta{
				Alias:    customAlias,
				Access:   shared.RouteAccessPublic,
				Describe: "扩展路由测试",
			},
			Chain: shared.RouteSecurityPublic,
			Handler: func(*svc.ServiceContext) http.HandlerFunc {
				return func(w http.ResponseWriter, _ *http.Request) {
					w.WriteHeader(http.StatusNoContent)
				}
			},
		}}
	})
	// 通过公开注册入口追加后，从 go-zero 真实路由表验证结果。
	if err := RegisterPublicHandlers(server, svc.NewServiceContext(config.Config{}, "test-version", svc.Dependencies{}), module); err != nil {
		t.Fatalf("注册扩展路由失败: %v", err)
	}

	routeSet := make(map[string]struct{}, len(server.Routes()))
	for _, route := range server.Routes() {
		routeSet[route.Method+" "+route.Path] = struct{}{}
	}
	if _, ok := routeSet[http.MethodGet+" /api/custom"]; !ok {
		t.Fatal("期望外部路由模块已注册")
	}
}

// TestRegisterHandlersRejectsInvalidModules 确保扩展模块配置错误会拒绝启动而不是静默跳过。
func TestRegisterHandlersRejectsInvalidModules(t *testing.T) {
	tests := []struct {
		name    string        // name 表示错误注册场景。
		modules []RouteModule // modules 表示待注册模块。
	}{
		{name: "nil", modules: []RouteModule{nil}},
		{name: "non-canonical name", modules: []RouteModule{NewRouteModuleFunc(" custom ", func() []shared.RouteSpec { return []shared.RouteSpec{{}} })}},
		{name: "empty routes", modules: []RouteModule{NewRouteModuleFunc("custom", func() []shared.RouteSpec { return nil })}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := rest.MustNewServer(rest.RestConf{Host: "127.0.0.1", Port: 0})
			defer server.Stop()
			svcCtx := svc.NewServiceContext(config.Config{}, "test-version", svc.Dependencies{})
			if err := RegisterPublicHandlersWithModules(server, svcCtx, tt.modules...); err == nil {
				t.Fatal("expected invalid route module error")
			}
		})
	}
}

// TestRegisterHandlersRejectsDuplicateRoutesBeforeServerStart 确保扩展模块不能把重复路由留到监听阶段触发 panic。
func TestRegisterHandlersRejectsDuplicateRoutesBeforeServerStart(t *testing.T) {
	server := rest.MustNewServer(rest.RestConf{Host: "127.0.0.1", Port: 0})
	defer server.Stop()
	duplicate := func(name string) RouteModule {
		return NewRouteModuleFunc(name, func() []shared.RouteSpec {
			return []shared.RouteSpec{{
				Method:       http.MethodGet,
				Path:         "/api/live",
				Meta:         shared.HealthLive,
				DocumentPath: shared.RouteDocHealth,
				Chain:        shared.RouteSecurityNone,
				Handler:      func(*svc.ServiceContext) http.HandlerFunc { return func(http.ResponseWriter, *http.Request) {} },
			}}
		})
	}
	err := RegisterPublicHandlersWithModules(server, svc.NewServiceContext(config.Config{}, "test-version", svc.Dependencies{}), duplicate("first"), duplicate("second"))
	if err == nil || !strings.Contains(err.Error(), "路由重复") {
		t.Fatalf("期望重复路由在启动前返回错误，实际 err=%v", err)
	}
	if len(server.Routes()) != 0 {
		t.Fatalf("校验失败前不应写入任何路由，实际=%+v", server.Routes())
	}
}

// TestCustomRouteModuleIsPartitionedBySecurityChain 确保扩展模块也不能把内网路由注册到公网监听器。
func TestCustomRouteModuleIsPartitionedBySecurityChain(t *testing.T) {
	publicServer := rest.MustNewServer(rest.RestConf{Host: "127.0.0.1", Port: 0})
	defer publicServer.Stop()
	internalServer := rest.MustNewServer(rest.RestConf{Host: "127.0.0.1", Port: 0})
	defer internalServer.Stop()
	module := NewRouteModuleFunc("custom-internal", func() []shared.RouteSpec {
		return []shared.RouteSpec{{
			Method:       http.MethodPost,
			Path:         "/internal/custom",
			DocumentPath: shared.RouteDocSystem,
			Meta: shared.RouteMeta{
				Alias:    routealias.Alias("custom.internal"),
				Access:   shared.RouteAccessInternal,
				Describe: "扩展内网路由测试",
			},
			Chain:   shared.RouteSecurityInternal,
			Handler: func(*svc.ServiceContext) http.HandlerFunc { return func(http.ResponseWriter, *http.Request) {} },
		}}
	})
	svcCtx := svc.NewServiceContext(config.Config{}, "test-version", svc.Dependencies{})
	if err := RegisterPublicHandlersWithModules(publicServer, svcCtx, module); err != nil {
		t.Fatalf("注册公网扩展路由失败: %v", err)
	}
	if err := RegisterInternalHandlersWithModules(internalServer, svcCtx, module); err != nil {
		t.Fatalf("注册内网扩展路由失败: %v", err)
	}
	if _, ok := routeSet(publicServer.Routes())[http.MethodPost+" /internal/custom"]; ok {
		t.Fatal("扩展内网路由不能注册到公网监听器")
	}
	if _, ok := routeSet(internalServer.Routes())[http.MethodPost+" /internal/custom"]; !ok {
		t.Fatal("扩展内网路由应注册到内网监听器")
	}
}

// TestDefaultRouteContractsValid 确保契约字段完整且访问边界与路径前缀匹配。
func TestDefaultRouteContractsValid(t *testing.T) {
	seenRoutes := make(map[string]struct{}, len(DefaultRouteContracts()))
	knownMetas := routeMetaAccessByAlias()
	for _, contract := range DefaultRouteContracts() {
		if contract.Method == "" || contract.Path == "" || contract.DocumentPath == "" {
			t.Fatalf("route contract has empty field: %+v", contract)
		}
		key := routeKey(contract.Method, contract.Path)
		if _, ok := seenRoutes[key]; ok {
			t.Fatalf("duplicate route contract: %s", key)
		}
		seenRoutes[key] = struct{}{}

		access, ok := knownMetas[string(contract.Meta.Alias)]
		if !ok {
			t.Fatalf("route contract meta alias missing from DefaultRouteMetas: %+v", contract)
		}
		if access != contract.Meta.Access {
			t.Fatalf("route contract access mismatch: %+v", contract)
		}
		if contract.Meta.Access == shared.RouteAccessInternal && !strings.HasPrefix(contract.Path, "/internal/") {
			t.Fatalf("internal route must use /internal/ prefix: %+v", contract)
		}
		if contract.Meta.Access != shared.RouteAccessInternal && strings.HasPrefix(contract.Path, "/internal/") {
			t.Fatalf("non-internal route must not use /internal/ prefix: %+v", contract)
		}
		if contract.InternalOnly && contract.Meta.Alias != shared.HealthMetrics.Alias {
			t.Fatalf("非指标路由不得绕过访问级别声明为仅内网: %+v", contract)
		}
	}
}

// TestMetricsRouteIsInternalListenerOnly 验证指标无需应用层凭证，但不会暴露在公网监听器。
func TestMetricsRouteIsInternalListenerOnly(t *testing.T) {
	key := routeKey(http.MethodGet, "/api/metrics")
	for _, spec := range DefaultRouteSpecs() {
		if routeKey(spec.Method, spec.Path) != key {
			continue
		}
		if !spec.InternalOnly || spec.Chain != shared.RouteSecurityNone {
			t.Fatalf("metrics 路由边界错误: %+v", spec)
		}
		return
	}
	t.Fatal("缺少 metrics 路由")
}

// TestDefaultRouteContractsSkipAccessLog 确保只有健康探针路由会跳过普通访问日志。
func TestDefaultRouteContractsSkipAccessLog(t *testing.T) {
	healthAliases := map[string]struct{}{
		string(shared.HealthLive.Alias):    {},
		string(shared.HealthReady.Alias):   {},
		string(shared.HealthMetrics.Alias): {},
	}
	seenHealth := make(map[string]struct{}, len(healthAliases))
	for _, contract := range DefaultRouteContracts() {
		alias := string(contract.Meta.Alias)
		_, isHealth := healthAliases[alias]
		if contract.SkipAccessLog != isHealth {
			t.Fatalf("route %s skip_access_log = %v, want %v", alias, contract.SkipAccessLog, isHealth)
		}
		if isHealth {
			seenHealth[alias] = struct{}{}
		}
	}
	if len(seenHealth) != len(healthAliases) {
		t.Fatalf("健康探针路由覆盖不完整: got=%v want=%v", seenHealth, healthAliases)
	}
}

// TestRouteContractDocumentsContainPath 确保接口文档包含契约表中的真实路径。
func TestRouteContractDocumentsContainPath(t *testing.T) {
	for _, contract := range DefaultRouteContracts() {
		documentPath := filepath.Join("..", "..", contract.DocumentPath)
		body, err := os.ReadFile(documentPath)
		if err != nil {
			t.Fatalf("read route document %s: %v", contract.DocumentPath, err)
		}
		if !strings.Contains(string(body), contract.Path) {
			t.Fatalf("document %s does not contain route path %s", contract.DocumentPath, contract.Path)
		}
	}
}

// TestRouteSecurityPoliciesMatchDocuments 确保接口文档安全字段和路由安全策略一致。
func TestRouteSecurityPoliciesMatchDocuments(t *testing.T) {
	for _, contract := range DefaultRouteContracts() {
		documentPath := filepath.Join("..", "..", contract.DocumentPath)
		body, err := os.ReadFile(documentPath)
		if err != nil {
			t.Fatalf("read route document %s: %v", contract.DocumentPath, err)
		}
		section, ok := routeDocumentSection(string(body), routeKey(contract.Method, contract.Path))
		if !ok {
			t.Fatalf("document %s missing route section %s", contract.DocumentPath, routeKey(contract.Method, contract.Path))
		}
		policy, _ := security.LookupRoutePolicy(string(contract.Meta.Alias))
		rows := routeSecurityDocumentRows(policy)
		for label, value := range rows {
			row := "| " + label + " | " + value + " |"
			if !strings.Contains(section, row) {
				t.Fatalf("document %s route %s missing security row %q", contract.DocumentPath, routeKey(contract.Method, contract.Path), row)
			}
		}
	}
}

// TestRouteSecurityPoliciesUseContractAliases 确保安全策略只绑定契约表内路由。
func TestRouteSecurityPoliciesUseContractAliases(t *testing.T) {
	aliases := make(map[string]struct{}, len(DefaultRouteContracts()))
	for _, contract := range DefaultRouteContracts() {
		aliases[string(contract.Meta.Alias)] = struct{}{}
	}
	for alias := range security.RouteSecurityPolicies {
		if _, ok := aliases[string(alias)]; !ok {
			t.Fatalf("security policy route alias missing from route contracts: %s", alias)
		}
	}
}

// routeSet 返回按 HTTP 方法和路径去重后的注册路由集合。
func routeSet(routes []rest.Route) map[string]struct{} {
	result := make(map[string]struct{}, len(routes))
	for _, route := range routes {
		key := routeKey(route.Method, route.Path)
		if _, ok := result[key]; ok {
			continue
		}
		result[key] = struct{}{}
	}
	return result
}

// routeDocumentSection 截取指定路由标识所在的二级文档章节。
func routeDocumentSection(document string, key string) (string, bool) {
	lines := strings.Split(document, "\n")
	start := -1
	marker := "`" + key + "`"
	for index, line := range lines {
		if strings.TrimSpace(line) == marker {
			start = index
			break
		}
	}
	if start < 0 {
		return "", false
	}
	end := len(lines)
	for index := start + 1; index < len(lines); index++ {
		line := strings.TrimSpace(lines[index])
		if strings.HasPrefix(line, "## ") && !strings.HasPrefix(line, "### ") {
			end = index
			break
		}
	}
	return strings.Join(lines[start:end], "\n"), true
}

// routeSecurityDocumentRows 生成接口文档必须声明的四项安全字段。
func routeSecurityDocumentRows(policy security.RouteSecurityPolicy) map[string]string {
	return map[string]string{
		"请求签名字段": signDocumentFieldValue(policy.RequestSign),
		"请求加密字段": securityDocumentFieldValue(policy.RequestCipher, "不参与加密"),
		"响应签名字段": signDocumentFieldValue(policy.ResponseSign),
		"响应加密字段": securityDocumentFieldValue(policy.ResponseCipher, "不参与加密"),
	}
}

// signDocumentFieldValue 区分关闭签名与只签基础头的文档值。
func signDocumentFieldValue(fields []string) string {
	if fields == nil {
		return "不参与签名"
	}
	if len(fields) == 0 {
		return "无业务字段，仅签 `appID`、`traceID`、`timestamp`"
	}
	return strings.Join(fields, ", ")
}

// securityDocumentFieldValue 将空字段列表映射为指定的关闭文案。
func securityDocumentFieldValue(fields []string, empty string) string {
	if len(fields) == 0 {
		return empty
	}
	return strings.Join(fields, ", ")
}

// routeKey 生成方法和路径经过规范化的路由标识。
func routeKey(method, path string) string {
	return strings.ToUpper(strings.TrimSpace(method)) + " " + strings.TrimSpace(path)
}
