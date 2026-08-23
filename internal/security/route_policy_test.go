package security

import (
	"strings"
	"testing"

	"api/internal/routealias"
)

// TestLookupRoutePolicyRejectsUnknown 验证未知别名不会降级成空策略。
func TestLookupRoutePolicyRejectsUnknown(t *testing.T) {
	if _, ok := LookupRoutePolicy("unknown.route"); ok {
		t.Fatal("LookupRoutePolicy unknown should report missing")
	}
}

// TestLookupRoutePolicyForLogoutUsesHeaderOnlySign 验证无业务字段的退出接口仍启用基础头验签。
func TestLookupRoutePolicyForLogoutUsesHeaderOnlySign(t *testing.T) {
	policy, ok := LookupRoutePolicy(string(routealias.AuthLogout))
	if !ok || policy.RequestSign == nil || len(policy.RequestSign) != 0 {
		t.Fatalf("LookupRoutePolicy(auth.logout) request sign = %#v, want enabled empty fields", policy.RequestSign)
	}
}

// TestValidateRoutePolicyRejectsNonCanonicalFields 确保策略拒绝重复、空白或未纳入签名的加密字段。
func TestValidateRoutePolicyRejectsNonCanonicalFields(t *testing.T) {
	alias := routealias.Alias("test.route")
	for _, policy := range []RouteSecurityPolicy{
		{RequestSign: []string{" token "}},
		{RequestSign: []string{"token", "token"}},
		{RequestSign: []string{"token"}, RequestCipher: []string{"password"}},
	} {
		if err := ValidateRoutePolicy(alias, policy); err == nil {
			t.Fatalf("ValidateRoutePolicy(%+v) expected error", policy)
		}
	}
}

// TestValidateRoutePolicyJSONCipherFields 确保请求和嵌套响应的 JSON 编码标记不改变真实签名路径。
func TestValidateRoutePolicyJSONCipherFields(t *testing.T) {
	for _, policy := range []RouteSecurityPolicy{
		{RequestSign: []string{"payload"}, RequestCipher: []string{"json:payload"}},
		{ResponseSign: []string{"user.profile"}, ResponseCipher: []string{"json:user.profile"}},
	} {
		if err := ValidateRoutePolicy("test.route", policy); err != nil {
			t.Fatalf("真实密文字段参与签名时不应拒绝: %v", err)
		}
	}
	for _, policy := range []RouteSecurityPolicy{
		{RequestSign: []string{"json:payload"}, RequestCipher: []string{"json:payload"}},
		{ResponseSign: []string{"json:user.profile"}, ResponseCipher: []string{"json:user.profile"}},
	} {
		if err := ValidateRoutePolicy("test.route", policy); err == nil {
			t.Fatalf("编码标记不能替代真实密文字段: %+v", policy)
		}
	}
}

// TestSignPoliciesExcludeLargeDisplayFields 校验描述和备注等展示性长文本不进入轻量签名。
func TestSignPoliciesExcludeLargeDisplayFields(t *testing.T) {
	for alias, policy := range RouteSecurityPolicies {
		for _, fields := range [][]string{policy.RequestSign, policy.ResponseSign} {
			for _, field := range fields {
				switch strings.ToLower(strings.TrimSpace(field)) {
				case "description", "reason", "remark":
					t.Fatalf("route %s signs large display field %q", alias, field)
				}
			}
		}
	}
}
