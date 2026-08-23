package auth

import (
	"net/http"

	"api/internal/handler/shared"
	authlogic "api/internal/logic/auth"
	"api/internal/svc"
	"api/internal/types"
)

// RegisterHandler 使用统一参数解析和校验入口，注册前不要求已有登录态。
func RegisterHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return shared.RespHandler(func(r *http.Request, svcCtx *svc.ServiceContext, req *types.RegisterReq) *types.BizResult {
		return authlogic.NewAuthLogic(r.Context(), svcCtx).Register(req)
	})(svcCtx)
}

// LoginHandler 接收密码登录请求，失败响应沿用统一账号密码文案以防账号枚举。
func LoginHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return shared.RespHandler(func(r *http.Request, svcCtx *svc.ServiceContext, req *types.LoginReq) *types.BizResult {
		return authlogic.NewAuthLogic(r.Context(), svcCtx).Login(req)
	})(svcCtx)
}

// RefreshHandler 仅使用鉴权上下文中的当前会话，不从请求体接收替换 token。
func RefreshHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		l := authlogic.NewAuthLogic(r.Context(), svcCtx)
		shared.WriteBizResponse(w, r, l.Refresh())
	}
}

// LogoutHandler 退出鉴权上下文中的稳定 sid，不接受客户端另选注销对象。
func LogoutHandler(svcCtx *svc.ServiceContext) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		l := authlogic.NewAuthLogic(r.Context(), svcCtx)
		shared.WriteBizResponse(w, r, l.Logout())
	}
}
