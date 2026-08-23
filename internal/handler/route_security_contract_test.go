package handler

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"api/internal/config"
	"api/internal/handler/shared"
	"api/internal/routealias"
	"api/internal/security"
	"api/internal/svc"

	"github.com/zeromicro/go-zero/rest"
)

// TestRouteSecurityContractsCoverRouteContracts 确保每个内置路由别名都有安全链路契约。
func TestRouteSecurityContractsCoverRouteContracts(t *testing.T) {
	securityByAlias := routeSecurityContractByAlias()
	contractAliases := make(map[string]struct{}, len(DefaultRouteContracts()))
	for _, contract := range DefaultRouteContracts() {
		alias := string(contract.Meta.Alias)
		contractAliases[alias] = struct{}{}
		if _, ok := securityByAlias[alias]; !ok {
			t.Fatalf("route security contract missing alias=%s", alias)
		}
	}
	for alias := range securityByAlias {
		if _, ok := contractAliases[alias]; !ok {
			t.Fatalf("route security contract has extra alias=%s", alias)
		}
	}
}

// TestRouteSecurityContractsFollowAccessBoundary 确保访问边界和安全链路匹配。
func TestRouteSecurityContractsFollowAccessBoundary(t *testing.T) {
	accessByAlias := routeMetaAccessByAlias()
	for _, contract := range DefaultRouteSecurityContracts() {
		access, ok := accessByAlias[string(contract.Alias)]
		if !ok {
			t.Fatalf("route security contract alias missing from route meta: %s", contract.Alias)
		}
		switch access {
		case shared.RouteAccessPublic:
			if contract.Chain != shared.RouteSecurityNone && contract.Chain != shared.RouteSecurityPublic {
				t.Fatalf("public route %s chain = %s", contract.Alias, contract.Chain)
			}
		case shared.RouteAccessAuth:
			if contract.Chain != shared.RouteSecurityAuth {
				t.Fatalf("auth route %s chain = %s", contract.Alias, contract.Chain)
			}
		case shared.RouteAccessInternal:
			if contract.Chain != shared.RouteSecurityInternal {
				t.Fatalf("internal route %s chain = %s", contract.Alias, contract.Chain)
			}
		default:
			t.Fatalf("unknown route access alias=%s access=%s", contract.Alias, access)
		}
	}
}

// TestRouteSecurityPoliciesMatchSecurityContracts 确保前台安全策略不误套到无安全链或内网路由。
func TestRouteSecurityPoliciesMatchSecurityContracts(t *testing.T) {
	securityByAlias := routeSecurityContractByAlias()
	for _, contract := range DefaultRouteSecurityContracts() {
		policy, _ := security.LookupRoutePolicy(string(contract.Alias))
		switch contract.Chain {
		case shared.RouteSecurityNone:
			if _, ok := security.RouteSecurityPolicies[routealias.Alias(contract.Alias)]; ok {
				t.Fatalf("no-security route must not define frontend security policy: %s", contract.Alias)
			}
		case shared.RouteSecurityInternal:
			if policy.RequestSign != nil || policy.ResponseSign != nil || len(policy.RequestCipher) != 0 || len(policy.ResponseCipher) != 0 {
				t.Fatalf("internal route must skip frontend sign/cipher policy: %s %+v", contract.Alias, policy)
			}
		}
	}
	for alias := range security.RouteSecurityPolicies {
		if _, ok := securityByAlias[string(alias)]; !ok {
			t.Fatalf("frontend security policy missing route security contract: %s", alias)
		}
	}
}

// TestRouteSecurityPoliciesUseFieldLevelSecurity 确保安全策略只声明关键字段，不做全量请求/响应处理。
func TestRouteSecurityPoliciesUseFieldLevelSecurity(t *testing.T) {
	for alias, policy := range security.RouteSecurityPolicies {
		if hasSecurityField(policy.RequestSign, security.SignFieldAll) {
			t.Fatalf("route %s request sign must not use *", alias)
		}
		if hasSecurityField(policy.RequestCipher, security.CipherWholeBody) {
			t.Fatalf("route %s request cipher must not use cipher", alias)
		}
		if hasSecurityField(policy.ResponseSign, security.SignFieldAll) {
			t.Fatalf("route %s response sign must not use *", alias)
		}
		if hasSecurityField(policy.ResponseCipher, security.CipherWholeBody) {
			t.Fatalf("route %s response cipher must not use cipher", alias)
		}
		// API 响应先加密后签名，每个密文字段都必须被签名覆盖。
		for _, field := range policy.ResponseCipher {
			if !hasSecurityField(policy.ResponseSign, field) {
				t.Fatalf("route %s response cipher field %s must be covered by response sign", alias, field)
			}
		}
		for label, fields := range map[string][]string{
			"request sign":    policy.RequestSign,
			"request cipher":  policy.RequestCipher,
			"response sign":   policy.ResponseSign,
			"response cipher": policy.ResponseCipher,
		} {
			if err := security.ValidateSecurityFieldCount(fields, label); err != nil {
				t.Fatalf("route %s %s fields invalid: %v", alias, label, err)
			}
		}
	}
}

