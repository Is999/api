package handler

import "api/internal/handler/shared"

// routeMetaAccessByAlias 按稳定别名索引访问级别，供真实路由与安全链契约交叉比对。
func routeMetaAccessByAlias() map[string]shared.RouteAccess {
	result := make(map[string]shared.RouteAccess, len(shared.DefaultRouteMetas()))
	for _, meta := range shared.DefaultRouteMetas() {
		result[string(meta.Alias)] = meta.Access
	}
	return result
}
