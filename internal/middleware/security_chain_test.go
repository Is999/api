package middleware

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	codes "api/common/codes"
	"api/common/runtimecfg"
	"api/internal/config"
	authlogic "api/internal/logic/auth"
	"api/internal/requestctx"
	"api/internal/routealias"
	"api/internal/security"
	"api/internal/svc"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// securityOrderRuntime 记录安全对象查找次数，用于校验链路顺序和单请求复用。
type securityOrderRuntime struct {
	signer       security.Signer  // signer 是已预编译的双向签名器。
	cryptor      security.Cryptor // cryptor 是已预编译的双向加解密器。
	signerCalls  int              // signerCalls 记录单次请求的签名器查找次数。
	cryptorCalls int              // cryptorCalls 只有安全链进入解密阶段才增加。
}

// ActiveUser 不应在公开安全链路测试中被调用。
func (r *securityOrderRuntime) ActiveUser(context.Context, int64) (*requestctx.AuthUser, error) {
	return nil, nil
}

// RecordAuthEvent 忽略测试事件，本用例只断言请求阶段顺序。
func (r *securityOrderRuntime) RecordAuthEvent(context.Context, RuntimeAuthEvent) {}

// SecurityRoute 开启签名和加密，让请求必须经过完整安全链。
func (r *securityOrderRuntime) SecurityRoute(context.Context, string) (RuntimeSecurityRoute, error) {
	return RuntimeSecurityRoute{SignEnabled: true, CryptoEnabled: true}, nil
}

// Signer 返回已预编译的双向签名器并记录查找次数。
func (r *securityOrderRuntime) Signer(context.Context, string, string, string, string) (security.Signer, string, error) {
	r.signerCalls++
	return r.signer, "v1", nil
}

// Cryptor 记录加解密器查找，伪造签名被拒绝时次数必须保持为零。
func (r *securityOrderRuntime) Cryptor(context.Context, string, string, string, string) (security.Cryptor, string, error) {
	r.cryptorCalls++
	return r.cryptor, "v1", nil
}

// TestAuthSecurityChainVerifiesBeforeDecrypt 确保伪造签名的密文请求不会获得任何解密处理。
func TestAuthSecurityChainVerifiesBeforeDecrypt(t *testing.T) {
	keys := mustSecurityTestRSAKeys(t)
	signer, err := security.NewRSASigner("", keys.publicPEM)
	if err != nil {
		t.Fatalf("NewRSASigner() error = %v", err)
	}
	runtime := &securityOrderRuntime{signer: signer, cryptor: noopCryptor{}}
	svcCtx := svc.NewServiceContext(securityEnabledConfig(t), "test-version", svc.Dependencies{})
	nextCalled := false
	handler := NewAuthMiddleware(svcCtx, runtime).PublicHandle(func(http.ResponseWriter, *http.Request) {
		nextCalled = true
	}, routealias.AuthLogin)
	request := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(`{"identityType":"username","identityValue":"YQ==","password":"Yg==","sign":"invalid"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-App-Id", base64.StdEncoding.EncodeToString([]byte("demo-app")))
	request.Header.Set("X-Signature", security.SignatureTypeRSA)
	request.Header.Set("X-Crypto", security.CryptoTypeAES)
	request.Header.Set("X-Cipher", security.EncodeCipherParams([]string{"identityValue", "password"}))
	request.Header.Set(requestctx.HeaderTraceID, "0123456789abcdef0123456789abcdef")
	request.Header.Set(requestctx.HeaderTimestamp, strconv.FormatInt(time.Now().Unix(), 10))
	recorder := httptest.NewRecorder()

	handler(recorder, request)

	if nextCalled {
		t.Fatal("伪造签名不应进入业务处理器")
	}
	if runtime.cryptorCalls != 0 {
		t.Fatalf("验签失败后 Cryptor 调用次数 = %d, want 0", runtime.cryptorCalls)
	}
	var response struct {
		Code int `json:"code"` // Code 是签名拒绝场景返回的业务码。
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("Unmarshal(response) error = %v", err)
	}
	if response.Code != codes.SecurityRequestRejected {
		t.Fatalf("response code = %d, want %d", response.Code, codes.SecurityRequestRejected)
	}
}

// TestAuthSecurityChainReusesResolvedObjects 确保双向安全策略在单次请求内各只选择一次签名器和加解密器。
func TestAuthSecurityChainReusesResolvedObjects(t *testing.T) {
	// 真实登录策略同时包含请求验签、请求解密、响应加密和回签。
	const traceID = "0123456789abcdef0123456789abcdef"
	cfg := securityEnabledConfig(t)
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() {
		_ = client.Close()
	})
	previous := runtimecfg.Get()
	runtimecfg.Set(runtimecfg.Snapshot{AppID: cfg.AppID})
	t.Cleanup(func() {
		runtimecfg.Restore(previous)
	})

	signer, err := security.NewHMACSigner(cfg.Security.SecretKey.Versions[0].AESKey)
	if err != nil {
		t.Fatalf("NewHMACSigner() error = %v", err)
	}
	runtime := &securityOrderRuntime{signer: signer, cryptor: noopCryptor{}}
	svcCtx := svc.NewServiceContext(cfg, "test-version", svc.Dependencies{Rds: client})
	policy, ok := security.LookupRoutePolicy(string(routealias.AuthLogin))
	if !ok {
		t.Fatal("auth.login security policy missing")
	}
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	params := map[string]any{
		"identityType":  "username",
		"identityValue": "demo-user",
		"password":      "demo-password",
	}
	sign, err := signer.Sign(security.BuildSignString(params, policy.RequestSign, traceID, timestamp, cfg.AppID))
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	params["sign"] = sign
	body, err := json.Marshal(params)
	if err != nil {
		t.Fatalf("Marshal(request) error = %v", err)
	}

	// 运行时记录对象查找次数，业务响应包含需要加密的敏感字段。
	nextCalled := false
	handler := NewAuthMiddleware(svcCtx, runtime).PublicHandle(func(w http.ResponseWriter, _ *http.Request) {
		nextCalled = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":true,"code":1,"data":{"token":"token","expiresAt":1700000000,"user":{"email":"person@example.test","phone":"13800000000"}}}`))
	}, routealias.AuthLogin)
	request := httptest.NewRequest(http.MethodPost, "/api/auth/login", strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-App-Id", base64.StdEncoding.EncodeToString([]byte(cfg.AppID)))
	request.Header.Set("X-Signature", security.SignatureTypeHMAC)
	request.Header.Set("X-Crypto", security.CryptoTypeAES)
	request.Header.Set("X-Cipher", security.EncodeCipherParams(policy.RequestCipher))
	request.Header.Set(requestctx.HeaderTraceID, traceID)
	request.Header.Set(requestctx.HeaderTimestamp, timestamp)
	recorder := httptest.NewRecorder()

	// 单次请求内签名器和加解密器都只能解析一次并双向复用。
	handler(recorder, request)

	if !nextCalled {
		t.Fatalf("安全请求未进入业务处理器: %s", recorder.Body.String())
	}
	if runtime.signerCalls != 1 || runtime.cryptorCalls != 1 {
		t.Fatalf("安全对象查找次数 signer=%d cryptor=%d, want 1/1", runtime.signerCalls, runtime.cryptorCalls)
	}
	if recorder.Header().Get(secretKeyVersionHeader) != "v1" {
		t.Fatalf("%s = %q, want v1", secretKeyVersionHeader, recorder.Header().Get(secretKeyVersionHeader))
	}
}

