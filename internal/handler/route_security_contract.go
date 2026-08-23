package handler

import (
	"api/internal/handler/shared"
	"api/internal/routealias"
)

// RouteSecurityContract 描述内置路由别名对应的安全链路契约。
type RouteSecurityContract struct {
	Alias routealias.Alias          // 路由别名
	Chain shared.RouteSecurityChain // 安全链路
}

// DefaultRouteSecurityContracts 返回内置路由安全链路契约集合。
func DefaultRouteSecurityContracts() []RouteSecurityContract {
	specs := DefaultRouteSpecs()
	contracts := make([]RouteSecurityContract, 0, len(specs))
	for _, spec := range specs {
		contracts = append(contracts, RouteSecurityContract{
			Alias: spec.Meta.Alias,
			Chain: spec.Chain,
		})
	}
	return contracts
}
