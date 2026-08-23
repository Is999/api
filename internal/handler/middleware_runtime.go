package handler

import (
	"context"
	stderrors "errors"

	authlogic "api/internal/logic/auth"
	userlogic "api/internal/logic/user"
	"api/internal/middleware"
	"api/internal/requestctx"
	"api/internal/security"
	"api/internal/svc"

	"github.com/Is999/go-utils/errors"
)

// middlewareRuntime 把请求基础设施需要的窄能力适配到现有业务逻辑。
type middlewareRuntime struct {
	svc *svc.ServiceContext // svc 由路由启动装配传入，生命周期与 HTTP Server 一致
}

// newMiddlewareRuntime 创建 HTTP 中间件唯一的业务能力适配器。
func newMiddlewareRuntime(svcCtx *svc.ServiceContext) middleware.Runtime {
	return &middlewareRuntime{svc: svcCtx}
}

// ActiveUser 从主库读取鉴权快照，并把业务错误映射为中间件稳定分类。
func (r *middlewareRuntime) ActiveUser(ctx context.Context, userID int64) (*requestctx.AuthUser, error) {
	user, err := userlogic.NewUserLogic(ctx, r.svc).GetActiveUserForAuth(userID)
	if err != nil {
		switch {
		case errors.Is(err, userlogic.ErrUserDisabled):
			return nil, stderrors.Join(middleware.ErrRuntimeUserDisabled, err)
		case errors.Is(err, userlogic.ErrUserNotFound):
			return nil, stderrors.Join(middleware.ErrRuntimeUserNotFound, err)
		default:
			return nil, stderrors.Join(middleware.ErrRuntimeDependencyUnavailable, errors.Tag(err))
		}
	}
	profile := userlogic.BuildUserProfile(user)
	return &requestctx.AuthUser{Profile: *profile, AuthVersion: user.AuthVersion}, nil
}

// RecordAuthEvent 以旁路语义复用业务层的脱敏和 Collector 投递策略。
func (r *middlewareRuntime) RecordAuthEvent(ctx context.Context, input middleware.RuntimeAuthEvent) {
	authlogic.RecordAuthEvent(ctx, r.svc, authlogic.AuthEventInput{
		Action: input.Action, UserID: input.UserID, Identity: input.Identity, ClientIP: input.ClientIP,
		SessionID: input.SessionID, Reason: input.Reason, Count: input.Count,
	})
}

// SecurityRoute 返回当前 AppID 的签名与加密开关。
func (r *middlewareRuntime) SecurityRoute(_ context.Context, appID string) (middleware.RuntimeSecurityRoute, error) {
	registry, err := r.securityKeys()
	if err != nil {
		return middleware.RuntimeSecurityRoute{}, errors.Tag(err)
	}
	route, err := registry.Route(appID)
	if err != nil {
		return middleware.RuntimeSecurityRoute{}, errors.Tag(err)
	}
	return middleware.RuntimeSecurityRoute{SignEnabled: route.SignEnabled, CryptoEnabled: route.CryptoEnabled}, nil
}

// Signer 返回启动期预编译的签名器，不向请求链路暴露原始密钥。
func (r *middlewareRuntime) Signer(_ context.Context, appID, versionHint, grayKey, signatureType string) (security.Signer, string, error) {
	registry, err := r.securityKeys()
	if err != nil {
		return nil, "", errors.Tag(err)
	}
	return registry.Signer(appID, versionHint, grayKey, signatureType)
}

// Cryptor 返回启动期预编译的加解密器，同一对象同时服务请求与响应方向。
func (r *middlewareRuntime) Cryptor(_ context.Context, appID, versionHint, grayKey, cryptoType string) (security.Cryptor, string, error) {
	registry, err := r.securityKeys()
	if err != nil {
		return nil, "", errors.Tag(err)
	}
	return registry.Cryptor(appID, versionHint, grayKey, cryptoType)
}

// securityKeys 返回启动期注册表，缺失时拒绝安全请求。
func (r *middlewareRuntime) securityKeys() (*security.KeyRegistry, error) {
	if r == nil || r.svc == nil {
		return nil, errors.New("安全密钥注册表未初始化")
	}
	registry := r.svc.SecurityKeys()
	if registry == nil {
		return nil, errors.New("安全密钥注册表未初始化")
	}
	return registry, nil
}
