package user

import (
	"net/http"

	"api/internal/handler/shared"
)

// RouteSpecs 返回前台用户路由规格。
func RouteSpecs() []shared.RouteSpec {
	return []shared.RouteSpec{
		{
			Method:       http.MethodGet,
			Path:         "/api/user/profile", // 获取当前用户资料，必须校验前台登录态。
			Meta:         shared.UserProfile,
			DocumentPath: shared.RouteDocUser,
			Chain:        shared.RouteSecurityAuth,
			Handler:      UserProfileHandler,
		},
		{
			Method:       http.MethodPost,
			Path:         "/internal/users/:id/runtime-sync", // 内网同步后台直改用户表后的 API 运行态缓存。
			Meta:         shared.UserRuntimeSync,
			DocumentPath: shared.RouteDocUser,
			Chain:        shared.RouteSecurityInternal,
			Handler:      UserRuntimeSyncHandler,
		},
	}
}
