package middleware

import "testing"

// TestParseHeaderTraceIDRequiresCanonicalHex 确保自定义 trace id 只有 32 位小写十六进制一种输入格式。
func TestParseHeaderTraceIDRequiresCanonicalHex(t *testing.T) {
	const canonical = "0123456789abcdef0123456789abcdef"
	if traceID, ok := parseHeaderTraceID(canonical); !ok || traceID.String() != canonical {
		t.Fatalf("规范 trace id 解析失败: id=%s ok=%t", traceID.String(), ok)
	}
	for _, raw := range []string{
		"01234567-89ab-cdef-0123-456789abcdef",
		"0123456789ABCDEF0123456789ABCDEF",
		" " + canonical,
		canonical + " ",
		"00000000000000000000000000000000",
	} {
		if _, ok := parseHeaderTraceID(raw); ok {
			t.Fatalf("非规范 trace id 不应被继承: %q", raw)
		}
	}
}