// TestAuthSecurityChainSignsEncryptedResponse 确保响应回签覆盖最终密文，客户端可先验签再解密。
func TestAuthSecurityChainSignsEncryptedResponse(t *testing.T) {
	const traceID = "0123456789abcdef0123456789abcdef"
	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	cfg := securityEnabledConfig(t)
	svcCtx := newSecurityTestServiceContext(t, cfg, svc.Dependencies{})
	handler := newTestAuthMiddleware(svcCtx).PublicHandle(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":true,"code":1,"data":{"email":"person@example.test","phone":"13800000000"}}`))
	}, routealias.UserProfile)
	request := httptest.NewRequest(http.MethodGet, "/api/user/profile", nil)
	request.Header.Set("X-App-Id", base64.StdEncoding.EncodeToString([]byte(cfg.AppID)))
	request.Header.Set("X-Signature", security.SignatureTypeHMAC)
	request.Header.Set("X-Crypto", security.CryptoTypeAES)
	request.Header.Set(requestctx.HeaderTraceID, traceID)
	request.Header.Set(requestctx.HeaderTimestamp, timestamp)
	recorder := httptest.NewRecorder()

	handler(recorder, request)

	var envelope struct {
		Data map[string]any `json:"data"` // Data 保存需要先验签再解密的响应字段。
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("Unmarshal(response) error = %v; body=%s", err, recorder.Body.String())
	}
	signature := security.SignValueString(envelope.Data["sign"])
	if signature == "" {
		t.Fatalf("加密响应缺少回签: %s", recorder.Body.String())
	}
	policy, _ := security.LookupRoutePolicy(string(routealias.UserProfile))
	signer, err := security.NewHMACSigner(cfg.Security.SecretKey.Versions[0].AESKey)
	if err != nil {
		t.Fatalf("NewHMACSigner() error = %v", err)
	}
	// 客户端先使用线上密文复算签名，不能先改写成明文再验签。
	canonical := security.BuildSignString(envelope.Data, policy.ResponseSign, traceID, timestamp, cfg.AppID)
	if ok, err := signer.Verify(canonical, signature); err != nil || !ok {
		t.Fatalf("密文响应验签 = %t, %v; body=%s", ok, err, recorder.Body.String())
	}
	// 验签通过后再解密目标字段，检查协议顺序与业务值均保持一致。
	cipherObj, err := security.NewAESGCMCipher(cfg.Security.SecretKey.Versions[0].AESKey)
	if err != nil {
		t.Fatalf("NewAESGCMCipher() error = %v", err)
	}
	email, err := cipherObj.Decrypt(security.SignValueString(envelope.Data["email"]))
	if err != nil || email != "person@example.test" {
		t.Fatalf("Decrypt(email) = %q, %v", email, err)
	}
}

// securityTestRSAKeys 保存测试进程内复用的 PEM 文本，避免固化私钥或重复生成密钥。
type securityTestRSAKeys struct {
	privatePEM string // 配置为服务端请求解密和响应签名私钥
	publicPEM  string // 配置为用户请求验签和响应加密公钥
}

