package middleware

import (
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"api/common/runtimecfg"
	"api/internal/requestctx"
	"api/internal/routealias"
	"api/internal/security"
	"api/internal/svc"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// TestSecurityResponsePreservesNumbers 确保安全字段改写不会改变其它业务数字，包括嵌套值和指数表示。
func TestSecurityResponsePreservesNumbers(t *testing.T) {
	const body = `{"status":true,"code":1,"data":{"email":"person@example.test","phone":"13800000000","large":9007199254740993,"maximum":18446744073709551615,"nested":{"negative":-9007199254740993,"decimal":0.123456789012345678901,"exponent":1.234567890123456789e+30}}}`
	cfg := securityEnabledConfig(t)
	svcCtx := newSecurityTestServiceContext(t, cfg, svc.Dependencies{})
	next := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}
	// 同一业务响应分别经过回签、加密和组合链，检查全部实际响应改写入口。
	for name, handler := range map[string]http.HandlerFunc{
		"signature": newTestSignatureMiddleware(svcCtx).Handle(next, routealias.UserProfile),
		"crypto":    newTestCryptoMiddleware(svcCtx).Handle(next),
		"combined":  newTestAuthMiddleware(svcCtx).PublicHandle(next, routealias.UserProfile),
	} {
		t.Run(name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, "/api/user/profile", nil)
			request.Header.Set("X-App-Id", base64.StdEncoding.EncodeToString([]byte(cfg.AppID)))
			request.Header.Set("X-Signature", security.SignatureTypeHMAC)
			request.Header.Set("X-Crypto", security.CryptoTypeAES)
			request.Header.Set(requestctx.HeaderTraceID, "0123456789abcdef0123456789abcdef")
			request.Header.Set(requestctx.HeaderTimestamp, strconv.FormatInt(time.Now().Unix(), 10))
			recorder := httptest.NewRecorder()
			handler(recorder, bindRequestMeta(request, routealias.UserProfile, svcCtx))
			if recorder.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", recorder.Code, recorder.Body.String())
			}
			// RawMessage 直接核对线上数值文本，测试自身不能再次用 float64 丢失精度。
			var envelope struct {
				Data map[string]json.RawMessage `json:"data"` // 未参与安全策略的数字必须原样返回。
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &envelope); err != nil {
				t.Fatal(err)
			}
			for field, want := range map[string]string{
				"large":   "9007199254740993",
				"maximum": "18446744073709551615",
				"nested":  `{"decimal":0.123456789012345678901,"exponent":1.234567890123456789e+30,"negative":-9007199254740993}`,
			} {
				if got := string(envelope.Data[field]); got != want {
					t.Errorf("%s = %s, want %s", field, got, want)
				}
			}
		})
	}
}

// TestCryptoMiddlewarePassesThroughRoutesWithoutCipherPolicy 校验普通响应不会进入整包缓冲。
func TestCryptoMiddlewarePassesThroughRoutesWithoutCipherPolicy(t *testing.T) {
	target := httptest.NewRecorder()
	streamedInsideHandler := false
	svcCtx := svc.NewServiceContext(securityEnabledConfig(t), "test-version", svc.Dependencies{})
	handler := newTestCryptoMiddleware(svcCtx).Handle(
		func(w http.ResponseWriter, _ *http.Request) {
			if _, ok := w.(http.Flusher); !ok {
				t.Fatal("无加密策略时必须保留底层 ResponseWriter 的流式接口")
			}
			_, _ = w.Write([]byte("first-chunk"))
			streamedInsideHandler = target.Body.String() == "first-chunk"
		},
	)

	request := httptest.NewRequest(http.MethodGet, "/api/plain", nil)
	handler(target, bindRequestMeta(request, routealias.AuthLogout, svcCtx))

	if !streamedInsideHandler {
		t.Fatal("无加密策略时响应首块必须在业务处理器返回前写入底层 ResponseWriter")
	}
}

