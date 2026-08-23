package middleware

import (
	"testing"

	"api/internal/requestctx"
	"api/internal/types"
)

// TestAuthVersionMatches 确保主库版本先提交、Redis 尚未清理时旧 JWT 仍会 fail-close。
func TestAuthVersionMatches(t *testing.T) {
	user := &requestctx.AuthUser{Profile: types.UserProfile{ID: 42, Username: "demo"}, AuthVersion: 2}
	if authVersionMatches(user, &UserTokenIdentity{UserID: 42, AuthVersion: 1}) {
		t.Fatal("authVersionMatches() = true for stale JWT")
	}
	if !authVersionMatches(user, &UserTokenIdentity{UserID: 42, AuthVersion: 2}) {
		t.Fatal("authVersionMatches() = false for current JWT")
	}
	if authVersionMatches(user, &UserTokenIdentity{UserID: 43, AuthVersion: 2}) {
		t.Fatal("authVersionMatches() = true for mismatched user ID")
	}
	if authVersionMatches(&requestctx.AuthUser{Profile: types.UserProfile{ID: 42, Username: "demo"}}, &UserTokenIdentity{UserID: 42}) {
		t.Fatal("authVersionMatches() = true for zero auth version")
	}
}