// generateSecurityTestRSAKeys 每个测试进程只生成一组 RSA 材料，所有用例只读共享 PEM 文本。
var generateSecurityTestRSAKeys = sync.OnceValues(func() (securityTestRSAKeys, error) {
	privateKey, err := rsa.GenerateKey(rand.Reader, security.MinRSAKeyBits)
	if err != nil {
		return securityTestRSAKeys{}, err
	}
	publicDER, err := x509.MarshalPKIXPublicKey(&privateKey.PublicKey)
	if err != nil {
		return securityTestRSAKeys{}, err
	}
	return securityTestRSAKeys{
		privatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(privateKey)})),
		publicPEM:  string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER})),
	}, nil
})

// mustSecurityTestRSAKeys 返回测试进程共享的 RSA 材料，生成失败时立即终止当前用例。
func mustSecurityTestRSAKeys(t *testing.T) securityTestRSAKeys {
	t.Helper()
	keys, err := generateSecurityTestRSAKeys()
	if err != nil {
		t.Fatalf("generateSecurityTestRSAKeys() error = %v", err)
	}
	return keys
}

// TestSignatureMiddlewareSkipsRouteWithoutSignPolicy 确保无签名策略的路由不要求安全请求头。
func TestSignatureMiddlewareSkipsRouteWithoutSignPolicy(t *testing.T) {
	svcCtx := svc.NewServiceContext(securityEnabledConfig(t), "test-version", svc.Dependencies{})
	middleware := newTestSignatureMiddleware(svcCtx)
	handler := middleware.Handle(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}, routealias.UserRuntimeSync)

	req := httptest.NewRequest(http.MethodPost, "/internal/users/1/runtime-sync", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
}

// TestCryptoMiddlewareSkipsRouteWithoutCipherPolicy 确保无加密策略的路由直接透传响应。
func TestCryptoMiddlewareSkipsRouteWithoutCipherPolicy(t *testing.T) {
	svcCtx := svc.NewServiceContext(securityEnabledConfig(t), "test-version", svc.Dependencies{})
	middleware := newTestCryptoMiddleware(svcCtx)
	handler := middleware.Handle(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", nil)
	req = bindRequestMeta(req, routealias.AuthLogout, nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNoContent)
	}
}

// TestSignatureMiddlewareDisabledSkipsTraceHeaders 确保仅开启加密时，签名中间件不要求 trace_id 和 timestamp。
func TestSignatureMiddlewareDisabledSkipsTraceHeaders(t *testing.T) {
	cfg := securityEnabledConfig(t)
	cfg.Security.SecretKey.SignStatus = 0
	svcCtx := svc.NewServiceContext(cfg, "test-version", svc.Dependencies{})
	called := false
	handler := newTestSignatureMiddleware(svcCtx).Handle(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	}, routealias.AuthLogin)
	request := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	recorder := httptest.NewRecorder()

	handler(recorder, request)

	if !called {
		t.Fatal("签名关闭时请求应进入业务处理器")
	}
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
}

// TestCryptoMiddlewareDisabledSkipsCipherPolicy 确保仅开启签名时，加密中间件不加工响应。
func TestCryptoMiddlewareDisabledSkipsCipherPolicy(t *testing.T) {
	cfg := securityEnabledConfig(t)
	cfg.Security.SecretKey.CryptoStatus = 0
	svcCtx := svc.NewServiceContext(cfg, "test-version", svc.Dependencies{})
	called := false
	handler := newTestCryptoMiddleware(svcCtx).Handle(func(w http.ResponseWriter, _ *http.Request) {
		called = true
		w.WriteHeader(http.StatusNoContent)
	})
	request := httptest.NewRequest(http.MethodPost, "/api/auth/login", nil)
	request = bindRequestMeta(request, routealias.AuthLogin, svcCtx)
	recorder := httptest.NewRecorder()

	handler(recorder, request)

	if !called {
		t.Fatal("加密关闭时请求应进入业务处理器")
	}
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
	if recorder.Header().Get("X-Cipher") != "" || recorder.Header().Get("X-Crypto") != "" {
		t.Fatal("加密关闭时响应不应包含加密协议头")
	}
}

// TestSignatureTraceIDRequiresCanonicalActiveTrace 确保签名标识格式固定且与实际链路一致。
func TestSignatureTraceIDRequiresCanonicalActiveTrace(t *testing.T) {
	const traceID = "0123456789abcdef0123456789abcdef"
	req := httptest.NewRequest(http.MethodPost, "/api/demo", nil)
	req.Header.Set(requestctx.HeaderTraceID, traceID)
	ctx, _ := requestctx.New(req.Context())
	requestctx.SetTrace(ctx, traceID, "0123456789abcdef")
	req = req.WithContext(ctx)

	got, err := signatureTraceID(req)
	if err != nil || got != traceID {
		t.Fatalf("signatureTraceID() = %q, %v, want %q", got, err, traceID)
	}

	req.Header.Set(requestctx.HeaderTraceID, "01234567-89ab-cdef-0123-456789abcdef")
	if _, err = signatureTraceID(req); err == nil || !strings.Contains(err.Error(), "32位小写十六进制") {
		t.Fatalf("UUID trace error = %v, want canonical format rejection", err)
	}

	req.Header.Set(requestctx.HeaderTraceID, " "+traceID)
	if _, err = signatureTraceID(req); err == nil || !strings.Contains(err.Error(), "32位小写十六进制") {
		t.Fatalf("whitespace trace error = %v, want canonical format rejection", err)
	}

	req.Header.Set(requestctx.HeaderTraceID, "fedcba9876543210fedcba9876543210")
	if _, err = signatureTraceID(req); err == nil || !strings.Contains(err.Error(), "实际链路不一致") {
		t.Fatalf("mismatched trace error = %v, want active trace rejection", err)
	}
}

