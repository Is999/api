package middleware

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	keys "api/common/rediskeys"
	"api/common/runtimecfg"
	"api/internal/config"
	"api/internal/requestctx"
	"api/internal/svc"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// TestReplayTTLIncludesFutureWindow 验证未来时间戳的防重放记录不会在签名仍有效时提前过期。
func TestReplayTTLIncludesFutureWindow(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cfg := config.Config{AppID: "site-a"}
	previous := runtimecfg.Get()
	runtimecfg.Set(runtimecfg.Snapshot{AppID: cfg.AppID})
	t.Cleanup(func() { runtimecfg.Restore(previous) })
	middleware := NewSignatureMiddleware(svc.NewServiceContext(cfg, "test-version", svc.Dependencies{Rds: client}), nil)
	for _, offset := range []time.Duration{-4 * time.Minute, 0, 4 * time.Minute} {
		t.Run(offset.String(), func(t *testing.T) {
			req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
			req.Header.Set(requestctx.HeaderTimestamp, strconv.FormatInt(time.Now().Add(offset).Unix(), 10))
			_, expiresAt, err := requestTimestamp(req)
			if err != nil {
				t.Fatal(err)
			}
			traceID := fmt.Sprintf("replay-%d", offset)
			if err := middleware.markRequestVerified(req, cfg.AppID, traceID, expiresAt); err != nil {
				t.Fatal(err)
			}
			ttl, err := client.PTTL(t.Context(), keys.WithPrefix(fmt.Sprintf(keys.SignatureReplayRequest, traceID))).Result()
			if err != nil {
				t.Fatal(err)
			}
			// 只容忍本地命令耗时和 Redis 毫秒精度，截止点必须来自已校验时间戳。
			if required := time.Until(expiresAt); ttl < required-time.Second || ttl > required+time.Second {
				t.Fatalf("防重放期限不匹配: ttl=%s required=%s", ttl, required)
			}
			if offset > 0 {
				server.FastForward(6 * time.Minute)
				if err := middleware.markRequestVerified(req, cfg.AppID, traceID, expiresAt); err == nil {
					t.Fatal("原固定 TTL 后，仍有效的未来签名被允许再次登记")
				}
			}
		})
	}
}

// TestReplayRegistrationRejectsExpiredDeadline 验签执行耗时跨过截止点时，拒绝写入永久或负 TTL 标记。
func TestReplayRegistrationRejectsExpiredDeadline(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	cfg := config.Config{AppID: "site-a"}
	previous := runtimecfg.Get()
	runtimecfg.Set(runtimecfg.Snapshot{AppID: cfg.AppID})
	t.Cleanup(func() { runtimecfg.Restore(previous) })
	middleware := NewSignatureMiddleware(svc.NewServiceContext(cfg, "test-version", svc.Dependencies{Rds: client}), nil)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	if err := middleware.markRequestVerified(req, cfg.AppID, "expired-trace", time.Now().Add(-time.Second)); err == nil {
		t.Fatal("过期签名不应登记")
	}
	if len(server.Keys()) != 0 {
		t.Fatalf("过期签名写入缓存: %v", server.Keys())
	}
}
