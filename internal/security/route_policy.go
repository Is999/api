package security

import (
	"slices"
	"strings"

	"api/internal/routealias"

	"github.com/Is999/go-utils/errors"
)

// RouteSecurityPolicy 定义单个路由的请求验签、响应回签与响应加密策略。
type RouteSecurityPolicy struct {
	RequestSign    []string // RequestSign 表示请求验签关键字段；nil 关闭验签，空切片只签基础头，禁止使用 *
	RequestCipher  []string // RequestCipher 表示请求允许解密的字段；禁止使用 cipher 整包加密
	ResponseSign   []string // ResponseSign 表示响应回签关键字段；nil 关闭回签，空切片只签基础头，禁止使用 *
	ResponseCipher []string // ResponseCipher 表示响应需要加密的字段路径；禁止使用 cipher 整包加密
}

// RouteSecurityPolicies 定义前台 API 的推荐安全策略，key 来自统一路由别名常量。
var RouteSecurityPolicies = map[routealias.Alias]RouteSecurityPolicy{
	// auth.register 保护注册账号、密码、联系方式和新会话 token。
	routealias.AuthRegister: {
		RequestSign:    []string{"username", "password", "nickname", "email", "phone"},
		RequestCipher:  []string{"password", "email", "phone"},
		ResponseSign:   []string{"token", "expiresAt", "user.email", "user.phone"},
		ResponseCipher: []string{"token", "user.email", "user.phone"},
	},
	// auth.login 保护登录身份、密码和响应 token。
	routealias.AuthLogin: {
		RequestSign:    []string{"identityType", "identityValue", "password"},
		RequestCipher:  []string{"identityValue", "password"},
		ResponseSign:   []string{"token", "expiresAt", "user.email", "user.phone"},
		ResponseCipher: []string{"token", "user.email", "user.phone"},
	},
	// auth.refresh 保护刷新后的访问 token。
	routealias.AuthRefresh: {
		ResponseSign:   []string{"token", "expiresAt"},
		ResponseCipher: []string{"token"},
	},
	// auth.logout 没有业务请求字段，使用 AppID、TraceID 与时间戳完成轻量验签。
	routealias.AuthLogout: {
		RequestSign: []string{},
	},
	// user.profile 对当前用户联系方式先加密，再对最终密文回签。
	routealias.UserProfile: {
		ResponseSign:   []string{"email", "phone"},
		ResponseCipher: []string{"email", "phone"},
	},
	// user.runtime.sync 走内网运维链路，不参与前台签名加密。
	routealias.UserRuntimeSync: {},
	// system.config_reload.status 走内网运维链路，不参与前台签名加密。
	routealias.SystemConfigReloadStatus: {},
	// system.config_reload.items 走内网运维链路，不参与前台签名加密。
	routealias.SystemConfigReloadItems: {},
	// system.config_reload.run 走内网运维链路，不参与前台签名加密。
	routealias.SystemConfigReloadRun: {},
}

// LookupRoutePolicy 根据路由别名读取统一安全策略，未知别名必须由调用方显式处理。
func LookupRoutePolicy(route string) (RouteSecurityPolicy, bool) {
	alias := routealias.Alias(route)
	if alias == "" || alias == routealias.Ignore {
		return RouteSecurityPolicy{}, false
	}
	policy, ok := RouteSecurityPolicies[alias]
	return policy, ok
}

// ValidateRoutePolicy 校验单条路由策略，任何字段清洗、去重或加密未签名都会拒绝启动。
func ValidateRoutePolicy(alias routealias.Alias, policy RouteSecurityPolicy) error {
	fields := []struct {
		name  string   // name 表示错误消息中的策略位置。
		items []string // items 表示该位置声明的精确字段列表。
	}{
		{name: "request_sign", items: policy.RequestSign},
		{name: "request_cipher", items: policy.RequestCipher},
		{name: "response_sign", items: policy.ResponseSign},
		{name: "response_cipher", items: policy.ResponseCipher},
	}
	for _, fieldSet := range fields {
		if err := ValidateSecurityFieldCount(fieldSet.items, string(alias)+"."+fieldSet.name); err != nil {
			return errors.Tag(err)
		}
	}
	if slices.Contains(policy.RequestSign, SignFieldAll) || slices.Contains(policy.ResponseSign, SignFieldAll) {
		return errors.Errorf("路由策略不允许全量签名 alias=%s", alias)
	}
	if slices.Contains(policy.RequestCipher, CipherWholeBody) || slices.Contains(policy.ResponseCipher, CipherWholeBody) {
		return errors.Errorf("路由策略不允许整包加密 alias=%s", alias)
	}
	// 加密不能单独关闭完整性保护，请求和响应两侧分别检查字段子集。
	if err := validateCipherFieldsSigned(alias, "request", policy.RequestCipher, policy.RequestSign); err != nil {
		return errors.Tag(err)
	}
	return errors.Tag(validateCipherFieldsSigned(alias, "response", policy.ResponseCipher, policy.ResponseSign))
}

// validateCipherFieldsSigned 要求加密字段同时参与签名，防止密文解开后缺少完整性保护。
func validateCipherFieldsSigned(alias routealias.Alias, scope string, cipherFields []string, signFields []string) error {
	for _, field := range cipherFields {
		// json: 仅是加密编解码标记，签名必须读取传输中的真实密文字段。
		if !slices.Contains(signFields, strings.TrimPrefix(field, CipherJSONPrefix)) {
			return errors.Errorf("路由加密字段未参与签名 alias=%s scope=%s field=%s", alias, scope, field)
		}
	}
	return nil
}
