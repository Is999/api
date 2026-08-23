package user

import (
	"net/http"

	"api/internal/handler/shared"
	authlogic "api/internal/logic/auth"
	userlogic "api/internal/logic/user"
	"api/internal/svc"
	"api/internal/types"
)

// UserProfileHandler 只读取鉴权用户的资料，请求参数不能改查其它用户。
func UserProfileHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		l := userlogic.NewUserLogic(r.Context(), svcCtx)
		shared.WriteBizResponse(w, r, l.Profile())
	}
}

// UserRuntimeSyncHandler 接收后台提交后的运行态通知，目标用户只取已校验路径参数。
func UserRuntimeSyncHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return shared.RespHandler[types.UserRuntimeSyncReq](
		func(r *http.Request, svcCtx *svc.ServiceContext, req *types.UserRuntimeSyncReq) *types.BizResult {
			return authlogic.NewAuthLogic(r.Context(), svcCtx).SyncUserRuntime(req)
		},
	)(svcCtx)
}
