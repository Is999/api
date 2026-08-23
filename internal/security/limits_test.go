package security

import (
	"strings"
	"testing"

	"github.com/Is999/go-utils/errors"
)

// TestValidateSecurityFieldCountRejectsTooManyFields 确保字段级安全策略拒绝超过固定数量上限的字段。
func TestValidateSecurityFieldCountRejectsTooManyFields(t *testing.T) {
	fields := []string{"f1", "f2", "f3", "f4", "f5", "f6", "f7", "f8", "f9"}
	if err := ValidateSecurityFieldCount(fields, "请求签名"); err == nil {
		t.Fatal("ValidateSecurityFieldCount() should reject too many fields")
	}
}

// TestValidateSecurityFieldCountRejectsNonCanonicalFields 确保策略字段不会被 trim 或去重后静默改变签名范围。
func TestValidateSecurityFieldCountRejectsNonCanonicalFields(t *testing.T) {
	for _, fields := range [][]string{{" user.id "}, {"user.id", "user.id"}, {""}} {
		if err := ValidateSecurityFieldCount(fields, "请求签名"); err == nil {
			t.Fatalf("期望非规范安全字段被拒绝: %q", fields)
		}
	}
}

// TestValidateSecurityScalarValueRejectsComplexValue 确保标量安全字段拒绝对象等复杂值。
func TestValidateSecurityScalarValueRejectsComplexValue(t *testing.T) {
	value := map[string]any{"name": "demo"}
	if err := ValidateSecurityScalarValue("请求加密", "profile", value); err == nil {
		t.Fatal("ValidateSecurityScalarValue() should reject complex value")
	}
}

// TestValidateSecurityTextValueRejectsOversizeValue 确保文本安全字段在加密前执行字节上限校验。
func TestValidateSecurityTextValueRejectsOversizeValue(t *testing.T) {
	value := strings.Repeat("x", MaxSecurityFieldBytes+1)
	if err := ValidateSecurityTextValue("请求加密", "password", value, MaxSecurityFieldBytes); err == nil {
		t.Fatal("ValidateSecurityTextValue() should reject oversize value")
	}
}

// TestValidateSecurityJSONValueRejectsOversizeValue 验证复杂安全字段按序列化后的 JSON 字节数拒绝超大载荷。
func TestValidateSecurityJSONValueRejectsOversizeValue(t *testing.T) {
	value := map[string]any{"text": strings.Repeat("x", MaxSecurityJSONFieldBytes)}
	if _, err := ValidateSecurityJSONValue("响应加密", "profile", value); err == nil {
		t.Fatal("ValidateSecurityJSONValue() should reject oversize JSON value")
	}
}

// TestValidateSecurityLimitErrorsUseSentinel 确保各阶段可通过统一哨兵错误映射载荷超限业务码。
func TestValidateSecurityLimitErrorsUseSentinel(t *testing.T) {
	err := ValidateSecurityScalarValue("响应加密", "profile", map[string]any{"name": "demo"})
	if !errors.Is(err, ErrSecurityPayloadTooLarge) {
		t.Fatalf("ValidateSecurityScalarValue() error = %v, want ErrSecurityPayloadTooLarge", err)
	}
}