// TestSecurityConfigConfiguredRequiresConcreteVersion 确保只有版本名而无密钥材料时不启用安全链。
func TestSecurityConfigConfiguredRequiresConcreteVersion(t *testing.T) {
	cfg := config.Config{
		AppID: "demo-app",
		Security: config.SecurityConfig{
			SecretKey: config.SecuritySecretKeyConfig{
				StableVersion: "v1",
			},
		},
	}
	svcCtx := svc.NewServiceContext(cfg, "test-version", svc.Dependencies{})

	if securityConfigConfigured(svcCtx) {
		t.Fatal("securityConfigConfigured() should ignore stable_version without key material")
	}
}

// TestSignatureMiddlewareRejectsUnsupportedSignatureTypes 确保未知签名算法失败关闭。
func TestSignatureMiddlewareRejectsUnsupportedSignatureTypes(t *testing.T) {
	middleware := newTestSignatureMiddleware(newSecurityTestServiceContext(t, securityEnabledConfig(t), svc.Dependencies{}))
	request := httptest.NewRequest(http.MethodPost, "/api/demo", nil)
	for _, signatureType := range []string{"UNKNOWN", "HMAC", "M", "MD5"} {
		if _, _, err := middleware.signer(request, "demo-app", security.ResolveSignatureType(signatureType)); err == nil || !strings.Contains(err.Error(), "签名方式不合法") {
			t.Fatalf("signer(%q) error = %v, want invalid signature type", signatureType, err)
		}
	}
}

// TestSignatureMiddlewareFailsClosedWithoutSecurityRegistry 确保已启用安全链时缺失启动注册表不会进入业务处理器。
func TestSignatureMiddlewareFailsClosedWithoutSecurityRegistry(t *testing.T) {
	cfg := securityEnabledConfig(t)
	middleware := newTestSignatureMiddleware(svc.NewServiceContext(cfg, "test-version", svc.Dependencies{}))
	nextCalled := false
	handler := middleware.Handle(func(http.ResponseWriter, *http.Request) {
		nextCalled = true
	}, routealias.UserProfile)
	request := httptest.NewRequest(http.MethodGet, "/api/user/profile", nil)
	request.Header.Set("X-App-Id", base64.StdEncoding.EncodeToString([]byte(cfg.AppID)))
	request.Header.Set("X-Signature", security.SignatureTypeHMAC)
	request.Header.Set(requestctx.HeaderTraceID, "0123456789abcdef0123456789abcdef")
	request.Header.Set(requestctx.HeaderTimestamp, strconv.FormatInt(time.Now().Unix(), 10))
	recorder := httptest.NewRecorder()

	handler(recorder, request)

	if nextCalled {
		t.Fatal("安全密钥注册表缺失时不得进入业务处理器")
	}
	var response struct {
		Code int `json:"code"` // Code 是启动依赖缺失时返回的业务码。
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("Unmarshal(response) error = %v; body=%s", err, recorder.Body.String())
	}
	if recorder.Code != http.StatusInternalServerError || response.Code != codes.SecurityKeyUnavailable {
		t.Fatalf("response = HTTP %d/code %d, want HTTP 500/code %d", recorder.Code, response.Code, codes.SecurityKeyUnavailable)
	}
	for _, header := range []string{"X-Cipher", "X-Crypto", "X-Key-Version", "X-Signature"} {
		if value := recorder.Header().Get(header); value != "" {
			t.Fatalf("security failure response header %s = %q, want empty", header, value)
		}
	}
}

// TestSignatureMiddlewareVerifiesHeaderOnlyPolicy 校验空字段策略能验签并写入防重放缓存。
func TestSignatureMiddlewareVerifiesHeaderOnlyPolicy(t *testing.T) {
	// 空字段清单只签请求头上下文，不读取业务参数。
	const traceID = "0123456789abcdef0123456789abcdef"
	cfg := securityEnabledConfig(t)
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() {
		_ = client.Close()
	})
	previous := runtimecfg.Get()
	runtimecfg.Set(runtimecfg.Snapshot{AppID: cfg.AppID})
	t.Cleanup(func() {
		runtimecfg.Restore(previous)
	})

	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	versionCfg := cfg.Security.SecretKey.Versions[0]
	signer, err := security.NewHMACSigner(versionCfg.AESKey)
	if err != nil {
		t.Fatalf("NewHMACSigner() error = %v", err)
	}
	sign, err := signer.Sign(security.BuildSignString(nil, []string{}, traceID, timestamp, cfg.AppID))
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", strings.NewReader(`{"sign":"`+sign+`"}`))
	req.Header.Set("Content-Type", "application/json")
	middleware := newTestSignatureMiddleware(newSecurityTestServiceContext(t, cfg, svc.Dependencies{Rds: client}))
	policy, ok := security.LookupRoutePolicy(string(routealias.AuthLogout))
	if !ok {
		t.Fatal("auth.logout security policy missing")
	}
	// 首次验签写入防重放记录，相同请求第二次必须被拒绝。
	if _, _, err = middleware.verifyRequest(req, policy, cfg.AppID, traceID, timestamp, security.SignatureTypeHMAC, time.Now().Add(signatureReplayTTL)); err != nil {
		t.Fatalf("verifyRequest() error = %v", err)
	}
	if _, _, err = middleware.verifyRequest(req, policy, cfg.AppID, traceID, timestamp, security.SignatureTypeHMAC, time.Now().Add(signatureReplayTTL)); err == nil || !strings.Contains(err.Error(), "重复请求") {
		t.Fatalf("second verifyRequest() error = %v, want replay rejection", err)
	}
}

