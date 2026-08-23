package auth

import (
	codes "api/common/codes"
	i18n "api/common/i18n"
	userlogic "api/internal/logic/user"
	"api/internal/model"
	"api/internal/svc"
	"api/internal/types"

	"github.com/Is999/go-utils/errors"
)

// SyncUserRuntime 同步后台直改业务用户表后必须由 API 自己维护的运行态缓存。
func (l *AuthLogic) SyncUserRuntime(req *types.UserRuntimeSyncReq) *types.BizResult {
	if err := req.Validate(); err != nil {
		return types.ParamErrorResult(err).
			WithError(err)
	}
	if req.Sessions {
		// 主库版本尚未提交时不得先删除资料缓存或推进 Redis 版本栅栏。
		if result := l.validateCommittedAuthVersion(req.ID, req.AuthVersion); result != nil {
			return result
		}
	}

	resp := &types.UserRuntimeSyncResp{
		UserID:                  req.ID,
		AuthVersion:             req.AuthVersion,
		Reason:                  req.Reason,
		ProfileCacheInvalidated: false,
		SessionsInvalidated:     false,
	}
	if req.Profile {
		// 资料失效使用精确 key，可由后台重复提交同步请求重试。
		if err := userlogic.NewUserLogic(l.Ctx, l.Svc).DeleteUserProfileCache(req.ID); err != nil {
			return types.ServerError(i18n.MsgKeyInternalError, err, "AuthLogic.SyncUserRuntime 删除用户资料缓存失败 user_id=%d", req.ID).ToBizResult()
		}
		resp.ProfileCacheInvalidated = true
	}
	if req.Sessions {
		// Lua 原子推进版本并清空会话，较旧的并发同步不能覆盖新版本。
		if err := l.InvalidateUserSessions(req.ID, req.AuthVersion); err != nil {
			return types.ServerError(i18n.MsgKeyInternalError, err, "AuthLogic.SyncUserRuntime 失效用户登录态失败 user_id=%d", req.ID).ToBizResult()
		}
		resp.SessionsInvalidated = true
	}
	return types.NewBizResult(codes.UpdateSuccess).
		SetI18nMessage(i18n.MsgKeyUpdateSuccess).
		WithData(resp)
}

// validateCommittedAuthVersion 在任何 Redis 副作用前确认后台提交的认证版本已进入主库。
func (l *AuthLogic) validateCommittedAuthVersion(userID int64, authVersion uint64) *types.BizResult {
	db := l.Svc.WriteDB(svc.DatabaseMain)
	if db == nil {
		return types.ServerError(i18n.MsgKeyInternalError, errors.New("业务用户主库未初始化"), "AuthLogic.SyncUserRuntime 校验用户认证版本失败 user_id=%d", userID).ToBizResult()
	}
	user, err := model.FindUserByID(db, userID, l.Svc.CurrentConfig().User.RouteShardCount)
	if err != nil {
		return types.ServerError(i18n.MsgKeyInternalError, err, "AuthLogic.SyncUserRuntime 校验用户认证版本失败 user_id=%d", userID).ToBizResult()
	}
	if user == nil || user.AuthVersion != authVersion {
		err = errors.Errorf("用户认证版本未提交或不一致 user_id=%d expected=%d actual=%d", userID, authVersion, authVersionOf(user))
		return types.ServerError(i18n.MsgKeyInternalError, err, "AuthLogic.SyncUserRuntime 拒绝未提交的用户认证版本 user_id=%d", userID).ToBizResult()
	}
	return nil
}

// authVersionOf 安全返回用户认证版本，用户不存在时返回零值。
func authVersionOf(user *model.User) uint64 {
	if user == nil {
		return 0
	}
	return user.AuthVersion
}