// TestPublicAndAuthRoutesDeclareSecurityPolicy 确保前台路由显式声明字段级安全策略。
func TestPublicAndAuthRoutesDeclareSecurityPolicy(t *testing.T) {
	for _, contract := range DefaultRouteSecurityContracts() {
		switch contract.Chain {
		case shared.RouteSecurityPublic, shared.RouteSecurityAuth:
			if _, ok := security.RouteSecurityPolicies[routealias.Alias(contract.Alias)]; !ok {
				t.Fatalf("frontend route must declare explicit security policy: %s", contract.Alias)
			}
		}
	}
}

// TestRouteNoTokenBehaviorMatchesSecurityContracts 通过真实 handler 验证未登录访问边界。
func TestRouteNoTokenBehaviorMatchesSecurityContracts(t *testing.T) {
	// 从两个 Server 的真实注册结果取处理器；不启动监听器，也不执行 Server 全局中间件。
	publicServer := rest.MustNewServer(rest.RestConf{Host: "127.0.0.1", Port: 0})
	defer publicServer.Stop()
	internalServer := rest.MustNewServer(rest.RestConf{Host: "127.0.0.1", Port: 0})
	defer internalServer.Stop()

	svcCtx := svc.NewServiceContext(config.Config{JwtSecret: "test-secret-please-change"}, "test-version", svc.Dependencies{})
	if err := RegisterPublicHandlers(publicServer, svcCtx); err != nil {
		t.Fatalf("注册公网路由失败: %v", err)
	}
	if err := RegisterInternalHandlers(internalServer, svcCtx); err != nil {
		t.Fatalf("注册内网路由失败: %v", err)
	}
	routeHandlers := routeHandlerByKey(append(publicServer.Routes(), internalServer.Routes()...))
	securityByAlias := routeSecurityContractByAlias()

	// 这里只断言凭证拒绝边界，公开业务因参数或依赖缺失返回失败不代表其业务链通过。
	for _, routeContract := range DefaultRouteContracts() {
		key := routeKey(routeContract.Method, routeContract.Path)
		handler, ok := routeHandlers[key]
		if !ok {
			t.Fatalf("registered route missing handler: %s", key)
		}
		securityContract := securityByAlias[string(routeContract.Meta.Alias)]
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(routeContract.Method, routeContract.Path, nil)
		handler(rec, req)

		switch securityContract.Chain {
		case shared.RouteSecurityNone, shared.RouteSecurityPublic:
			if rec.Code == http.StatusUnauthorized {
				t.Fatalf("route %s should not require token, got 401", key)
			}
		case shared.RouteSecurityAuth:
			if rec.Code != http.StatusUnauthorized {
				t.Fatalf("route %s should require token, status=%d", key, rec.Code)
			}
		case shared.RouteSecurityInternal:
			if rec.Code != http.StatusForbidden {
				t.Fatalf("route %s should require ops token, status=%d", key, rec.Code)
			}
		default:
			t.Fatalf("unknown route security chain: %+v", securityContract)
		}
	}
}

// hasSecurityField 判断安全字段清单是否包含指定协议字段。
func hasSecurityField(fields []string, want string) bool {
	for _, field := range fields {
		if field == want {
			return true
		}
	}
	return false
}

// routeSecurityContractByAlias 按稳定别名索引安全契约，便于和路由元数据逐项对照。
func routeSecurityContractByAlias() map[string]RouteSecurityContract {
	result := make(map[string]RouteSecurityContract, len(DefaultRouteSecurityContracts()))
	for _, contract := range DefaultRouteSecurityContracts() {
		result[string(contract.Alias)] = contract
	}
	return result
}

// routeHandlerByKey 按 HTTP 方法和路径索引真实注册的处理器。
func routeHandlerByKey(routes []rest.Route) map[string]http.HandlerFunc {
	result := make(map[string]http.HandlerFunc, len(routes))
	for _, route := range routes {
		result[routeKey(route.Method, route.Path)] = route.Handler
	}
	return result
}
