package auth

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"

	codes "api/common/codes"
	"api/internal/config"
	"api/internal/svc"

	"github.com/Is999/go-utils/errors"
	"github.com/alicebob/miniredis/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"
)

// TestAuthRateLimitResultMapsRedisFailure 验证限流依赖故障返回可重试的 Redis 503 业务码。
func TestAuthRateLimitResultMapsRedisFailure(t *testing.T) {
	result := authRateLimitResult(errors.New("Redis unavailable"))
	if result == nil || result.Code != codes.RedisUnavailable || !result.IsFailure() {
		t.Fatalf("authRateLimitResult()=%+v，期望 RedisUnavailable", result)
	}
}

// TestCheckAuthRateLimitLocksAfterMaxAttempts 确保认证入口超过阈值后进入锁定状态。
func TestCheckAuthRateLimitLocksAfterMaxAttempts(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()

	logicObj := newAuthLogicForRateLimit(client)
	cfg := config.AuthRateLimitConfig{
		Enabled:       true,
		WindowSeconds: 60,
		MaxAttempts:   1,
		LockSeconds:   60,
	}
	if err := logicObj.checkAuthRateLimit(authRateLimitActionLoginIP, "127.0.0.1", cfg); err != nil {
		t.Fatalf("first checkAuthRateLimit() error = %v", err)
	}
	countKey, lockKey := logicObj.authRateLimitKeys(authRateLimitActionLoginIP, "127.0.0.1")
	if ttl := client.TTL(context.Background(), countKey).Val(); ttl <= 0 {
		t.Fatalf("rate limit count ttl = %v, want positive", ttl)
	}
	if countKey != "app:site-a:auth:rate_limit:{login_ip:6b8ec6a8e00d090c9fff080a6b221eb4588748205bbf51cc9e220e5aab709e3b}:count" ||
		lockKey != "app:site-a:auth:rate_limit:{login_ip:6b8ec6a8e00d090c9fff080a6b221eb4588748205bbf51cc9e220e5aab709e3b}:lock" {
		t.Fatalf("rate limit keys = %q %q, want same hash tag", countKey, lockKey)
	}
	err := logicObj.checkAuthRateLimit(authRateLimitActionLoginIP, "127.0.0.1", cfg)
	if !errors.Is(err, ErrAuthRateLimited) {
		t.Fatalf("second checkAuthRateLimit() error = %v, want ErrAuthRateLimited", err)
	}
	if exists := client.Exists(context.Background(), countKey).Val(); exists != 0 {
		t.Fatalf("rate limit count exists = %d, want deleted after lock", exists)
	}
}

// TestCheckAuthRateLimitConcurrentBoundary 确保并发请求不会越过原子最大尝试次数。
func TestCheckAuthRateLimitConcurrentBoundary(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()
	logicObj := newAuthLogicForRateLimit(client)
	cfg := config.AuthRateLimitConfig{Enabled: true, WindowSeconds: 60, MaxAttempts: 10, LockSeconds: 60}

	// 50 次普通并发争用 10 次额度，非限流错误需单独计数，不能混入正常拒绝。
	var allowed atomic.Int64
	var unexpected atomic.Int64
	var workers sync.WaitGroup
	for index := 0; index < 50; index++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			err := logicObj.checkAuthRateLimit(authRateLimitActionLoginIP, "127.0.0.2", cfg)
			switch {
			case err == nil:
				allowed.Add(1)
			case !errors.Is(err, ErrAuthRateLimited):
				unexpected.Add(1)
			}
		}()
	}
	workers.Wait()
	if unexpected.Load() != 0 {
		t.Fatalf("unexpected errors = %d, want 0", unexpected.Load())
	}
	if allowed.Load() != int64(cfg.MaxAttempts) {
		t.Fatalf("allowed requests = %d, want %d", allowed.Load(), cfg.MaxAttempts)
	}
}

// TestClearAuthRateLimitRemovesCountAndLock 确保登录成功后可以清理当前主体的限流状态。
func TestClearAuthRateLimitRemovesCountAndLock(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()

	logicObj := newAuthLogicForRateLimit(client)
	cfg := config.AuthRateLimitConfig{
		Enabled:       true,
		WindowSeconds: 60,
		MaxAttempts:   1,
		LockSeconds:   60,
	}
	subject := "demo_user"
	_ = logicObj.checkAuthRateLimit(authRateLimitActionLoginIdentity, subject, cfg)
	_ = logicObj.checkAuthRateLimit(authRateLimitActionLoginIdentity, subject, cfg)
	if err := logicObj.clearAuthRateLimit(authRateLimitActionLoginIdentity, subject); err != nil {
		t.Fatalf("clearAuthRateLimit() error = %v", err)
	}

	if err := logicObj.checkAuthRateLimit(authRateLimitActionLoginIdentity, subject, cfg); err != nil {
		t.Fatalf("checkAuthRateLimit() after clear error = %v", err)
	}
}

// TestClearAuthRateLimitFailureIsObservable 分别验证 Redis 删除错误和失败指标入口，不模拟完整登录调用。
func TestClearAuthRateLimitFailureIsObservable(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1})
	t.Cleanup(func() {
		_ = client.Close()
	})
	logicObj := newAuthLogicForRateLimit(client)
	if err := RegisterMetrics(); err != nil {
		t.Fatalf("RegisterMetrics() error = %v", err)
	}
	before := authCleanupMetricValue(t, authRateLimitActionLoginIP)
	server.Close()
	if err := logicObj.clearAuthRateLimit(authRateLimitActionLoginIP, "127.0.0.1"); err == nil {
		t.Fatal("clearAuthRateLimit() error = nil, want Redis failure")
	}
	// 生产登录调用方负责记指标，本用例显式调用以限定断言范围。
	recordAuthRateLimitCleanupFailure(authRateLimitActionLoginIP)
	after := authCleanupMetricValue(t, authRateLimitActionLoginIP)
	if after != before+1 {
		t.Fatalf("cleanup failure metric = %v, want %v", after, before+1)
	}
}

// authCleanupMetricValue 从默认注册表读取固定动作的限流清理失败计数。
func authCleanupMetricValue(t *testing.T, action string) float64 {
	t.Helper()
	families, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		t.Fatalf("Gather() error = %v", err)
	}
	for _, family := range families {
		if family.GetName() != "api_auth_rate_limit_cleanup_failures_total" {
			continue
		}
		for _, metric := range family.GetMetric() {
			for _, label := range metric.GetLabel() {
				if label.GetName() == "action" && label.GetValue() == action {
					return metric.GetCounter().GetValue()
				}
			}
		}
	}
	return 0
}

// newAuthLogicForRateLimit 提供固定 AppID 和 HMAC 密钥，使限流键可复算且不保存原始身份。
func newAuthLogicForRateLimit(client redis.UniversalClient) *AuthLogic {
	return NewAuthLogic(context.Background(), svc.NewServiceContext(config.Config{
		AppID:  "site-a",
		AppKey: "rate-limit-secret",
	}, "v1", svc.Dependencies{Rds: client}))
}
