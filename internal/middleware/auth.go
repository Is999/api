package middleware

import (
	"context"
	"net/http"
	"strings"

	codes "api/common/codes"
	i18n "api/common/i18n"
	"api/internal/httpresp"
	"api/internal/infra/collectorx"
	"api/internal/infra/loggerx"
	"api/internal/requestctx"
	"api/internal/routealias"
	"api/internal/svc"

	"github.com/Is999/go-utils/errors"
)

// AuthMiddleware 负责 JWT、Redis session 鉴权以及请求元数据补全。
type AuthMiddleware struct {
	svc       *svc.ServiceContext  // 鉴权依赖的服务上下文
	runtime   Runtime              // 用户主库查询和风控事件由 handler 装配层提供
	crypto    *CryptoMiddleware    // 请求解密与响应加密中间件
	signature *SignatureMiddleware // 请求验签与响应签名中间件
}

// NewAuthMiddleware 创建鉴权中间件实例。
func NewAuthMiddleware(svcCtx *svc.ServiceContext, runtime Runtime) *AuthMiddleware {
	return &AuthMiddleware{
		svc:       svcCtx,
		runtime:   runtime,
		crypto:    NewCryptoMiddleware(svcCtx, runtime),
		signature: NewSignatureMiddleware(svcCtx, runtime),
	}
}

// PublicHandle 为未登录接口挂载加密与签名中间件，但不执行 JWT 鉴权。
func (m *AuthMiddleware) PublicHandle(next http.HandlerFunc, alias routealias.Alias) http.HandlerFunc {
	handler := next
	// 响应先在内层加密、再由外层回签；请求则先验签密文、再解密，不向未验签输入暴露解密器。
	if m.crypto != nil {
		handler = m.crypto.Handle(handler)
	}
	if m.signature != nil {
		handler = m.signature.Handle(handler, alias)
	}
	return func(w http.ResponseWriter, r *http.Request) {
		handler(w, bindRequestMeta(r, alias, m.svc))
	}
}

