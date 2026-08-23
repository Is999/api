package shared

import (
	"testing"

	"api/internal/security"
)

// TestDefaultRouteMetasValid 确保内置路由元数据字段完整且别名唯一。
func TestDefaultRouteMetasValid(t *testing.T) {
	metas := DefaultRouteMetas()
	if len(metas) == 0 {
		t.Fatal("DefaultRouteMetas() must not be empty")
	}
	seen := make(map[string]struct{}, len(metas))
	for _, meta := range metas {
		if meta.Alias == "" {
			t.Fatalf("route meta has empty alias: %+v", meta)
		}
		if meta.Describe == "" {
			t.Fatalf("route meta has empty describe: %+v", meta)
		}
		if !validRouteAccess(meta.Access) {
			t.Fatalf("route meta has invalid access: %+v", meta)
		}
		alias := string(meta.Alias)
		if _, ok := seen[alias]; ok {
			t.Fatalf("duplicate route alias: %s", alias)
		}
		seen[alias] = struct{}{}
	}
}

// TestRouteMetaAccessBoundaries 确保关键路由访问边界不漂移。
func TestRouteMetaAccessBoundaries(t *testing.T) {
	want := map[string]RouteAccess{
		string(HealthLive.Alias):               RouteAccessPublic,
		string(HealthReady.Alias):              RouteAccessPublic,
		string(HealthMetrics.Alias):            RouteAccessPublic,
		string(AuthRegister.Alias):             RouteAccessPublic,
		string(AuthLogin.Alias):                RouteAccessPublic,
		string(AuthRefresh.Alias):              RouteAccessAuth,
		string(AuthLogout.Alias):               RouteAccessAuth,
		string(UserProfile.Alias):              RouteAccessAuth,
		string(SystemConfigReloadStatus.Alias): RouteAccessInternal,
		string(SystemConfigReloadItems.Alias):  RouteAccessInternal,
		string(SystemConfigReloadRun.Alias):    RouteAccessInternal,
		string(SystemDocsRootFile.Alias):       RouteAccessInternal,
		string(SystemDocsFile.Alias):           RouteAccessInternal,
		string(SystemDocsNestedFile.Alias):     RouteAccessInternal,
	}
	got := routeMetaAccessByAlias()
	for alias, access := range want {
		if got[alias] != access {
			t.Fatalf("route %s access = %s, want %s", alias, got[alias], access)
		}
	}
}

// TestRouteSecurityPoliciesUseKnownAliases 确保安全策略只绑定已声明的路由别名。
func TestRouteSecurityPoliciesUseKnownAliases(t *testing.T) {
	known := routeMetaAccessByAlias()
	for alias := range security.RouteSecurityPolicies {
		if _, ok := known[string(alias)]; !ok {
			t.Fatalf("security policy route alias missing from RouteMeta: %s", alias)
		}
	}
}

// validRouteAccess 校验访问类型只能是公开、登录态或内网入口，空值不能隐式放行。
func validRouteAccess(access RouteAccess) bool {
	switch access {
	case RouteAccessPublic, RouteAccessAuth, RouteAccessInternal:
		return true
	default:
		return false
	}
}

// routeMetaAccessByAlias 按别名索引内置访问策略，供路由表与安全策略交叉核对。
func routeMetaAccessByAlias() map[string]RouteAccess {
	result := make(map[string]RouteAccess, len(DefaultRouteMetas()))
	for _, meta := range DefaultRouteMetas() {
		result[string(meta.Alias)] = meta.Access
	}
	return result
}