// TestSignatureMiddlewareRejectsRequestSignAll 确保请求签名不能扩大为全量字段。
func TestSignatureMiddlewareRejectsRequestSignAll(t *testing.T) {
	middleware := newTestSignatureMiddleware(newSecurityTestServiceContext(t, securityEnabledConfig(t), svc.Dependencies{}))
	_, _, err := middleware.verifyRequest(httptest.NewRequest(http.MethodPost, "/api/demo", nil), security.RouteSecurityPolicy{
		RequestSign: []string{security.SignFieldAll},
	}, "demo-app", "trace", "1700000000", security.SignatureTypeHMAC, time.Now().Add(signatureReplayTTL))
	if err == nil || !strings.Contains(err.Error(), "全量字段") {
		t.Fatalf("verifyRequest() error = %v, want full-field rejection", err)
	}
}

// TestSignatureMiddlewareRejectsOversizeRequestSignField 确保超长请求字段映射为安全载荷超限。
func TestSignatureMiddlewareRejectsOversizeRequestSignField(t *testing.T) {
	middleware := newTestSignatureMiddleware(newSecurityTestServiceContext(t, securityEnabledConfig(t), svc.Dependencies{}))
	body := `{"username":"` + strings.Repeat("x", security.MaxSecurityFieldBytes+1) + `","sign":"demo"}`
	req := httptest.NewRequest(http.MethodPost, "/api/demo", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	_, _, err := middleware.verifyRequest(req, security.RouteSecurityPolicy{
		RequestSign: []string{"username"},
	}, "demo-app", "trace", "1700000000", security.SignatureTypeHMAC, time.Now().Add(signatureReplayTTL))
	if err == nil || !strings.Contains(err.Error(), "长度超过上限") {
		t.Fatalf("verifyRequest() error = %v, want oversize field rejection", err)
	}
	if got := resolveSecurityFailureCode(authlogic.AuthEventReasonSignatureFailed, codes.AuthFailed, err); got != codes.SecurityPayloadTooLarge {
		t.Fatalf("resolveSecurityFailureCode() = %d, want %d", got, codes.SecurityPayloadTooLarge)
	}
}

// TestSignatureMiddlewareRejectsOversizeSignValue 确保超长签名值在密码学计算前被拒绝。
func TestSignatureMiddlewareRejectsOversizeSignValue(t *testing.T) {
	middleware := newTestSignatureMiddleware(newSecurityTestServiceContext(t, securityEnabledConfig(t), svc.Dependencies{}))
	body := `{"username":"demo","sign":"` + strings.Repeat("x", security.MaxSecurityFieldBytes+1) + `"}`
	req := httptest.NewRequest(http.MethodPost, "/api/demo", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	_, _, err := middleware.verifyRequest(req, security.RouteSecurityPolicy{
		RequestSign: []string{"username"},
	}, "demo-app", "trace", "1700000000", security.SignatureTypeHMAC, time.Now().Add(signatureReplayTTL))
	if err == nil || !strings.Contains(err.Error(), "长度超过上限") {
		t.Fatalf("verifyRequest() error = %v, want oversize sign rejection", err)
	}
}

// TestSignatureMiddlewareRejectsResponseSignAll 确保响应签名不能扩大为全量字段。
func TestSignatureMiddlewareRejectsResponseSignAll(t *testing.T) {
	middleware := newTestSignatureMiddleware(newSecurityTestServiceContext(t, securityEnabledConfig(t), svc.Dependencies{}))
	recorder := newBodyRecorder()
	_, _ = recorder.body.WriteString(`{"status":true,"data":{"token":"t","items":[1,2,3]}}`)
	_, err := middleware.signResponse(recorder, security.RouteSecurityPolicy{
		ResponseSign: []string{security.SignFieldAll},
	}, "demo-app", "trace", "1700000000", nil, "")
	if err == nil || !strings.Contains(err.Error(), "全量字段") {
		t.Fatalf("signResponse() error = %v, want full-field rejection", err)
	}
}

// TestSignatureMiddlewareMarkRequestVerifiedFailsClosedOnAppIDMismatch 确保 AppID 不一致时不写防重放键。
func TestSignatureMiddlewareMarkRequestVerifiedFailsClosedOnAppIDMismatch(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() {
		_ = client.Close()
	})
	prev := runtimecfg.Get()
	runtimecfg.Set(runtimecfg.Snapshot{AppID: "site-b"})
	t.Cleanup(func() {
		runtimecfg.Restore(prev)
	})

	middleware := newTestSignatureMiddleware(svc.NewServiceContext(config.Config{AppID: "site-a"}, "test-version", svc.Dependencies{Rds: client}))
	err := middleware.markRequestVerified(httptest.NewRequest(http.MethodPost, "/api/demo", nil), "site-a", "trace-1", time.Now().Add(signatureReplayTTL))
	if err == nil || !strings.Contains(err.Error(), "app_id") {
		t.Fatalf("markRequestVerified() error = %v, want app_id mismatch", err)
	}
	if server.Exists("app:site-a:signature:replay:trace-1") || server.Exists("app:site-b:signature:replay:trace-1") {
		t.Fatal("app_id 不一致时不应写入签名防重放缓存")
	}
}

// TestSignatureMiddlewareRejectsOversizeResponseSignField 确保超长响应字段不会进入签名计算。
func TestSignatureMiddlewareRejectsOversizeResponseSignField(t *testing.T) {
	middleware := newTestSignatureMiddleware(newSecurityTestServiceContext(t, securityEnabledConfig(t), svc.Dependencies{}))
	recorder := newBodyRecorder()
	_, _ = recorder.body.WriteString(`{"status":true,"data":{"token":"` + strings.Repeat("x", security.MaxSecurityFieldBytes+1) + `"}}`)
	_, err := middleware.signResponse(recorder, security.RouteSecurityPolicy{
		ResponseSign: []string{"token"},
	}, "demo-app", "trace", "1700000000", nil, "")
	if err == nil || !strings.Contains(err.Error(), "长度超过上限") {
		t.Fatalf("signResponse() error = %v, want oversize field rejection", err)
	}
}

// TestRequestTimestampWindow 固定时间戳规范格式和五分钟防重放窗口。
func TestRequestTimestampWindow(t *testing.T) {
	now := time.Now().Unix()
	req := httptest.NewRequest(http.MethodPost, "/api/demo", nil)
	req.Header.Set("X-Timestamp", fmt.Sprint(now))
	got, _, err := requestTimestamp(req)
	if err != nil {
		t.Fatalf("requestTimestamp() error = %v", err)
	}
	if got != fmt.Sprint(now) {
		t.Fatalf("requestTimestamp() = %q, want %d", got, now)
	}
	req.Header.Set("X-Timestamp", " "+fmt.Sprint(now)+" ")
	if _, _, err := requestTimestamp(req); err == nil || !strings.Contains(err.Error(), "格式错误") {
		t.Fatalf("requestTimestamp(non-canonical) error = %v", err)
	}

	expired := httptest.NewRequest(http.MethodPost, "/api/demo", nil)
	expired.Header.Set("X-Timestamp", fmt.Sprint(now-int64(signatureReplayTTL.Seconds())-1))
	if _, _, err := requestTimestamp(expired); err == nil || !strings.Contains(err.Error(), "已过期") {
		t.Fatalf("requestTimestamp(expired) error = %v, want expired", err)
	}

	extremeFuture := httptest.NewRequest(http.MethodPost, "/api/demo", nil)
	extremeFuture.Header.Set("X-Timestamp", "9223372036854775807")
	if _, _, err := requestTimestamp(extremeFuture); err == nil || !strings.Contains(err.Error(), "已过期") {
		t.Fatalf("requestTimestamp(extreme future) error = %v, want expired", err)
	}
}

// TestSignatureMiddlewareRejectsUnknownResponseKeyBeforeHandler 确保响应侧密钥错误不会发生在业务副作用之后。
func TestSignatureMiddlewareRejectsUnknownResponseKeyBeforeHandler(t *testing.T) {
	cfg := securityEnabledConfig(t)
	middleware := newTestSignatureMiddleware(newSecurityTestServiceContext(t, cfg, svc.Dependencies{}))
	nextCalled := false
	handler := middleware.Handle(func(http.ResponseWriter, *http.Request) {
		nextCalled = true
	}, routealias.UserProfile)
	request := httptest.NewRequest(http.MethodGet, "/api/user/profile", nil)
	request.Header.Set("X-App-Id", base64.StdEncoding.EncodeToString([]byte(cfg.AppID)))
	request.Header.Set("X-Key-Version", "missing-version")
	request.Header.Set(requestctx.HeaderTraceID, "0123456789abcdef0123456789abcdef")
	request.Header.Set(requestctx.HeaderTimestamp, strconv.FormatInt(time.Now().Unix(), 10))
	recorder := httptest.NewRecorder()

	handler(recorder, request)

	if nextCalled {
		t.Fatal("响应密钥不可用时不得进入业务处理器")
	}
	var response struct {
		Code int `json:"code"` // Code 是响应密钥预检失败时返回的业务码。
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("Unmarshal(response) error = %v; body=%s", err, recorder.Body.String())
	}
	if recorder.Code != http.StatusInternalServerError || response.Code != codes.SecurityKeyUnavailable {
		t.Fatalf("response = HTTP %d/code %d, want HTTP 500/code %d", recorder.Code, response.Code, codes.SecurityKeyUnavailable)
	}
}

// TestSecurityResponseMiddlewareFailsClosedOnInvalidJSON 确保受保护成功响应不能绕过签名或加密直接泄漏。
func TestSecurityResponseMiddlewareFailsClosedOnInvalidJSON(t *testing.T) {
	cfg := securityEnabledConfig(t)
	for _, testCase := range []struct {
		name     string           // name 标识签名、加密或完整安全链测试分支。
		handler  http.HandlerFunc // handler 是当前分支待执行的响应安全链。
		wantCode int              // wantCode 是非法业务响应应返回的失败业务码。
	}{
		{
			name: "signature",
			handler: newTestSignatureMiddleware(newSecurityTestServiceContext(t, cfg, svc.Dependencies{})).Handle(
				func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("raw-secret")) },
				routealias.UserProfile,
			),
			wantCode: codes.SecurityResponseSignFailed,
		},
		{
			name: "crypto",
			handler: newTestCryptoMiddleware(newSecurityTestServiceContext(t, cfg, svc.Dependencies{})).Handle(
				func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("raw-secret")) },
			),
			wantCode: codes.SecurityResponseEncryptFailed,
		},
		{
			name: "full chain",
			handler: newTestAuthMiddleware(newSecurityTestServiceContext(t, cfg, svc.Dependencies{})).PublicHandle(
				func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("raw-secret")) },
				routealias.UserProfile,
			),
			wantCode: codes.SecurityResponseEncryptFailed,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/user/profile", nil)
			request.Header.Set("X-App-Id", base64.StdEncoding.EncodeToString([]byte(cfg.AppID)))
			request.Header.Set("X-Signature", security.SignatureTypeHMAC)
			request.Header.Set("X-Crypto", security.CryptoTypeAES)
			request.Header.Set(requestctx.HeaderTraceID, "0123456789abcdef0123456789abcdef")
			request.Header.Set(requestctx.HeaderTimestamp, strconv.FormatInt(time.Now().Unix(), 10))
			request = bindRequestMeta(request, routealias.UserProfile, svc.NewServiceContext(cfg, "test-version", svc.Dependencies{}))
			recorder := httptest.NewRecorder()

			testCase.handler(recorder, request)

			if strings.Contains(recorder.Body.String(), "raw-secret") {
				t.Fatalf("invalid protected response leaked raw body: %s", recorder.Body.String())
			}
			var response struct {
				Code int `json:"code"` // Code 是受保护响应格式非法时返回的业务码。
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatalf("Unmarshal(response) error = %v; body=%s", err, recorder.Body.String())
			}
			if response.Code != testCase.wantCode {
				t.Fatalf("response code = %d, want %d", response.Code, testCase.wantCode)
			}
			for _, header := range []string{"X-Cipher", "X-Crypto", "X-Key-Version", "X-Signature"} {
				if value := recorder.Header().Get(header); value != "" {
					t.Fatalf("security failure response header %s = %q, want empty", header, value)
				}
			}
		})
	}
}

