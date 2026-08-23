package middleware

import (
	"context"
	stderrors "errors"
	"testing"

	"api/internal/bootstrap/configload/validators"
	"api/internal/config"
	authlogic "api/internal/logic/auth"
	userlogic "api/internal/logic/user"
	"api/internal/requestctx"
	"api/internal/security"
	"api/internal/svc"

	"github.com/Is999/go-utils/errors"
)

// testMiddlewareRuntime 让中间件测试继续覆盖真实业务适配行为，生产代码不反向依赖 logic。
type testMiddlewareRuntime struct {
	svc *svc.ServiceContext // svc 是当前测试场景的依赖快照
}

// newTestAuthMiddleware 通过测试适配器连接真实用户查询与安全对象，未直接实例化 bootstrap 适配器。
func newTestAuthMiddleware(svcCtx *svc.ServiceContext) *AuthMiddleware {
	return NewAuthMiddleware(svcCtx, &testMiddlewareRuntime{svc: svcCtx})
}

// newTestSignatureMiddleware 创建使用启动期密钥注册表的签名中间件。
func newTestSignatureMiddleware(svcCtx *svc.ServiceContext) *SignatureMiddleware {
	return NewSignatureMiddleware(svcCtx, &testMiddlewareRuntime{svc: svcCtx})
}

// newTestCryptoMiddleware 创建使用启动期密钥注册表的加密中间件。
func newTestCryptoMiddleware(svcCtx *svc.ServiceContext) *CryptoMiddleware {
	return NewCryptoMiddleware(svcCtx, &testMiddlewareRuntime{svc: svcCtx})
}

// newSecurityTestServiceContext 复用生产编译入口注入密钥注册表，避免测试保留原始密钥读取路径。
func newSecurityTestServiceContext(t *testing.T, cfg config.Config, deps svc.Dependencies) *svc.ServiceContext {
	t.Helper()
	registry, err := validators.CompileSecurityRegistry(cfg)
	if err != nil {
		t.Fatalf("CompileSecurityRegistry() error = %v", err)
	}
	deps.SecurityKeys = registry
	return svc.NewServiceContext(cfg, "test-version", deps)
}

// ActiveUser 复用真实用户查询，并把业务状态转换为中间件约定的受控错误。
func (r *testMiddlewareRuntime) ActiveUser(ctx context.Context, userID int64) (*requestctx.AuthUser, error) {
	user, err := userlogic.NewUserLogic(ctx, r.svc).GetActiveUserForAuth(userID)
	if err != nil {
		switch {
		case errors.Is(err, userlogic.ErrUserDisabled):
			return nil, stderrors.Join(ErrRuntimeUserDisabled, err)
		case errors.Is(err, userlogic.ErrUserNotFound):
			return nil, stderrors.Join(ErrRuntimeUserNotFound, err)
		default:
			return nil, stderrors.Join(ErrRuntimeDependencyUnavailable, errors.Tag(err))
		}
	}
	profile := userlogic.BuildUserProfile(user)
	return &requestctx.AuthUser{Profile: *profile, AuthVersion: user.AuthVersion}, nil
}

// RecordAuthEvent 把中间件事件转交真实认证事件入口，测试中保留相同副作用。
func (r *testMiddlewareRuntime) RecordAuthEvent(ctx context.Context, input RuntimeAuthEvent) {
	authlogic.RecordAuthEvent(ctx, r.svc, authlogic.AuthEventInput{
		Action: input.Action, UserID: input.UserID, Identity: input.Identity, ClientIP: input.ClientIP,
		SessionID: input.SessionID, Reason: input.Reason, Count: input.Count,
	})
}

// SecurityRoute 从启动期密钥注册表读取当前应用的签名和加密开关。
func (r *testMiddlewareRuntime) SecurityRoute(_ context.Context, appID string) (RuntimeSecurityRoute, error) {
	registry, err := r.securityKeys()
	if err != nil {
		return RuntimeSecurityRoute{}, errors.Tag(err)
	}
	route, err := registry.Route(appID)
	if err != nil {
		return RuntimeSecurityRoute{}, errors.Tag(err)
	}
	return RuntimeSecurityRoute{SignEnabled: route.SignEnabled, CryptoEnabled: route.CryptoEnabled}, nil
}

// Signer 返回测试启动阶段已编译的签名器。
func (r *testMiddlewareRuntime) Signer(_ context.Context, appID, versionHint, grayKey, signatureType string) (security.Signer, string, error) {
	registry, err := r.securityKeys()
	if err != nil {
		return nil, "", errors.Tag(err)
	}
	return registry.Signer(appID, versionHint, grayKey, signatureType)
}

// Cryptor 返回测试启动阶段已编译的加解密器。
func (r *testMiddlewareRuntime) Cryptor(_ context.Context, appID, versionHint, grayKey, cryptoType string) (security.Cryptor, string, error) {
	registry, err := r.securityKeys()
	if err != nil {
		return nil, "", errors.Tag(err)
	}
	return registry.Cryptor(appID, versionHint, grayKey, cryptoType)
}

// securityKeys 保持测试适配器与生产适配器相同的缺失依赖失败语义。
func (r *testMiddlewareRuntime) securityKeys() (*security.KeyRegistry, error) {
	if r == nil || r.svc == nil {
		return nil, errors.New("安全密钥注册表未初始化")
	}
	registry := r.svc.SecurityKeys()
	if registry == nil {
		return nil, errors.New("安全密钥注册表未初始化")
	}
	return registry, nil
}
