package health

import (
	"net/http"

	"api/internal/handler/shared"
	"api/internal/svc"
)

// RouteSpecs 返回基础健康检查路由规格。
func RouteSpecs() []shared.RouteSpec {
	return []shared.RouteSpec{
		{
			Method:        http.MethodGet,
			Path:          "/api/live", // 存活检查，不访问外部依赖。
			Meta:          shared.HealthLive,
			DocumentPath:  shared.RouteDocHealth,
			Chain:         shared.RouteSecurityNone,
			SkipAccessLog: true,
			Handler:       LiveHandler,
		},
		{
			Method:        http.MethodGet,
			Path:          "/api/ready", // 就绪检查，探测 MySQL/Redis 等关键依赖。
			Meta:          shared.HealthReady,
			DocumentPath:  shared.RouteDocHealth,
			Chain:         shared.RouteSecurityNone,
			SkipAccessLog: true,
			Handler:       ReadyHandler,
		},
		{
			Method:        http.MethodGet,
			Path:          "/api/metrics", // Prometheus 指标抓取入口。
			Meta:          shared.HealthMetrics,
			DocumentPath:  shared.RouteDocHealth,
			Chain:         shared.RouteSecurityNone,
			InternalOnly:  true,
			SkipAccessLog: true,
			Handler: func(_ *svc.ServiceContext) http.HandlerFunc {
				return MetricsHandler()
			},
		},
	}
}