// Handle 负责鉴权并补齐当前请求的用户信息。
func (m *AuthMiddleware) Handle(next http.HandlerFunc, alias routealias.Alias) http.HandlerFunc {
	handler := func(w http.ResponseWriter, r *http.Request) {
		ctx, _ := requestctx.New(r.Context())
		clientIP := requestClientIP(m.svc, r)
		requestctx.SetRequest(ctx, r.Method, r.URL.Path, clientIP)
		if alias != "" && alias != routealias.Ignore {
			requestctx.SetRoute(ctx, string(alias))
		}

		failUnauthorized := func(code int, messageKey string, err error, reason string, identity *UserTokenIdentity) {
			m.emitAuthFailureEvent(ctx, reason, identity)
			resp := httpresp.NewJSONResp(ctx, w).
				SetHTTPStatus(http.StatusUnauthorized).
				SetCode(code)
			if err != nil {
				resp = resp.SetError(err)
			}
			resp.Fail(messageKey)
		}
		// failServerError 不投递伪造的 token 风控事件，依赖故障由统一错误日志和指标负责观测。
		failServerError := func(code int, messageKey string, err error) {
			httpresp.NewJSONResp(ctx, w).
				SetCode(code).
				SetError(err).
				Fail(messageKey)
		}

		identity, err := VerifyUserTokenFromRequest(ctx, m.svc, r, true)
		switch {
		case errors.Is(err, errMissingBearerToken):
			failUnauthorized(codes.Unauthorized, i18n.MsgKeyUnauthorizedText, err, collectorx.AuthSecurityReasonMissingBearer, nil)
			return
		case errors.Is(err, errTokenExpired):
			failUnauthorized(codes.TokenExpired, i18n.MsgKeyTokenExpired, err, collectorx.AuthSecurityReasonTokenExpired, identity)
			return
		case errors.Is(err, errSessionExpired):
			failUnauthorized(codes.SessionExpired, i18n.MsgKeySessionExpired, err, collectorx.AuthSecurityReasonSessionExpired, identity)
			return
		case errors.Is(err, errAuthDependencyUnavailable):
			failServerError(codes.ServiceBusy, i18n.MsgKeyServiceBusy, err)
			return
		case err != nil:
			failUnauthorized(codes.TokenInvalid, i18n.MsgKeyTokenInvalid, err, collectorx.AuthSecurityReasonTokenInvalid, identity)
			return
		}

		// 主库状态和认证版本是最终撤销边界，Redis 会话只负责快速拒绝和原子生命周期。
		runtime, runtimeErr := requireRuntime(m.runtime)
		if runtimeErr != nil {
			failServerError(codes.InternalError, i18n.MsgKeyInternalError, runtimeErr)
			return
		}
		user, err := runtime.ActiveUser(ctx, identity.UserID)
		if err != nil {
			switch {
			case errors.Is(err, ErrRuntimeUserDisabled):
				failUnauthorized(codes.UserDisabled, i18n.MsgKeyUserDisabled, err, collectorx.AuthSecurityReasonUserDisabled, identity)
				return
			case errors.Is(err, ErrRuntimeUserNotFound):
				failUnauthorized(codes.TokenInvalid, i18n.MsgKeyTokenInvalid, err, collectorx.AuthSecurityReasonUserNotFound, identity)
				return
			case errors.Is(err, ErrRuntimeDependencyUnavailable):
				failServerError(codes.ServiceBusy, i18n.MsgKeyServiceBusy, err)
				return
			default:
				failServerError(codes.InternalError, i18n.MsgKeyInternalError, err)
				return
			}
		}
		if !authVersionMatches(user, identity) {
			failUnauthorized(codes.SessionExpired, i18n.MsgKeySessionExpired, errSessionExpired, collectorx.AuthSecurityReasonSessionExpired, identity)
			return
		}

		// 固定主库已确认的脱敏快照，刷新逻辑复用本次查询结果。
		requestctx.SetAuthUser(ctx, user)
		requestctx.SetAccessToken(ctx, identity.Token)
		requestctx.SetSessionID(ctx, identity.SessionID)
		requestctx.SetUser(ctx, identity.UserID, user.Profile.Username, clientIP)
		ctx = loggerx.BindContext(ctx)
		next(w, r.WithContext(ctx))
	}
	return m.PublicHandle(handler, alias)
}

// authVersionMatches 校验主库快照的用户 ID 和认证版本均与 JWT 一致。
func authVersionMatches(user *requestctx.AuthUser, identity *UserTokenIdentity) bool {
	return user != nil && identity != nil && user.Profile.ID == identity.UserID && user.AuthVersion > 0 && user.AuthVersion == identity.AuthVersion
}

// emitAuthFailureEvent 投递登录态鉴权失败事件，Collector 不可用时不影响响应。
func (m *AuthMiddleware) emitAuthFailureEvent(ctx context.Context, reason string, identity *UserTokenIdentity) {
	if m == nil || m.runtime == nil {
		return
	}
	input := RuntimeAuthEvent{
		Action: collectorx.AuthSecurityActionAuthFailed,
		Reason: reason,
	}
	if identity != nil {
		input.UserID = identity.UserID
		input.Identity = "username:" + strings.ToLower(strings.TrimSpace(identity.UserName))
		input.SessionID = identity.SessionID
	}
	m.runtime.RecordAuthEvent(ctx, input)
}

// bindRequestMeta 为公开路由补齐请求元数据和稳定路由别名。
func bindRequestMeta(r *http.Request, alias routealias.Alias, svcCtx *svc.ServiceContext) *http.Request {
	if r == nil {
		return r
	}
	ctx, _ := requestctx.New(r.Context())
	requestctx.SetRequest(ctx, r.Method, r.URL.Path, requestClientIP(svcCtx, r))
	if alias != "" && alias != routealias.Ignore {
		requestctx.SetRoute(ctx, string(alias))
	}
	return r.WithContext(ctx)
}
