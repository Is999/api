package security

import (
	"slices"
	"strings"
	"testing"
)

// TestBuildSignStringSortsDeclaredFields 验证显式字段按名称排序编码，且不改写清单快照顺序。
func TestBuildSignStringSortsDeclaredFields(t *testing.T) {
	fields := []string{"b", "a"}
	got := BuildSignString(map[string]any{"a": "first", "b": "second"}, fields, "trace", "1700000000", "app")
	want := "v2|app=3:app|trace=5:trace|timestamp=10:1700000000|field=1:a5:first|field=1:b6:second"
	if got != want {
		t.Fatalf("显式字段签名顺序不符合协议: %q", got)
	}
	if !slices.Equal(fields, []string{"b", "a"}) {
		t.Fatalf("签名过程不应修改字段清单: %v", fields)
	}
}

// TestBuildSignStringUsesStableOrder 锁定字段排序、长度前缀和复杂值 JSON 排序组成的签名协议。
func TestBuildSignStringUsesStableOrder(t *testing.T) {
	data := map[string]any{
		"b":       2,
		"sign":    "ignored",
		"a":       "1",
		"profile": map[string]any{"name": "tom", "age": 18},
	}

	got := BuildSignString(data, []string{SignFieldAll}, "trace", "1700000000", "app")
	want := `v2|app=3:app|trace=5:trace|timestamp=10:1700000000|field=1:a1:1|field=1:b1:2|field=7:profile23:{"age":18,"name":"tom"}`
	if got != want {
		t.Fatalf("BuildSignString() = %q, want %q", got, want)
	}
}

// TestBuildSignStringWithoutBusinessFields 校验空字段策略只使用 AppID、TraceID 与时间戳。
func TestBuildSignStringWithoutBusinessFields(t *testing.T) {
	got := BuildSignString(map[string]any{"ignored": "value"}, []string{}, "trace-demo-000", "1700000000", "demo-app")
	want := "v2|app=8:demo-app|trace=14:trace-demo-000|timestamp=10:1700000000"
	if got != want {
		t.Fatalf("BuildSignString() = %q, want %q", got, want)
	}
}

// TestBuildSignStringStableArray 校验数组字段使用稳定 JSON 签名格式。
func TestBuildSignStringStableArray(t *testing.T) {
	got := BuildSignString(map[string]any{
		"ids": []int64{2, 3},
	}, []string{"ids"}, "trace-demo-004", "1700000000", "demo-app")
	want := "v2|app=8:demo-app|trace=14:trace-demo-004|timestamp=10:1700000000|field=3:ids5:[2,3]"
	if got != want {
		t.Fatalf("BuildSignString() = %q, want %q", got, want)
	}
}

// TestBuildSignStringSeparatesDelimiterValues 验证字段值包含 & 或 = 时仍由长度前缀区分字段边界。
func TestBuildSignStringSeparatesDelimiterValues(t *testing.T) {
	left := BuildSignString(map[string]any{"a": "1&b=2", "b": "3"}, []string{"a", "b"}, "trace", "1700000000", "app")
	right := BuildSignString(map[string]any{"a": "1", "b": "2&b=3"}, []string{"a", "b"}, "trace", "1700000000", "app")
	if left == right {
		t.Fatalf("不同字段值生成了相同签名串: %q", left)
	}
}

// TestBuildSignStringPreservesFloatPrecision 确保不同高精度数值不会因固定小数位截断生成同一签名串。
func TestBuildSignStringPreservesFloatPrecision(t *testing.T) {
	left := BuildSignString(map[string]any{"amount": 1.0000001}, []string{"amount"}, "trace", "1700000000", "app")
	right := BuildSignString(map[string]any{"amount": 1.0000002}, []string{"amount"}, "trace", "1700000000", "app")
	if left == right {
		t.Fatalf("不同浮点值生成了相同签名串: %q", left)
	}
	if !strings.Contains(left, "9:1.0000001") || !strings.Contains(right, "9:1.0000002") {
		t.Fatalf("浮点值未使用可往返精度: left=%q right=%q", left, right)
	}
}

// TestBuildSignStringPreservesNestedFloatPrecision 确保复杂 JSON 字段递归序列化时同样保留浮点精度。
func TestBuildSignStringPreservesNestedFloatPrecision(t *testing.T) {
	left := SignValueString(map[string]any{"amount": 1.0000001})
	right := SignValueString(map[string]any{"amount": 1.0000002})
	if left == right || left != `{"amount":1.0000001}` || right != `{"amount":1.0000002}` {
		t.Fatalf("复杂字段浮点编码不准确: left=%q right=%q", left, right)
	}
}

// TestBuildSignStringResolvesNestedFieldPaths 验证按点分路径提取嵌套值参与签名串，不执行加解密。
func TestBuildSignStringResolvesNestedFieldPaths(t *testing.T) {
	data := map[string]any{
		"token": "token-value",
		"user": map[string]any{
			"email": "masked@example.test",
			"phone": "138****0000",
		},
	}
	got := BuildSignString(data, []string{"token", "user.email", "user.phone"}, "trace", "1700000000", "app")
	want := "v2|app=3:app|trace=5:trace|timestamp=10:1700000000" +
		"|field=5:token11:token-value" +
		"|field=10:user.email19:masked@example.test" +
		"|field=10:user.phone11:138****0000"
	if got != want {
		t.Fatalf("BuildSignString() = %q, want %q", got, want)
	}
}

// TestSignFieldValueRejectsNonCanonicalPath 确保字段路径不会按 trim 后的另一名称参与签名。
func TestSignFieldValueRejectsNonCanonicalPath(t *testing.T) {
	data := map[string]any{"user": map[string]any{"email": "demo@example.com"}}
	for _, field := range []string{" user.email", "user. email", "user.email "} {
		if _, ok := SignFieldValue(data, field); ok {
			t.Fatalf("期望非规范签名字段路径被拒绝: %q", field)
		}
	}
}

// TestResolveSecurityHeaderTypes 确保只为缺失请求头补协议默认值，不改写客户端输入。
func TestResolveSecurityHeaderTypes(t *testing.T) {
	signCases := map[string]string{
		"":  SignatureTypeRSA,
		"R": SignatureTypeRSA,
		"H": SignatureTypeHMAC,
	}
	for input, want := range signCases {
		if got := ResolveSignatureType(input); got != want {
			t.Fatalf("ResolveSignatureType(%q) = %q, want %q", input, got, want)
		}
	}
	for _, input := range []string{"r", "h", "rsa", "RSA", "AES", " R ", "hmac", "md5", "M", "A"} {
		if got := ResolveSignatureType(input); got != input {
			t.Fatalf("ResolveSignatureType(%q) = %q, want unchanged unsupported value", input, got)
		}
	}

	cryptoCases := map[string]string{
		"":  CryptoTypeAES,
		"A": CryptoTypeAES,
		"R": CryptoTypeRSA,
	}
	for input, want := range cryptoCases {
		if got := ResolveCryptoType(input); got != want {
			t.Fatalf("ResolveCryptoType(%q) = %q, want %q", input, got, want)
		}
	}
	for _, input := range []string{"a", "r", "AES", "rsa", " A "} {
		if got := ResolveCryptoType(input); got != input {
			t.Fatalf("ResolveCryptoType(%q) = %q, want unchanged unsupported value", input, got)
		}
	}
}
