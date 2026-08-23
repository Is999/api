package requestctx

import (
	"context"

	"api/internal/types"
)

// User 存储上下文中的前台用户信息。
type User struct {
	ID   int64  // 鉴权确认的用户 ID，0 不构成有效身份
	Name string // 主库确认的用户名，不采用请求参数
	IP   string // 按可信代理规则解析的本次来源 IP
}

// AuthUser 保存鉴权主库已确认的脱敏资料和认证版本，不携带密码或联系方式密文。
type AuthUser struct {
	Profile     types.UserProfile // Profile 是本次请求使用的用户公开资料值
	AuthVersion uint64            // AuthVersion 来源主库，用于与当前 JWT 声明比对
}

// UserFromContext 从请求元数据中提取前台用户信息。
func UserFromContext(ctx context.Context) *User {
	if ctx == nil {
		return nil
	}
	meta := FromContext(ctx)
	if meta == nil || meta.UserID == 0 {
		return nil
	}
	user := &User{
		ID:   meta.UserID,
		Name: meta.UserName,
		IP:   meta.ClientIP,
	}
	if user.Name == "" {
		return nil
	}
	return user
}

// SetAuthUser 复制鉴权用户快照，避免下游修改运行适配器持有的数据。
func SetAuthUser(ctx context.Context, user *AuthUser) {
	meta := FromContext(ctx)
	if meta == nil {
		return
	}
	if user == nil {
		meta.authUser = AuthUser{}
		return
	}
	meta.authUser = *user
}

// AuthUserFromContext 返回鉴权用户快照副本，缺少有效用户或认证版本时返回 nil。
func AuthUserFromContext(ctx context.Context) *AuthUser {
	meta := FromContext(ctx)
	if meta == nil || meta.authUser.Profile.ID <= 0 || meta.authUser.AuthVersion == 0 {
		return nil
	}
	return new(meta.authUser)
}
