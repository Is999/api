package validators

import (
	"strings"

	"api/internal/config"

	"github.com/Is999/go-utils/errors"
)

const (
	minOpsTokenLength = 16 // 运维令牌生产环境最小长度
)

// ValidateProduction 校验生产环境禁止使用的占位和不安全配置。
func ValidateProduction(c config.Config) error {
	if !config.IsProductionMode(c.Mode) {
		return nil
	}
	if isPlaceholderSecret(c.JwtSecret) {
		return errors.Errorf("生产环境 jwt_secret 不能使用占位值")
	}
	if isPlaceholderSecret(c.AppKey) {
		return errors.Errorf("生产环境 app_key 不能使用占位值")
	}
	if c.Redis.TLSInsecureSkipVerify {
		return errors.Errorf("生产环境 redis.tls_insecure_skip_verify 不能为 true")
	}
	if !c.Collector.Enabled {
		return errors.Errorf("生产环境必须启用 collector，认证风控事件依赖该链路")
	}
	// authTask 是生产认证风控事件的固定 Kafka 路由。
	authTask, ok := c.Collector.Tasks[config.CollectorBizTypeAuthSecurity]
	if !ok || authTask.Topic != config.CollectorTopicAuthSecurity {
		return errors.Errorf("生产环境 collector.tasks.%s.topic 必须配置为 %s", config.CollectorBizTypeAuthSecurity, config.CollectorTopicAuthSecurity)
	}
	if !c.Auth.LoginRateLimit.Enabled {
		return errors.Errorf("生产环境必须启用 auth.login_rate_limit")
	}
	if c.Auth.RegisterEnabled && !c.Auth.RegisterRateLimit.Enabled {
		return errors.Errorf("生产环境开放注册时必须启用 auth.register_rate_limit")
	}
	token := c.Ops.ConfigReloadToken
	if token != strings.TrimSpace(token) {
		return errors.Errorf("生产环境 ops.config_reload_token 不能包含首尾空白")
	}
	if len(token) < minOpsTokenLength {
		return errors.Errorf("生产环境 ops.config_reload_token 长度不能小于 %d", minOpsTokenLength)
	}
	if isPlaceholderSecret(token) {
		return errors.Errorf("生产环境 ops.config_reload_token 不能使用占位值")
	}
	return nil
}

// isPlaceholderSecret 判断密钥是否仍为示例占位值。
func isPlaceholderSecret(value string) bool {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return true
	}
	for _, pattern := range []string{"replace-with", "please-change", "change-me", "changeme", "your-", "todo"} {
		if strings.Contains(value, pattern) {
			return true
		}
	}
	return false
}
