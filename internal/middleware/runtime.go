package middleware

import (
	"context"

	"api/internal/requestctx"
	"api/internal/security"

	"github.com/Is999/go-utils/errors"
)

var (
	// ErrRuntimeUserNotFound 表示鉴权主库未找到 token 对应用户。
	ErrRuntimeUserNotFound = errors.New("鉴权用户不存在")
	// ErrRuntimeUserDisabled 表示鉴权主库中的用户已禁用。
	ErrRuntimeUserDisabled = errors.New("鉴权用户已禁用")
	// ErrRuntimeDependencyUnavailable 表示主库等鉴权依赖暂时不可用，不能伪装成 token 失效。
	ErrRuntimeDependencyUnavailable = errors.New("鉴权运行依赖不可用")
)

// RuntimeAuthEvent 是中间件提交给业务适配器的认证风控事件。
type RuntimeAuthEvent struct {
	Action    string // Action 是 Collector 认证事件动作
	UserID    int64  // UserID 未知时为 0
	Identity  string // Identity 仅由业务适配器做不可逆哈希后投递
	ClientIP  string // ClientIP 仅由业务适配器做不可逆哈希后投递
	SessionID string // SessionID 仅由业务适配器做不可逆哈希后投递
	Reason    string // Reason 是稳定失败原因枚举
	Count     int    // Count 是批量失效等动作的影响数量
}

// RuntimeSecurityRoute 是中间件实际消费的安全链路开关。
type RuntimeSecurityRoute struct {
	SignEnabled   bool // SignEnabled 控制请求验签和响应回签
	CryptoEnabled bool // CryptoEnabled 控制请求解密和响应加密
}

// Runtime 隔离 HTTP 中间件与用户、风控和密钥业务逻辑，具体实现由 handler 装配层提供。
type Runtime interface {
	// ActiveUser 返回鉴权所需的最新用户快照，依赖故障必须保留可分类错误。
	ActiveUser(ctx context.Context, userID int64) (*requestctx.AuthUser, error)
	// RecordAuthEvent 以 best-effort 语义提交脱敏认证事件。
	RecordAuthEvent(ctx context.Context, event RuntimeAuthEvent)
	// SecurityRoute 按 AppID 返回启动快照中的安全链路开关。
	SecurityRoute(ctx context.Context, appID string) (RuntimeSecurityRoute, error)
	// Signer 按显式版本或灰度键返回可并发复用的预编译签名器。
	Signer(ctx context.Context, appID, versionHint, grayKey, signatureType string) (security.Signer, string, error)
	// Cryptor 按显式版本或灰度键返回可并发复用的预编译加解密器。
	Cryptor(ctx context.Context, appID, versionHint, grayKey, cryptoType string) (security.Cryptor, string, error)
}

// requireRuntime 在需要访问业务能力时失败关闭，避免 nil 适配器绕过鉴权或安全策略。
func requireRuntime(runtime Runtime) (Runtime, error) {
	if runtime == nil {
		return nil, errors.New("中间件运行适配器未初始化")
	}
	return runtime, nil
}
