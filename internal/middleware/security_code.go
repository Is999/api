package middleware

import (
	"strings"

	codes "api/common/codes"
	"api/internal/infra/collectorx"
	"api/internal/security"

	"github.com/Is999/go-utils/errors"
)

// resolveSecurityFailureCode 将安全链路失败原因转换为可观测的业务码。
func resolveSecurityFailureCode(reason string, fallback int, err error) int {
	reason = resolveSecurityFailureReason(reason, err)
	if errors.Is(err, security.ErrSecurityPayloadTooLarge) {
		return codes.SecurityPayloadTooLarge
	}
	switch strings.TrimSpace(reason) {
	case collectorx.AuthSecurityReasonSecurityAppIDInvalid:
		return codes.SecurityAppIDInvalid
	case collectorx.AuthSecurityReasonSecurityKeyUnavailable:
		return codes.SecurityKeyUnavailable
	case collectorx.AuthSecurityReasonSignatureFailed,
		collectorx.AuthSecurityReasonRequestDecryptFailed:
		return codes.SecurityRequestRejected
	case collectorx.AuthSecurityReasonSecurityPayloadTooLarge:
		return codes.SecurityPayloadTooLarge
	case collectorx.AuthSecurityReasonResponseSignFailed:
		return codes.SecurityResponseSignFailed
	case collectorx.AuthSecurityReasonCryptoDisabled:
		return codes.SecurityCryptoDisabled
	case collectorx.AuthSecurityReasonResponseEncryptFailed:
		return codes.SecurityResponseEncryptFailed
	default:
		if fallback != codes.Undefined {
			return fallback
		}
		return codes.AuthFailed
	}
}

// resolveSecurityFailureReason 将安全链路内部错误归并为稳定风控原因。
func resolveSecurityFailureReason(reason string, err error) string {
	if errors.Is(err, security.ErrSecurityPayloadTooLarge) {
		return collectorx.AuthSecurityReasonSecurityPayloadTooLarge
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return collectorx.AuthSecurityReasonSecurityFailed
	}
	return reason
}
