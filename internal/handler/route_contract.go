package handler

import "api/internal/handler/shared"

// RouteContract 描述一条内置 HTTP 路由契约。
type RouteContract struct {
	Method        string           // HTTP 方法
	Path          string           // HTTP 路径
	Meta          shared.RouteMeta // 路由元数据
	DocumentPath  string           // 仓库根目录下的接口文档路径
	InternalOnly  bool             // 是否只在内网监听器注册
	SkipAccessLog bool             // 是否跳过普通访问日志
}

// DefaultRouteContracts 返回内置 HTTP 路由契约集合。
func DefaultRouteContracts() []RouteContract {
	specs := DefaultRouteSpecs()
	contracts := make([]RouteContract, 0, len(specs))
	for _, spec := range specs {
		contracts = append(contracts, RouteContract{
			Method:        spec.Method,
			Path:          spec.Path,
			Meta:          spec.Meta,
			DocumentPath:  spec.DocumentPath,
			InternalOnly:  spec.InternalOnly,
			SkipAccessLog: spec.SkipAccessLog,
		})
	}
	return contracts
}
