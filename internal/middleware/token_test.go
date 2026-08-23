package middleware

import (
	"context"
	"strings"
	"testing"
	"time"

	keys "api/common/rediskeys"
	"api/common/runtimecfg"
	"api/internal/config"
	"api/internal/svc"

	"github.com/Is999/go-utils/errors"
	"github.com/alicebob/miniredis/v2"
	"github.com/golang-jwt/jwt/v4"
	"github.com/redis/go-redis/v9"
)

// tokenTestIssuer 是测试 token 与运行配置共享的签发方。
const tokenTestIssuer = "api"

// useTestAppID 为当前测试注入 Redis key 命名空间。
func useTestAppID(t *testing.T, appID string) {
	t.Helper()
	prev := runtimecfg.Get()
	runtimecfg.Set(runtimecfg.Snapshot{AppID: appID})
	t.Cleanup(func() {
		runtimecfg.Restore(prev)
	})
}

// TestBearerToken 验证标准 Bearer token 提取。
func TestBearerToken(t *testing.T) {
	token, err := bearerToken("Bearer abc.def")
	if err != nil {
		t.Fatalf("bearerToken() error = %v", err)
	}
	if token != "abc.def" {
		t.Fatalf("bearerToken() = %q, want abc.def", token)
	}
}

// TestBearerTokenMissing 验证缺失 Bearer 前缀时返回错误。
func TestBearerTokenMissing(t *testing.T) {
	if _, err := bearerToken("Basic abc"); err == nil {
		t.Fatalf("bearerToken(Basic) error = nil, want error")
	}
}

// TestBearerTokenRejectsOversizedHeader 验证超长 JWT 在解析和 HMAC 前被拒绝。
func TestBearerTokenRejectsOversizedHeader(t *testing.T) {
	if _, err := bearerToken("Bearer " + strings.Repeat("a", maxBearerTokenBytes+1)); !errors.Is(err, errInvalidToken) {
		t.Fatalf("oversized bearer error = %v, want errInvalidToken", err)
	}
}

// TestVerifyUserTokenRejectsAppIDMismatch 确保 token 不能跨 AppID 复用。
func TestVerifyUserTokenRejectsAppIDMismatch(t *testing.T) {
	useTestAppID(t, "site-a")
	token := signedUserToken(t, "test-secret-please-change", "site-b")
	svcCtx := svc.NewServiceContext(tokenTestConfig("site-a"), "v1", svc.Dependencies{})

	if _, err := VerifyUserToken(context.Background(), svcCtx, token, false); !errors.Is(err, errInvalidToken) {
		t.Fatalf("VerifyUserToken() error = %v, want errInvalidToken", err)
	}
}

// TestVerifyUserTokenRejectsWhitespaceAppID 确保 token 的 app_id 与当前命名空间逐字节一致。
func TestVerifyUserTokenRejectsWhitespaceAppID(t *testing.T) {
	useTestAppID(t, "site-a")
	token := signedUserToken(t, "test-secret-please-change", " site-a ")
	svcCtx := svc.NewServiceContext(tokenTestConfig("site-a"), "v1", svc.Dependencies{})

	if _, err := VerifyUserToken(context.Background(), svcCtx, token, false); !errors.Is(err, errInvalidToken) {
		t.Fatalf("VerifyUserToken() error = %v, want errInvalidToken", err)
	}
}

// TestVerifyUserTokenRejectsRuntimeAppIDMismatch 确保会话 key 只使用当前进程运行态命名空间。
func TestVerifyUserTokenRejectsRuntimeAppIDMismatch(t *testing.T) {
	useTestAppID(t, "site-b")
	token := signedUserToken(t, "test-secret-please-change", "site-a")
	svcCtx := svc.NewServiceContext(tokenTestConfig("site-a"), "v1", svc.Dependencies{})

	if _, err := VerifyUserToken(context.Background(), svcCtx, token, false); !errors.Is(err, errInvalidToken) {
		t.Fatalf("VerifyUserToken() error = %v, want errInvalidToken", err)
	}
}

