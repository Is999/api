package middleware

import (
	"testing"

	codes "api/common/codes"
	authlogic "api/internal/logic/auth"
	"api/internal/security"

	"github.com/Is999/go-utils/errors"
)

// TestResolveSecurityFailureCodeMapsReasons 锁定各安全阶段失败原因到对外业务码的映射。
func TestResolveSecurityFailureCodeMapsReasons(t *testing.T) {
	tests := []struct {
		name     string // 失败时定位验签、解密或响应加工阶段。
		reason   string // 中间件记录的低基数失败原因。
		fallback int    // 未登记原因才使用调用方业务码。
		err      error  // 非空载荷超限错误优先于阶段原因。
		want     int    // 对外响应应使用的稳定安全业务码。
	}{
		{
			name:     "app id invalid",
			reason:   authlogic.AuthEventReasonSecurityAppIDInvalid,
			fallback: codes.ParamError,
			want:     codes.SecurityAppIDInvalid,
		},
		{
			name:     "signature failed",
			reason:   authlogic.AuthEventReasonSignatureFailed,
			fallback: codes.AuthFailed,
			want:     codes.SecurityRequestRejected,
		},
		{
			name:     "request decrypt failed",
			reason:   authlogic.AuthEventReasonRequestDecryptFailed,
			fallback: codes.AuthFailed,
			want:     codes.SecurityRequestRejected,
		},
		{
			name:     "response sign failed",
			reason:   authlogic.AuthEventReasonResponseSignFailed,
			fallback: codes.InternalError,
			want:     codes.SecurityResponseSignFailed,
		},
		{
			name:     "response encrypt failed",
			reason:   authlogic.AuthEventReasonResponseEncryptFailed,
			fallback: codes.InternalError,
			want:     codes.SecurityResponseEncryptFailed,
		},
		{
			name:     "fallback",
			reason:   "custom_reason",
			fallback: codes.InternalError,
			want:     codes.InternalError,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := resolveSecurityFailureCode(tt.reason, tt.fallback, tt.err); got != tt.want {
				t.Fatalf("resolveSecurityFailureCode() = %d, want %d", got, tt.want)
			}
		})
	}
}

// TestResolveSecurityFailureCodePrefersPayloadLimit 确保载荷超限覆盖阶段原因，避免误报为响应签名失败。
func TestResolveSecurityFailureCodePrefersPayloadLimit(t *testing.T) {
	err := errors.Wrapf(security.ErrSecurityPayloadTooLarge, "响应字段超过上限")
	got := resolveSecurityFailureCode(authlogic.AuthEventReasonResponseSignFailed, codes.InternalError, err)
	if got != codes.SecurityPayloadTooLarge {
		t.Fatalf("resolveSecurityFailureCode() = %d, want %d", got, codes.SecurityPayloadTooLarge)
	}
}

// TestResolveSecurityFailureReasonPrefersPayloadLimit 确保载荷超限统一归并到低基数风控原因。
func TestResolveSecurityFailureReasonPrefersPayloadLimit(t *testing.T) {
	err := errors.Wrapf(security.ErrSecurityPayloadTooLarge, "请求字段超过上限")
	got := resolveSecurityFailureReason(authlogic.AuthEventReasonSignatureFailed, err)
	if got != authlogic.AuthEventReasonSecurityPayloadTooLarge {
		t.Fatalf("resolveSecurityFailureReason() = %q, want %q", got, authlogic.AuthEventReasonSecurityPayloadTooLarge)
	}
}

// TestResolveSecurityFailureReasonFallback 确保空原因收敛为稳定默认值，已登记外原因仅清理首尾空白。
func TestResolveSecurityFailureReasonFallback(t *testing.T) {
	if got := resolveSecurityFailureReason("", nil); got != authlogic.AuthEventReasonSecurityFailed {
		t.Fatalf("resolveSecurityFailureReason(empty) = %q, want %q", got, authlogic.AuthEventReasonSecurityFailed)
	}
	if got := resolveSecurityFailureReason(" custom_reason ", nil); got != "custom_reason" {
		t.Fatalf("resolveSecurityFailureReason(custom) = %q, want custom_reason", got)
	}
}
