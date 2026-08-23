package middleware

import (
	"context"
	"strings"

	"api/internal/infra/collectorx"
)

// emitSecurityFailureEvent 投递签名或加密链路失败事件。
func emitSecurityFailureEvent(ctx context.Context, runtime Runtime, reason string) {
	if runtime == nil {
		return
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = collectorx.AuthSecurityReasonSecurityFailed
	}
	runtime.RecordAuthEvent(ctx, RuntimeAuthEvent{
		Action: collectorx.AuthSecurityActionSecurityFailed,
		Reason: reason,
	})
}
