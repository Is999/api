package codes

import "testing"

// TestDefaultCodeContracts 验证业务码契约唯一且可派生运行时映射。
func TestDefaultCodeContracts(t *testing.T) {
	seen := make(map[int]struct{})
	for _, contract := range DefaultCodeContracts() {
		if _, ok := seen[contract.Code]; ok {
			t.Fatalf("duplicate code contract code=%d", contract.Code)
		}
		seen[contract.Code] = struct{}{}

		if contract.HTTPStatus == 0 {
			t.Fatalf("code=%d missing HTTP status", contract.Code)
		}
		if contract.MessageKey == "" {
			t.Fatalf("code=%d missing message key", contract.Code)
		}
		if got := HTTPStatus(contract.Code); got != contract.HTTPStatus {
			t.Fatalf("HTTPStatus(%d)=%d, want %d", contract.Code, got, contract.HTTPStatus)
		}
		if got := IsSuccess(contract.Code); got != contract.Success {
			t.Fatalf("IsSuccess(%d)=%v, want %v", contract.Code, got, contract.Success)
		}
		if got, ok := MessageKey(contract.Code); !ok || got != contract.MessageKey {
			t.Fatalf("MessageKey(%d)=(%q,%v), want (%q,true)", contract.Code, got, ok, contract.MessageKey)
		}
	}
}

// TestMessageKeyUnknown 验证未知业务码不暴露默认文案 key。
func TestMessageKeyUnknown(t *testing.T) {
	if key, ok := MessageKey(999999); ok {
		t.Fatalf("MessageKey(unknown)=(%q,true), want false", key)
	}
}

// TestHTTPStatus 验证业务码到 HTTP 状态码的映射。
func TestHTTPStatus(t *testing.T) {
	tests := []struct {
		name string // 区分认证、安全、依赖和未知码回退。
		code int    // 实际传给响应封装的业务码。
		want int    // 外部 HTTP 状态，不能与业务码混用。
	}{
		{name: "success", code: Success, want: OK},
		{name: "token invalid", code: TokenInvalid, want: Unauthorized},
		{name: "security request", code: SecurityRequestRejected, want: Unauthorized},
		{name: "security payload too large", code: SecurityPayloadTooLarge, want: 413},
		{name: "dependency", code: DependencyUnavailable, want: ServiceBusy},
		{name: "create fail", code: CreateFail, want: ServerError},
		{name: "unknown failure", code: 999999, want: ServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := HTTPStatus(tt.code); got != tt.want {
				t.Fatalf("HTTPStatus(%d)=%d, want %d", tt.code, got, tt.want)
			}
		})
	}
}
