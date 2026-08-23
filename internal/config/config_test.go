package config

import (
	"testing"

	"github.com/zeromicro/go-zero/core/conf"
)

// TestEmptySecurityYAMLKeepsSwitchesDisabled 锁定空安全段的唯一语义，避免解析默认值把未配置链路误标为启用。
func TestEmptySecurityYAMLKeepsSwitchesDisabled(t *testing.T) {
	var cfg SecurityConfig
	if err := conf.LoadFromYamlBytes([]byte("secret_key: {}\n"), &cfg); err != nil {
		t.Fatalf("LoadFromYamlBytes() error = %v", err)
	}
	if cfg.SecretKey.SignStatus != 0 || cfg.SecretKey.CryptoStatus != 0 {
		t.Fatalf("empty security switches = sign:%d crypto:%d, want both disabled", cfg.SecretKey.SignStatus, cfg.SecretKey.CryptoStatus)
	}
}

// TestJWTExpiresInSecondsAppliesRuntimeBounds 确保绕过启动校验的测试装配也不会生成超长期令牌。
func TestJWTExpiresInSecondsAppliesRuntimeBounds(t *testing.T) {
	tests := []struct {
		configured int64 // configured 是 ServiceContext 收到的原始秒数。
		want       int64 // want 是签发和缓存链路实际使用的秒数。
	}{
		{configured: 0, want: DefaultJWTExpiresInSeconds},
		{configured: -1, want: DefaultJWTExpiresInSeconds},
		{configured: 90, want: 90},
		{configured: MaxJWTExpiresInSeconds + 1, want: MaxJWTExpiresInSeconds},
	}
	for _, tt := range tests {
		if got := JWTExpiresInSeconds(tt.configured); got != tt.want {
			t.Fatalf("JWTExpiresInSeconds(%d)=%d，期望=%d", tt.configured, got, tt.want)
		}
	}
}

// TestProfileCacheTTLSecondsAppliesRuntimeBounds 确保热加载或测试直装也不会产生永久缓存或 duration 溢出。
func TestProfileCacheTTLSecondsAppliesRuntimeBounds(t *testing.T) {
	tests := []struct {
		configured int64 // configured 是运行时快照中的原始秒数。
		want       int64 // want 是缓存写入实际使用的秒数。
	}{
		{configured: 0, want: DefaultProfileCacheTTLSeconds},
		{configured: -1, want: DefaultProfileCacheTTLSeconds},
		{configured: 300, want: 300},
		{configured: MaxProfileCacheTTLSeconds + 1, want: MaxProfileCacheTTLSeconds},
	}
	for _, tt := range tests {
		if got := ProfileCacheTTLSeconds(tt.configured); got != tt.want {
			t.Fatalf("ProfileCacheTTLSeconds(%d)=%d，期望=%d", tt.configured, got, tt.want)
		}
	}
}
