package validators

import (
	"strings"
	"testing"

	"api/internal/config"
)

// TestValidateProductionAcceptsBaseline 保证负例夹具能通过完整生产门禁，避免先被无关字段拒绝。
func TestValidateProductionAcceptsBaseline(t *testing.T) {
	if err := ValidateProduction(validProductionConfig()); err != nil {
		t.Fatalf("生产校验基线应通过: %v", err)
	}
}

// TestValidateProductionRejectsPlaceholderJWTSecret 确保生产环境不能使用示例 JWT 密钥。
func TestValidateProductionRejectsPlaceholderJWTSecret(t *testing.T) {
	cfg := validProductionConfig()
	cfg.JwtSecret = "replace-with-strong-secret"
	if err := ValidateProduction(cfg); err == nil || !strings.Contains(err.Error(), "jwt_secret") {
		t.Fatal("expected placeholder jwt_secret to be rejected")
	}
}

// TestValidateProductionRejectsMissingOpsToken 确保生产环境必须配置热加载运维令牌。
func TestValidateProductionRejectsMissingOpsToken(t *testing.T) {
	cfg := validProductionConfig()
	cfg.Ops.ConfigReloadToken = ""
	if err := ValidateProduction(cfg); err == nil || !strings.Contains(err.Error(), "ops.config_reload_token") {
		t.Fatal("expected missing ops token to be rejected")
	}
}

// TestValidateProductionRejectsWhitespaceOpsToken 确保生产校验本身不会清洗运维令牌后放行。
func TestValidateProductionRejectsWhitespaceOpsToken(t *testing.T) {
	cfg := validProductionConfig()
	cfg.Ops.ConfigReloadToken = " " + cfg.Ops.ConfigReloadToken
	if err := ValidateProduction(cfg); err == nil || !strings.Contains(err.Error(), "ops.config_reload_token") {
		t.Fatal("expected whitespace ops token to be rejected")
	}
}

// TestValidateProductionRequiresCollector 确保生产认证风控事件不会被静默丢弃。
func TestValidateProductionRequiresCollector(t *testing.T) {
	cfg := validProductionConfig()
	cfg.Collector.Enabled = false
	if err := ValidateProduction(cfg); err == nil || !strings.Contains(err.Error(), "必须启用 collector") {
		t.Fatal("expected disabled collector to be rejected")
	}
}

// TestValidateProductionRequiresAuthSecurityRoute 确保生产认证事件固定进入 Admin 消费 Topic。
func TestValidateProductionRequiresAuthSecurityRoute(t *testing.T) {
	for _, topic := range []string{"", "wrong_topic", " " + config.CollectorTopicAuthSecurity} {
		cfg := validProductionConfig()
		cfg.Collector.Tasks[config.CollectorBizTypeAuthSecurity] = config.CollectorTaskConfig{Topic: topic}
		if err := ValidateProduction(cfg); err == nil || !strings.Contains(err.Error(), "collector.tasks.auth.security.topic") {
			t.Fatalf("expected auth.security topic %q to be rejected", topic)
		}
	}
}

// validProductionConfig 返回满足生产硬校验的最小配置。
func validProductionConfig() config.Config {
	cfg := config.Config{
		AppKey:    "prod-app-key-9f3b6e1c7a2d4f0b",
		JwtSecret: "prod-jwt-9f3b6e1c7a2d4f0b8c5e6a1d2f3c4b5a",
		Auth: config.AuthConfig{
			LoginRateLimit: config.AuthRateLimitConfig{
				Enabled: true,
			},
			RegisterRateLimit: config.AuthRateLimitConfig{
				Enabled: true,
			},
		},
		Ops: config.OpsConfig{
			ConfigReloadToken: "prod-ops-9f3b6e1c7a2d4f0b",
		},
		Collector: config.CollectorConfig{
			Enabled: true,
			Tasks: map[string]config.CollectorTaskConfig{
				config.CollectorBizTypeAuthSecurity: {Topic: config.CollectorTopicAuthSecurity},
			},
		},
	}
	cfg.Mode = "pro"
	return cfg
}