// TestVerifyUserTokenPreservesSessionIdentityAndIndex 验证有效鉴权返回 sid/jti，并保留现有会话索引。
func TestVerifyUserTokenPreservesSessionIdentityAndIndex(t *testing.T) {
	useTestAppID(t, "site-a")
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()

	token := signedUserToken(t, "test-secret-please-change", "site-a")
	svcCtx := svc.NewServiceContext(tokenTestConfig("site-a"), "v1", svc.Dependencies{Rds: client})
	seedUserSession(t, client, 42, "testsid", token, 1)

	identity, err := VerifyUserToken(context.Background(), svcCtx, token, true)
	if err != nil {
		t.Fatalf("VerifyUserToken() error = %v", err)
	}
	if identity.JTI != "testjti" {
		t.Fatalf("identity.JTI = %q, want testjti", identity.JTI)
	}
	if identity.SessionID != "testsid" {
		t.Fatalf("identity.SessionID = %q, want testsid", identity.SessionID)
	}
	exists, err := client.Exists(context.Background(), keys.UserSessionIndexKey(42)).Result()
	if err != nil {
		t.Fatalf("Exists(index) error = %v", err)
	}
	if exists != 1 {
		t.Fatalf("session index exists = %d, want 1", exists)
	}
}

// TestVerifyUserTokenKeepsSnowflakeSubjectPrecision 确保字符串 sub 不会丢失超过 2^53 的雪花 ID 精度。
func TestVerifyUserTokenKeepsSnowflakeSubjectPrecision(t *testing.T) {
	useTestAppID(t, "site-a")
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()

	const userID int64 = 9_007_199_254_740_993
	token := signedUserTokenForSubject(t, "test-secret-please-change", "site-a", "9007199254740993")
	seedUserSession(t, client, userID, "testsid", token, 1)
	svcCtx := svc.NewServiceContext(tokenTestConfig("site-a"), "v1", svc.Dependencies{Rds: client})

	identity, err := VerifyUserToken(context.Background(), svcCtx, token, true)
	if err != nil {
		t.Fatalf("VerifyUserToken() error = %v", err)
	}
	if identity.UserID != userID {
		t.Fatalf("identity.UserID = %d, want %d", identity.UserID, userID)
	}
}

// TestVerifyUserTokenRejectsNumericSnowflakeSubject 确保 JWT sub 只接受当前签发的十进制字符串。
func TestVerifyUserTokenRejectsNumericSnowflakeSubject(t *testing.T) {
	useTestAppID(t, "site-a")
	const userID int64 = 9_007_199_254_740_993
	token := signedUserTokenForSubject(t, "test-secret-please-change", "site-a", userID)
	svcCtx := svc.NewServiceContext(tokenTestConfig("site-a"), "v1", svc.Dependencies{})

	if _, err := VerifyUserToken(context.Background(), svcCtx, token, false); !errors.Is(err, errInvalidToken) {
		t.Fatalf("VerifyUserToken() error = %v, want errInvalidToken", err)
	}
}

// TestVerifyUserTokenRejectsWhitespaceSubject 确保用户 ID 声明只接受签发器生成的规范十进制字符串。
func TestVerifyUserTokenRejectsWhitespaceSubject(t *testing.T) {
	useTestAppID(t, "site-a")
	token := signedUserTokenForSubject(t, "test-secret-please-change", "site-a", " 42 ")
	svcCtx := svc.NewServiceContext(tokenTestConfig("site-a"), "v1", svc.Dependencies{})

	if _, err := VerifyUserToken(context.Background(), svcCtx, token, false); !errors.Is(err, errInvalidToken) {
		t.Fatalf("VerifyUserToken() error = %v, want errInvalidToken", err)
	}
}

// TestVerifyUserTokenKeepsUint64AuthVersionPrecision 确保 auth_version 使用 json.Number 无损解析。
func TestVerifyUserTokenKeepsUint64AuthVersionPrecision(t *testing.T) {
	useTestAppID(t, "site-a")
	claims := jwt.MapClaims{
		"sub":          "42",
		"username":     "demo",
		"sid":          "testsid",
		"jti":          "testjti",
		"iss":          tokenTestIssuer,
		"app_id":       "site-a",
		"auth_version": ^uint64(0),
		"exp":          time.Now().Add(time.Hour).Unix(),
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("test-secret-please-change"))
	if err != nil {
		t.Fatalf("SignedString() error = %v", err)
	}
	identity, err := VerifyUserToken(context.Background(), svc.NewServiceContext(tokenTestConfig("site-a"), "v1", svc.Dependencies{}), token, false)
	if err != nil {
		t.Fatalf("VerifyUserToken() error = %v", err)
	}
	if identity.AuthVersion != ^uint64(0) {
		t.Fatalf("identity.AuthVersion = %d, want max uint64", identity.AuthVersion)
	}
}

