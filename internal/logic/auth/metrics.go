package auth

import (
	"strings"
	"sync"

	"api/common/prometheusx"

	"github.com/Is999/go-utils/errors"
	"github.com/prometheus/client_golang/prometheus"
)

// 认证运行指标只使用代码内固定动作标签，避免用户身份进入时序标签造成高基数。
var (
	authMetricsOnce sync.Once // 保证认证指标只在启动阶段注册一次。
	authMetricsErr  error     // 保存指标名称或类型冲突，供 bootstrap 拒绝启动。
	// authRateLimitCleanupFailuresTotal 统计登录已成功但限流计数清理失败的次数。
	authRateLimitCleanupFailuresTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Namespace: "api",
			Subsystem: "auth",
			Name:      "rate_limit_cleanup_failures_total",
			Help:      "登录成功后认证限流状态清理失败的累计次数。",
		},
		[]string{"action"},
	)
)

// RegisterMetrics 注册认证运行指标；同类型重复注册复用既有实例，其它冲突返回启动错误。
func RegisterMetrics() error {
	authMetricsOnce.Do(func() {
		authRateLimitCleanupFailuresTotal, authMetricsErr = prometheusx.Register(authRateLimitCleanupFailuresTotal)
		if authMetricsErr == nil {
			// 预创建固定标签序列，使健康实例也能被 Prometheus 抓到明确的零基线。
			authRateLimitCleanupFailuresTotal.WithLabelValues(authRateLimitActionLoginIP).Add(0)
			authRateLimitCleanupFailuresTotal.WithLabelValues(authRateLimitActionLoginIdentity).Add(0)
		}
	})
	return errors.Tag(authMetricsErr)
}

// recordAuthRateLimitCleanupFailure 记录一次限流清理失败；标签仅允许两个登录固定维度。
func recordAuthRateLimitCleanupFailure(action string) {
	action = strings.TrimSpace(action)
	switch action {
	case authRateLimitActionLoginIP, authRateLimitActionLoginIdentity:
	default:
		action = "unknown"
	}
	authRateLimitCleanupFailuresTotal.WithLabelValues(action).Inc()
}