// TestCryptoMiddlewareRejectsWholeBodyRequestCipher 确保请求只允许路由声明的字段级解密。
func TestCryptoMiddlewareRejectsWholeBodyRequestCipher(t *testing.T) {
	middleware := newTestCryptoMiddleware(newSecurityTestServiceContext(t, securityEnabledConfig(t), svc.Dependencies{}))
	err := middleware.decryptRequest(httptest.NewRequest(http.MethodPost, "/api/demo", nil), []string{cipherWholeBody}, noopCryptor{})
	if err == nil || !strings.Contains(err.Error(), "整包") {
		t.Fatalf("decryptRequest() error = %v, want whole-body rejection", err)
	}
}

// TestCryptoMiddlewareRejectsTooManyCipherFields 确保请求头字段数受安全上限约束。
func TestCryptoMiddlewareRejectsTooManyCipherFields(t *testing.T) {
	fields := []string{"f1", "f2", "f3", "f4", "f5", "f6", "f7", "f8", "f9"}
	raw := base64.StdEncoding.EncodeToString([]byte(`["f1","f2","f3","f4","f5","f6","f7","f8","f9"]`))
	_, err := decodeAndValidateCipherParams(raw, fields, "请求")
	if err == nil || !strings.Contains(err.Error(), "数量超过上限") {
		t.Fatalf("decodeAndValidateCipherParams() error = %v, want field count rejection", err)
	}
}

