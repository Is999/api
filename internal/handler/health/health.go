package health

import (
	"net/http"

	codes "api/common/codes"
	"api/internal/handler/shared"
	"api/internal/httpresp"
	"api/internal/infra/loggerx"
	healthlogic "api/internal/logic/health"
	"api/internal/requestctx"
	"api/internal/svc"
)

// LiveHandler 不访问外部依赖，避免数据库或 Redis 故障触发编排系统反复重启进程。
func LiveHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		requestctx.SetRoute(r.Context(), string(shared.HealthLive.Alias))
		resp := healthlogic.NewHealthLogic(r.Context(), svcCtx).Liveness()
		httpresp.NewJSONResp(r.Context(), w).SetCode(codes.OK).Success(resp)
	}
}

// ReadyHandler 将任一核心依赖失败转换为 503，供流量入口摘除未就绪实例。
func ReadyHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		requestctx.SetRoute(r.Context(), string(shared.HealthReady.Alias))
		resp, err := healthlogic.NewHealthLogic(r.Context(), svcCtx).Readiness(r.Context())
		if err != nil {
			httpresp.NewJSONResp(r.Context(), w).
				SetHTTPStatus(http.StatusServiceUnavailable).
				SetCode(codes.DependencyUnavailable).
				SetError(err).
				Fail("", resp)
			// Fail 先回填响应结果，摘要日志不能使用初始 HTTP 状态。
			loggerx.Errorw(r.Context(), "健康检查 依赖未就绪", err)
			return
		}
		httpresp.NewJSONResp(r.Context(), w).SetCode(codes.OK).Success(resp)
	}
}
