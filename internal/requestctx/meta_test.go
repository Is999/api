package requestctx

import (
	"context"
	"testing"
	"time"

	"api/internal/types"
)

// TestRefreshLatencyUsesStartedAt 验证按创建时间刷新请求耗时。
func TestRefreshLatencyUsesStartedAt(t *testing.T) {
	ctx, meta := New(context.Background())
	meta.StartedAt = time.Now().Add(-5 * time.Millisecond)

	RefreshLatency(ctx)

	if meta.LatencyMS <= 0 {
		t.Fatalf("RefreshLatency() latency_ms = %d, want positive", meta.LatencyMS)
	}
}

// TestSetLatencyRoundsSubMillisecondToOne 验证亚毫秒耗时按 1ms 记录。
func TestSetLatencyRoundsSubMillisecondToOne(t *testing.T) {
	ctx, meta := New(context.Background())

	SetLatency(ctx, time.Nanosecond)

	if meta.LatencyMS != 1 {
		t.Fatalf("SetLatency(1ns) latency_ms = %d, want 1", meta.LatencyMS)
	}
}

// TestSetSessionID 保存中间件已经解析出的会话 ID。
func TestSetSessionID(t *testing.T) {
	ctx, meta := New(context.Background())
	SetSessionID(ctx, " session-id ")
	if meta.SessionID != "session-id" {
		t.Fatalf("SessionID = %q, want session-id", meta.SessionID)
	}
}

// TestAuthUserContextUsesCopies 确保运行适配器和下游拿到的对象都不能改写请求快照。
func TestAuthUserContextUsesCopies(t *testing.T) {
	ctx, _ := New(t.Context())
	input := &AuthUser{
		Profile:     types.UserProfile{ID: 42, Username: "demo", Nickname: "before"},
		AuthVersion: 3,
	}

	SetAuthUser(ctx, input)
	input.Profile.Nickname = "changed-input"
	input.AuthVersion = 4

	got := AuthUserFromContext(ctx)
	if got == nil || got.Profile.Nickname != "before" || got.AuthVersion != 3 {
		t.Fatalf("AuthUserFromContext() = %+v, want stored value copy", got)
	}
	got.Profile.Nickname = "changed-output"
	got.AuthVersion = 5
	if again := AuthUserFromContext(ctx); again == nil || again.Profile.Nickname != "before" || again.AuthVersion != 3 {
		t.Fatalf("second AuthUserFromContext() = %+v, want independent copy", again)
	}

	SetAuthUser(ctx, nil)
	if got := AuthUserFromContext(ctx); got != nil {
		t.Fatalf("AuthUserFromContext() after clear = %+v, want nil", got)
	}
}
