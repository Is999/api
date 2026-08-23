package config

import (
	"net/http"

	"api/internal/handler/shared"
)

// RouteSpecs 返回内网配置热加载路由规格。
func RouteSpecs() []shared.RouteSpec {
	return []shared.RouteSpec{
		{
			Method:       http.MethodGet,
			Path:         "/internal/system/config-reload/status", // 内网查询配置热加载状态。
			Meta:         shared.SystemConfigReloadStatus,
			DocumentPath: shared.RouteDocSystem,
			Chain:        shared.RouteSecurityInternal,
			Handler:      ConfigReloadStatusHandler,
		},
		{
			Method:       http.MethodGet,
			Path:         "/internal/system/config-reload/items", // 内网查询运行态配置项。
			Meta:         shared.SystemConfigReloadItems,
			DocumentPath: shared.RouteDocSystem,
			Chain:        shared.RouteSecurityInternal,
			Handler:      ConfigReloadItemsHandler,
		},
		{
			Method:       http.MethodPost,
			Path:         "/internal/system/config-reload/run", // 内网手动触发配置热加载。
			Meta:         shared.SystemConfigReloadRun,
			DocumentPath: shared.RouteDocSystem,
			Chain:        shared.RouteSecurityInternal,
			Handler:      RunConfigReloadHandler,
		},
	}
}
