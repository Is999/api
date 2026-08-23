package health

import (
	"net/http"

	"api/internal/handler/shared"
	"api/internal/requestctx"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// MetricsHandler 提供原始 Prometheus 格式，访问隔离由内网监听器注册边界保证。
func MetricsHandler() http.HandlerFunc {
	handler := promhttp.Handler()
	return func(w http.ResponseWriter, r *http.Request) {
		requestctx.SetRoute(r.Context(), string(shared.HealthMetrics.Alias))
		handler.ServeHTTP(w, r)
	}
}