// TestCryptoMiddlewareRejectsNonCanonicalCipherFields 确保客户端字段列表不会被去空白或去重后接受。
func TestCryptoMiddlewareRejectsNonCanonicalCipherFields(t *testing.T) {
	for _, body := range []string{`[" password"]`, `["password","password"]`} {
		raw := base64.StdEncoding.EncodeToString([]byte(body))
		if _, err := decodeAndValidateCipherParams(raw, []string{"password"}, "请求"); err == nil {
			t.Fatalf("expected non-canonical X-Cipher fields to be rejected: %s", body)
		}
	}
}

// TestCryptoMiddlewareRejectsOversizeCipherHeader 确保超长 X-Cipher 在 JSON 解码前被拒绝。
func TestCryptoMiddlewareRejectsOversizeCipherHeader(t *testing.T) {
	raw := strings.Repeat("x", security.MaxSecurityJSONFieldBytes+1)
	_, err := decodeAndValidateCipherParams(raw, []string{"password"}, "请求")
	if err == nil || !strings.Contains(err.Error(), "长度超过上限") {
		t.Fatalf("decodeAndValidateCipherParams() error = %v, want header size rejection", err)
	}
}

// TestCryptoMiddlewareRejectsUndeclaredRequestCipher 确保客户端不能解密路由策略外的请求字段。
func TestCryptoMiddlewareRejectsUndeclaredRequestCipher(t *testing.T) {
	raw := security.EncodeCipherParams([]string{"profile"})
	_, err := decodeAndValidateCipherParams(raw, []string{"password"}, "请求")
	if err == nil || !strings.Contains(err.Error(), "不允许") {
		t.Fatalf("decodeAndValidateCipherParams() error = %v, want undeclared field rejection", err)
	}
}