// TestSignatureMiddlewarePassesThroughRequestOnlyPolicy 校验仅请求验签时保留底层响应接口和即时写出语义。
func TestSignatureMiddlewarePassesThroughRequestOnlyPolicy(t *testing.T) {
	// 构造只验请求头的合法签名和防重放 Redis。
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
	signer, err := security.NewHMACSigner(cfg.Security.SecretKey.Versions[0].AESKey)
	if err != nil {
		t.Fatalf("NewHMACSigner() error = %v", err)
	}
	sign, err := signer.Sign(security.BuildSignString(nil, []string{}, traceID, timestamp, cfg.AppID))
	if err != nil {
		t.Fatalf("Sign() error = %v", err)
	}
	// Handler 内部观察底层 writer，确认响应没有被中间件缓冲。
	target := httptest.NewRecorder()
	streamedInsideHandler := false
	svcCtx := newSecurityTestServiceContext(t, cfg, svc.Dependencies{Rds: client})
	handler := newTestSignatureMiddleware(svcCtx).Handle(func(w http.ResponseWriter, _ *http.Request) {
		if _, ok := w.(http.Flusher); !ok {
			t.Fatal("仅请求验签时必须保留底层 ResponseWriter 的流式接口")
		}
		_, _ = w.Write([]byte("first-chunk"))
		streamedInsideHandler = target.Body.String() == "first-chunk"
	}, routealias.AuthLogout)
	request := httptest.NewRequest(http.MethodPost, "/api/auth/logout", strings.NewReader(`{"sign":"`+sign+`"}`))
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-App-Id", base64.StdEncoding.EncodeToString([]byte(cfg.AppID)))
	request.Header.Set("X-Signature", security.SignatureTypeHMAC)
	request.Header.Set(requestctx.HeaderTraceID, traceID)
	request.Header.Set(requestctx.HeaderTimestamp, timestamp)

	// 验签通过后仍需补齐签名类型、trace 和时间戳响应头。
	handler(target, request)

	if !streamedInsideHandler {
		t.Fatal("仅请求验签时响应首块必须在业务处理器返回前写入底层 ResponseWriter")
	}
	if target.Header().Get("X-Signature") != security.SignatureTypeHMAC || target.Header().Get(requestctx.HeaderTraceID) != traceID || target.Header().Get(requestctx.HeaderTimestamp) != timestamp {
		t.Fatalf("签名响应头不完整: %#v", target.Header())
	}
}

// TestAccessLogMessageExcludesRawClientIdentity 确保访问日志正文不会绕过结构化字段规则泄漏原始 IP。
func TestAccessLogMessageExcludesRawClientIdentity(t *testing.T) {
	message := accessLogMessage(&requestctx.Meta{
		Method:   http.MethodGet,
		Path:     "/api/user/profile",
		Route:    string(routealias.UserProfile),
		ClientIP: "127.0.0.1",
	}, http.StatusOK, true)
	if strings.Contains(message, "127.0.0.1") || strings.Contains(message, "ip=") {
		t.Fatalf("access log message contains raw client IP: %s", message)
	}
}

// TestBodyRecorderKeepsFirstStatus 校验缓冲响应只接受首个 HTTP 状态码。
func TestBodyRecorderKeepsFirstStatus(t *testing.T) {
	recorder := newBodyRecorder()
	recorder.WriteHeader(http.StatusCreated)
	recorder.WriteHeader(http.StatusInternalServerError)
	_, _ = recorder.Write([]byte("ok"))

	if recorder.status != http.StatusCreated {
		t.Fatalf("期望保留首个状态码 %d，实际 %d", http.StatusCreated, recorder.status)
	}
}

// TestStatusRecorderKeepsFirstStatusAndUnwraps 校验访问日志包装器保持标准响应语义。
func TestStatusRecorderKeepsFirstStatusAndUnwraps(t *testing.T) {
	base := httptest.NewRecorder()
	recorder := &statusRecorder{ResponseWriter: base, status: http.StatusOK}
	recorder.WriteHeader(http.StatusCreated)
	recorder.WriteHeader(http.StatusInternalServerError)
	_, _ = recorder.Write([]byte("ok"))

	if recorder.status != http.StatusCreated || base.Code != http.StatusCreated {
		t.Fatalf("期望首个状态码 %d，实际 recorder=%d base=%d", http.StatusCreated, recorder.status, base.Code)
	}
	if recorder.bytes != 2 {
		t.Fatalf("期望记录 2 字节，实际 %d", recorder.bytes)
	}
	if recorder.Unwrap() != base {
		t.Fatal("期望 Unwrap 返回底层 ResponseWriter")
	}
}