// TestVerifyUserTokenReturnsIdentityOnSessionExpired 确保 session 失效时仍返回已校验 token 身份。
func TestVerifyUserTokenReturnsIdentityOnSessionExpired(t *testing.T) {
	useTestAppID(t, "site-a")
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()

	token := signedUserToken(t, "test-secret-please-change", "site-a")
	svcCtx := svc.NewServiceContext(tokenTestConfig("site-a"), "v1", svc.Dependencies{Rds: client})

	identity, err := VerifyUserToken(context.Background(), svcCtx, token, true)
	if !errors.Is(err, errSessionExpired) {
		t.Fatalf("VerifyUserToken() error = %v, want errSessionExpired", err)
	}
	if identity == nil || identity.UserID != 42 || identity.UserName != "demo" || identity.SessionID != "testsid" || identity.JTI != "testjti" {
		t.Fatalf("identity = %+v, want parsed token identity", identity)
	}
}

// TestVerifyUserTokenRejectsStaleRedisAuthVersion 确保 Redis 认证版本推进后旧 JWT 立即失效。
func TestVerifyUserTokenRejectsStaleRedisAuthVersion(t *testing.T) {
	useTestAppID(t, "site-a")
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()

	token := signedUserToken(t, "test-secret-please-change", "site-a")
	seedUserSession(t, client, 42, "testsid", token, 2)
	svcCtx := svc.NewServiceContext(tokenTestConfig("site-a"), "v1", svc.Dependencies{Rds: client})
	identity, err := VerifyUserToken(context.Background(), svcCtx, token, true)
	if !errors.Is(err, errSessionExpired) {
		t.Fatalf("VerifyUserToken() error = %v, want errSessionExpired", err)
	}
	if identity == nil || identity.AuthVersion != 1 {
		t.Fatalf("identity = %+v, want parsed auth version 1", identity)
	}
}

// TestVerifyUserTokenRejectsEmptyAppIDClaim 确保 token 必须携带明确 app_id。
func TestVerifyUserTokenRejectsEmptyAppIDClaim(t *testing.T) {
	useTestAppID(t, "site-a")
	token := signedUserToken(t, "test-secret-please-change", "")
	svcCtx := svc.NewServiceContext(tokenTestConfig("site-a"), "v1", svc.Dependencies{})

	if _, err := VerifyUserToken(context.Background(), svcCtx, token, false); !errors.Is(err, errInvalidToken) {
		t.Fatalf("VerifyUserToken() error = %v, want errInvalidToken", err)
	}
}

// TestVerifyUserTokenRejectsMissingSessionID 确保 token 必须携带 sid。
func TestVerifyUserTokenRejectsMissingSessionID(t *testing.T) {
	useTestAppID(t, "site-a")
	claims := jwt.MapClaims{
		"sub":          "42",
		"username":     "demo",
		"jti":          "testjti",
		"iss":          tokenTestIssuer,
		"app_id":       "site-a",
		"auth_version": 1,
		"exp":          time.Now().Add(time.Hour).Unix(),
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte("test-secret-please-change"))
	if err != nil {
		t.Fatalf("SignedString() error = %v", err)
	}
	svcCtx := svc.NewServiceContext(tokenTestConfig("site-a"), "v1", svc.Dependencies{})
	if _, err = VerifyUserToken(context.Background(), svcCtx, token, false); !errors.Is(err, errInvalidToken) {
		t.Fatalf("VerifyUserToken() error = %v, want errInvalidToken", err)
	}
}