// TestCryptoMiddlewareRejectsOversizeRequestCipherValue 确保超长请求密文在解密前被拒绝。
func TestCryptoMiddlewareRejectsOversizeRequestCipherValue(t *testing.T) {
	middleware := newTestCryptoMiddleware(newSecurityTestServiceContext(t, securityEnabledConfig(t), svc.Dependencies{}))
	req := httptest.NewRequest(http.MethodPost, "/api/demo", strings.NewReader(`{"password":"`+strings.Repeat("x", security.MaxSecurityFieldBytes+1)+`"}`))
	req.Header.Set("Content-Type", "application/json")
	err := middleware.decryptRequest(req, []string{"password"}, noopCryptor{})
	if err == nil || !strings.Contains(err.Error(), "长度超过上限") {
		t.Fatalf("decryptRequest() error = %v, want oversize field rejection", err)
	}
}

// TestCryptoMiddlewareAcceptsDeclaredRequestCipher 确保路由声明的请求字段可进入解密流程。
func TestCryptoMiddlewareAcceptsDeclaredRequestCipher(t *testing.T) {
	raw := security.EncodeCipherParams([]string{"password"})
	params, err := decodeAndValidateCipherParams(raw, []string{"password"}, "请求")
	if err != nil {
		t.Fatalf("decodeAndValidateCipherParams() error = %v", err)
	}
	middleware := newTestCryptoMiddleware(newSecurityTestServiceContext(t, securityEnabledConfig(t), svc.Dependencies{}))
	req := httptest.NewRequest(http.MethodPost, "/api/demo", strings.NewReader(`{"username":"demo","password":"secret"}`))
	req.Header.Set("Content-Type", "application/json")
	if err := middleware.decryptRequest(req, params, noopCryptor{}); err != nil {
		t.Fatalf("decryptRequest() error = %v", err)
	}
}

// TestCryptoMiddlewareRejectsOversizeResponseCipherValue 确保超长响应明文不会进入加密计算。
func TestCryptoMiddlewareRejectsOversizeResponseCipherValue(t *testing.T) {
	middleware := newTestCryptoMiddleware(newSecurityTestServiceContext(t, securityEnabledConfig(t), svc.Dependencies{}))
	recorder := newBodyRecorder()
	_, _ = recorder.body.WriteString(`{"status":true,"data":{"token":"` + strings.Repeat("x", security.MaxSecurityFieldBytes+1) + `"}}`)
	err := middleware.encryptResponse(recorder, []string{"token"}, noopCryptor{})
	if err == nil || !strings.Contains(err.Error(), "长度超过上限") {
		t.Fatalf("encryptResponse() error = %v, want oversize field rejection", err)
	}
}

// TestCryptoMiddlewareRejectsWholeBodyResponseCipher 确保响应只允许路由声明的字段级加密。
func TestCryptoMiddlewareRejectsWholeBodyResponseCipher(t *testing.T) {
	middleware := newTestCryptoMiddleware(newSecurityTestServiceContext(t, securityEnabledConfig(t), svc.Dependencies{}))
	recorder := newBodyRecorder()
	_, _ = recorder.body.WriteString(`{"status":true,"data":{"items":[1,2,3]}}`)
	err := middleware.encryptResponse(recorder, []string{cipherWholeBody}, noopCryptor{})
	if err == nil || !strings.Contains(err.Error(), "整包") {
		t.Fatalf("encryptResponse() error = %v, want whole-body rejection", err)
	}
}

// TestCryptoMiddlewareRejectsUndeclaredResponseCipher 确保响应不能加密路由策略外的字段。
func TestCryptoMiddlewareRejectsUndeclaredResponseCipher(t *testing.T) {
	raw := security.EncodeCipherParams([]string{"items"})
	_, err := decodeAndValidateCipherParams(raw, []string{"token"}, "响应")
	if err == nil || !strings.Contains(err.Error(), "不允许") {
		t.Fatalf("decodeAndValidateCipherParams() error = %v, want undeclared field rejection", err)
	}
}

// noopCryptor 原样返回字段值，用于隔离字段规则测试与真实密码学实现。
type noopCryptor struct{}

// Encrypt 原样返回输入，便于只断言响应字段选择和大小边界。
func (noopCryptor) Encrypt(data string) (string, error) {
	return data, nil
}

// Decrypt 原样返回输入，便于只断言请求字段选择和大小边界。
func (noopCryptor) Decrypt(data string) (string, error) {
	return data, nil
}

// securityEnabledConfig 构造同时开启签名和字段加密的有效配置。
func securityEnabledConfig(t *testing.T) config.Config {
	t.Helper()
	keys := mustSecurityTestRSAKeys(t)
	return config.Config{
		AppID: "demo-app",
		Security: config.SecurityConfig{
			SecretKey: config.SecuritySecretKeyConfig{
				StableVersion: "v1",
				SignStatus:    1,
				CryptoStatus:  1,
				Versions: []config.SecuritySecretKeyVersionConfig{{
					KeyVersion:          "v1",
					AESKey:              "1234567890123456",
					RSAPublicKeyUser:    keys.publicPEM,
					RSAPrivateKeyServer: keys.privatePEM,
				}},
			},
		},
	}
}