// TestVerifyUserTokenRejectsUnexpectedIssuer 确保 token 签发方必须与运行配置一致。
func TestVerifyUserTokenRejectsUnexpectedIssuer(t *testing.T) {
	useTestAppID(t, "site-a")
	svcCtx := svc.NewServiceContext(tokenTestConfig("site-a"), "v1", svc.Dependencies{})
	for _, issuer := range []string{"", "other-api", " api "} {
		token := signedUserTokenWith(t, jwt.SigningMethodHS256, "test-secret-please-change", "site-a", issuer)
		if _, err := VerifyUserToken(context.Background(), svcCtx, token, false); !errors.Is(err, errInvalidToken) {
			t.Fatalf("VerifyUserToken(issuer=%q) error = %v, want errInvalidToken", issuer, err)
		}
	}
}

// TestVerifyUserTokenRejectsOtherHMACAlgorithms 确保 JWT 只接受 HS256。
func TestVerifyUserTokenRejectsOtherHMACAlgorithms(t *testing.T) {
	useTestAppID(t, "site-a")
	token := signedUserTokenWith(t, jwt.SigningMethodHS384, "test-secret-please-change", "site-a", tokenTestIssuer)
	svcCtx := svc.NewServiceContext(tokenTestConfig("site-a"), "v1", svc.Dependencies{})
	if _, err := VerifyUserToken(context.Background(), svcCtx, token, false); !errors.Is(err, errInvalidToken) {
		t.Fatalf("VerifyUserToken(HS384) error = %v, want errInvalidToken", err)
	}
}

// tokenTestConfig 返回 JWT 鉴权测试使用的运行配置。
func tokenTestConfig(appID string) config.Config {
	return config.Config{
		AppID:     appID,
		JwtSecret: "test-secret-please-change",
		Auth:      config.AuthConfig{Issuer: tokenTestIssuer},
	}
}

// signedUserToken 使用默认用户 ID 签发当前 HS256 契约的测试 token。
func signedUserToken(t *testing.T, secret string, appID string) string {
	return signedUserTokenForSubject(t, secret, appID, "42")
}

// signedUserTokenForSubject 使用指定 sub 生成 HS256 测试 token。
func signedUserTokenForSubject(t *testing.T, secret string, appID string, subject any) string {
	return signedUserTokenWithSubject(t, jwt.SigningMethodHS256, secret, appID, tokenTestIssuer, subject)
}

// signedUserTokenWith 使用指定算法和签发方生成测试 token。
func signedUserTokenWith(t *testing.T, method jwt.SigningMethod, secret string, appID string, issuer string) string {
	return signedUserTokenWithSubject(t, method, secret, appID, issuer, "42")
}

// signedUserTokenWithSubject 使用指定算法、签发方和 sub 生成测试 token。
func signedUserTokenWithSubject(t *testing.T, method jwt.SigningMethod, secret string, appID string, issuer string, subject any) string {
	t.Helper()
	claims := jwt.MapClaims{
		"sub":          subject,
		"username":     "demo",
		"sid":          "testsid",
		"jti":          "testjti",
		"iss":          issuer,
		"app_id":       appID,
		"auth_version": 1,
		"exp":          time.Now().Add(time.Hour).Unix(),
	}
	token, err := jwt.NewWithClaims(method, claims).SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("SignedString() error = %v", err)
	}
	return token
}

// seedUserSession 写入会话校验测试需要的 Hash、索引和认证版本。
func seedUserSession(t *testing.T, client redis.UniversalClient, userID int64, sessionID string, token string, authVersion uint64) {
	t.Helper()
	ctx := context.Background()
	if err := client.HSet(ctx, keys.UserSessionHashKey(userID), sessionID, token).Err(); err != nil {
		t.Fatalf("HSet(session) error = %v", err)
	}
	if err := client.ZAdd(ctx, keys.UserSessionIndexKey(userID), redis.Z{
		Score:  float64(time.Now().Add(time.Hour).UnixMilli()),
		Member: sessionID,
	}).Err(); err != nil {
		t.Fatalf("ZAdd(session index) error = %v", err)
	}
	if err := client.Set(ctx, keys.UserSessionAuthVersionKey(userID), authVersion, time.Hour).Err(); err != nil {
		t.Fatalf("Set(auth version) error = %v", err)
	}
}
