package handler

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	codes "api/common/codes"
	i18n "api/common/i18n"
	"api/common/runtimecfg"
	"api/internal/config"
	"api/internal/handler/shared"
	"api/internal/httpresp"
	"api/internal/middleware"
	"api/internal/requestctx"
	"api/internal/routealias"
	"api/internal/security"
	"api/internal/svc"
	"api/internal/types"

	"github.com/Is999/go-utils/errors"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"github.com/zeromicro/go-zero/core/logx"
)

// TestJSONCipherRouteSecurity 从真实路由注册与安全链验证 json: 只标记编解码，签名必须覆盖实际密文字段。
func TestJSONCipherRouteSecurity(t *testing.T) {
	const alias routealias.Alias = "test.json-cipher"
	const plain = `{"large":9007199254740993,"nested":{"decimal":0.123456789012345678901,"exponent":1.234567890123456789e+30,"negative":-9007199254740993}}`
	cfg := smokeConfig()
	cfg.Security.SecretKey = config.SecuritySecretKeyConfig{SignStatus: 1, CryptoStatus: 1, StableVersion: "v1", Versions: []config.SecuritySecretKeyVersionConfig{{KeyVersion: "v1", AESKey: "1234567890123456"}}}
	signer, err := security.NewHMACSigner(cfg.Security.SecretKey.Versions[0].AESKey)
	if err != nil {
		t.Fatal(err)
	}
	cryptor, err := security.NewAESGCMCipher(cfg.Security.SecretKey.Versions[0].AESKey)
	if err != nil {
		t.Fatal(err)
	}
	registry := security.NewKeyRegistry(security.KeyRoute{AppID: cfg.AppID, StableVersion: "v1", SignEnabled: true, CryptoEnabled: true}, map[string]security.KeyVersion{"v1": {HMACSigner: signer, AESCryptor: cryptor}})
	previous := runtimecfg.Get()
	runtimecfg.Set(runtimecfg.Snapshot{AppID: cfg.AppID})
	t.Cleanup(func() { runtimecfg.Restore(previous) })
	for _, tc := range []struct {
		name        string // 用例区分注册期错误和请求期篡改拒绝。
		signField   string // 签名字段只能是真实业务路径，不包含 json: 编码标记。
		tampered    bool   // 使用另一份合法密文替换已签字段，排除单纯 GCM 标签错误。
		invalidJSON bool   // 已认证的明文仍不得包含尾随第二个 JSON 值。
	}{
		{name: "invalid-sign-field", signField: "json:payload", tampered: true},
		{name: "number-preservation", signField: "payload"},
		{name: "tampered-ciphertext", signField: "payload", tampered: true},
		{name: "trailing-json", signField: "payload", invalidJSON: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := miniredis.RunT(t)
			client := redis.NewClient(&redis.Options{Addr: server.Addr()})
			t.Cleanup(func() { _ = client.Close() })
			service := svc.NewServiceContext(cfg, "test-version", svc.Dependencies{Rds: client, SecurityKeys: registry})
			policy := security.RouteSecurityPolicy{RequestSign: []string{tc.signField}, RequestCipher: []string{"json:payload"}, ResponseSign: []string{tc.signField}, ResponseCipher: []string{"json:payload"}}
			security.RouteSecurityPolicies[alias] = policy
			t.Cleanup(func() { delete(security.RouteSecurityPolicies, alias) })
			called := false
			spec := shared.RouteSpec{
				Method: http.MethodPost, Path: "/api/test-json-cipher", Meta: shared.RouteMeta{Alias: alias, Describe: "JSON 小对象安全回归", Access: shared.RouteAccessPublic}, DocumentPath: shared.RouteDocAuth, Chain: shared.RouteSecurityPublic,
				Handler: func(*svc.ServiceContext) http.HandlerFunc {
					return func(w http.ResponseWriter, r *http.Request) {
						called = true
						body, err := io.ReadAll(r.Body)
						if err != nil {
							t.Fatal(err)
						}
						var params map[string]json.RawMessage
						if err := json.Unmarshal(body, &params); err != nil {
							t.Fatal(err)
						}
						if !tc.tampered && string(params["payload"]) != plain {
							t.Errorf("解密后 payload = %s, want %s", params["payload"], plain)
						}
						w.Header().Set("Content-Type", "application/json")
						_, _ = w.Write([]byte(`{"status":true,"code":1,"data":{"payload":` + string(params["payload"]) + `}}`))
					}
				},
			}
			route, err := spec.RestRoute(service, middleware.NewAuthMiddleware(service, newMiddlewareRuntime(service)), nil)
			if tc.signField == "json:payload" {
				if err != nil {
					return
				}
				t.Error("json: 编码标记不得被误当成密文字段，错误策略必须拒绝注册")
			} else if err != nil {
				t.Fatal(err)
			}
			plaintext := plain
			if tc.invalidJSON {
				plaintext += ` {"extra":true}`
			}
			ciphertext, err := cryptor.Encrypt(plaintext)
			if err != nil {
				t.Fatal(err)
			}
			params := map[string]any{"payload": ciphertext}
			const traceID = "0123456789abcdef0123456789abcdef"
			timestamp := strconv.FormatInt(time.Now().Unix(), 10)
			params["sign"], err = signer.Sign(security.BuildSignString(params, policy.RequestSign, traceID, timestamp, cfg.AppID))
			if err != nil {
				t.Fatal(err)
			}
			if tc.tampered {
				params["payload"], err = cryptor.Encrypt(`{"large":42}`)
				if err != nil {
					t.Fatal(err)
				}
			}
			body, err := json.Marshal(params)
			if err != nil {
				t.Fatal(err)
			}
			request := httptest.NewRequest(http.MethodPost, spec.Path, bytes.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			request.Header.Set("X-App-Id", base64.StdEncoding.EncodeToString([]byte(cfg.AppID)))
			request.Header.Set("X-Signature", security.SignatureTypeHMAC)
			request.Header.Set("X-Crypto", security.CryptoTypeAES)
			request.Header.Set("X-Cipher", security.EncodeCipherParams(policy.RequestCipher))
			request.Header.Set(requestctx.HeaderTraceID, traceID)
			request.Header.Set(requestctx.HeaderTimestamp, timestamp)
			recorder := httptest.NewRecorder()
			route.Handler(recorder, request)
			if tc.tampered || tc.invalidJSON {
				envelope := decodeSmokeEnvelope(t, recorder)
				if called || envelope.Code != codes.SecurityRequestRejected {
					t.Fatalf("非法安全请求进入业务=%t code=%d response=%s", called, envelope.Code, recorder.Body.String())
				}
				return
			}
			if !called || recorder.Code != http.StatusOK {
				t.Fatalf("业务未成功执行: %s", recorder.Body.String())
			}
			envelope := decodeSmokeEnvelope(t, recorder)
			canonical := security.BuildSignString(envelope.Data, policy.ResponseSign, traceID, timestamp, cfg.AppID)
			if ok, err := signer.Verify(canonical, security.SignValueString(envelope.Data["sign"])); err != nil || !ok {
				t.Fatalf("响应密文验签=%t,%v", ok, err)
			}
			decoded, err := cryptor.Decrypt(security.SignValueString(envelope.Data["payload"]))
			if err != nil || decoded != plain {
				t.Fatalf("响应解密=%s,%v; want %s", decoded, err, plain)
			}
			envelope.Data["payload"] = "tampered"
			canonical = security.BuildSignString(envelope.Data, policy.ResponseSign, traceID, timestamp, cfg.AppID)
			if ok, err := signer.Verify(canonical, security.SignValueString(envelope.Data["sign"])); err != nil || ok {
				t.Fatalf("篡改响应验签=%t,%v", ok, err)
			}
		})
	}
}

// httpSmokeEnvelope 解码进程内 handler 响应；这些用例不启动监听器或真实外部依赖。
type httpSmokeEnvelope struct {
	Status  bool           `json:"status"`  // 业务成功标记
	Code    int            `json:"code"`    // 业务响应码
	Message string         `json:"message"` // 多语言响应文案
	Data    map[string]any `json:"data"`    // 响应数据首层对象
	TraceID string         `json:"traceId"` // 请求链路追踪 ID
	SpanID  string         `json:"spanId"`  // 当前服务 span ID
}

// TestWriteBizResponseUsesUnifiedEnvelope 确保成功响应统一携带业务码、文案和追踪标识。
func TestWriteBizResponseUsesUnifiedEnvelope(t *testing.T) {
	req := newSmokeRequest(http.MethodGet, "/api/user/profile", nil)
	rec := httptest.NewRecorder()

	shared.WriteBizResponse(rec, req, types.NewBizResult(codes.FetchSuccess).
		SetI18nMessage(i18n.MsgKeyFetchSuccess).
		WithData(map[string]any{"ok": true}))

	if rec.Code != http.StatusOK {
		t.Fatalf("http status = %d, want %d", rec.Code, http.StatusOK)
	}
	envelope := decodeSmokeEnvelope(t, rec)
	if !envelope.Status {
		t.Fatal("status = false, want true")
	}
	if envelope.Code != codes.FetchSuccess {
		t.Fatalf("code = %d, want %d", envelope.Code, codes.FetchSuccess)
	}
	if envelope.Message == "" {
		t.Fatal("message should not be empty")
	}
	if envelope.TraceID != "trace-smoke" || envelope.SpanID != "span-smoke" {
		t.Fatalf("trace/span = %s/%s, want trace-smoke/span-smoke", envelope.TraceID, envelope.SpanID)
	}
	if got, ok := envelope.Data["ok"].(bool); !ok || !got {
		t.Fatalf("data.ok = %#v, want true", envelope.Data["ok"])
	}
}

// TestWriteBizResponseLogsFinalFailureStatus 验证错误日志读取的是 Fail 已写回的最终 HTTP 状态和业务码。
func TestWriteBizResponseLogsFinalFailureStatus(t *testing.T) {
	var logBuffer bytes.Buffer
	previousWriter := logx.Reset()
	logx.SetWriter(logx.NewWriter(&logBuffer))
	logx.SetLevel(logx.ErrorLevel)
	t.Cleanup(func() {
		logx.SetWriter(previousWriter)
		logx.SetLevel(logx.InfoLevel)
	})

	req := newSmokeRequest(http.MethodPost, "/api/auth/login", nil)
	rec := httptest.NewRecorder()
	shared.WriteBizResponse(rec, req, types.NewBizResult(codes.ParamError).WithError(errors.New("参数失败")))

	meta := requestctx.FromContext(req.Context())
	if rec.Code != http.StatusBadRequest || meta == nil || meta.HTTPStatus != http.StatusBadRequest || meta.BizCode != codes.ParamError {
		t.Fatalf("失败响应状态未同步: recorder=%d meta=%+v", rec.Code, meta)
	}
	logText := logBuffer.String()
	if !strings.Contains(logText, "http_status=400") && !strings.Contains(logText, `"http_status":400`) {
		t.Fatalf("错误日志缺少最终 HTTP 400 状态: %s", logText)
	}
	if !strings.Contains(logText, "biz_code=1001") && !strings.Contains(logText, `"biz_code":1001`) {
		t.Fatalf("错误日志缺少最终业务码: %s", logText)
	}
}

// TestAuthMiddlewareMissingBearerUsesUnifiedEnvelope 确保受保护路由缺少 Bearer 时短路并返回统一未授权响应。
func TestAuthMiddlewareMissingBearerUsesUnifiedEnvelope(t *testing.T) {
	svcCtx := svc.NewServiceContext(smokeConfig(), "test-version", svc.Dependencies{})
	authMw := middleware.NewAuthMiddleware(svcCtx, newMiddlewareRuntime(svcCtx))
	nextCalled := false
	handler := authMw.Handle(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
	}, routealias.UserProfile)

	req := newSmokeRequest(http.MethodGet, "/api/user/profile", nil)
	rec := httptest.NewRecorder()
	handler(rec, req)

	if nextCalled {
		t.Fatal("protected handler should not be called without bearer token")
	}
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("http status = %d, want %d", rec.Code, http.StatusUnauthorized)
	}
	envelope := decodeSmokeEnvelope(t, rec)
	if envelope.Status {
		t.Fatal("status = true, want false")
	}
	if envelope.Code != codes.Unauthorized {
		t.Fatalf("code = %d, want %d", envelope.Code, codes.Unauthorized)
	}
	if envelope.Message == "" {
		t.Fatal("message should not be empty")
	}
	if envelope.TraceID != "trace-smoke" || envelope.SpanID != "span-smoke" {
		t.Fatalf("trace/span = %s/%s, want trace-smoke/span-smoke", envelope.TraceID, envelope.SpanID)
	}
}

// TestPublicSecurityChainAllowsPlainJSONWithoutSecret 确保未配置字段级密钥时公开路由仍按明文 JSON 契约执行。
func TestPublicSecurityChainAllowsPlainJSONWithoutSecret(t *testing.T) {
	// 使用无密钥配置与最小业务回调，只证明公开安全链不强制签密，不执行真实登录。
	svcCtx := svc.NewServiceContext(smokeConfig(), "test-version", svc.Dependencies{})
	authMw := middleware.NewAuthMiddleware(svcCtx, newMiddlewareRuntime(svcCtx))
	nextCalled := false
	handler := authMw.PublicHandle(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		httpresp.NewJSONResp(r.Context(), w).SetCode(codes.Success).Success(map[string]any{"ok": true})
	}, routealias.AuthLogin)

	req := newSmokeRequest(http.MethodPost, "/api/auth/login", bytes.NewBufferString(`{"identityType":"username","identityValue":"demo","password":"secret123"}`))
	rec := httptest.NewRecorder()
	handler(rec, req)

	if !nextCalled {
		t.Fatal("public handler should be called for plain JSON when secret key is not configured")
	}
	if rec.Code != http.StatusOK {
		t.Fatalf("http status = %d, want %d", rec.Code, http.StatusOK)
	}
	if rec.Header().Get("X-Signature") != "" || rec.Header().Get("X-Cipher") != "" {
		t.Fatalf("security headers should be empty, got signature=%q cipher=%q", rec.Header().Get("X-Signature"), rec.Header().Get("X-Cipher"))
	}
	envelope := decodeSmokeEnvelope(t, rec)
	if !envelope.Status {
		t.Fatal("status = false, want true")
	}
	if envelope.Code != codes.Success {
		t.Fatalf("code = %d, want %d", envelope.Code, codes.Success)
	}
	if got, ok := envelope.Data["ok"].(bool); !ok || !got {
		t.Fatalf("data.ok = %#v, want true", envelope.Data["ok"])
	}
}

// smokeConfig 返回 HTTP 冒烟链路所需的最小有效配置。
func smokeConfig() config.Config {
	return config.Config{
		AppID:     "site-smoke",
		JwtSecret: "test-secret-please-change",
	}
}

// newSmokeRequest 构造已注入稳定追踪元数据的 JSON 请求。
func newSmokeRequest(method string, path string, body *bytes.Buffer) *http.Request {
	if body == nil {
		body = bytes.NewBuffer(nil)
	}
	ctx, _ := requestctx.New(context.Background())
	requestctx.SetTrace(ctx, "trace-smoke", "span-smoke")
	requestctx.SetRequest(ctx, method, path, "127.0.0.1")
	req := httptest.NewRequest(method, path, body).WithContext(ctx)
	req.Header.Set("Content-Type", "application/json")
	return req
}

// decodeSmokeEnvelope 解码统一响应包，JSON 非法时立即终止当前用例。
func decodeSmokeEnvelope(t *testing.T, rec *httptest.ResponseRecorder) httpSmokeEnvelope {
	t.Helper()
	var envelope httpSmokeEnvelope
	if err := json.Unmarshal(rec.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode response JSON: %v, body=%s", err, rec.Body.String())
	}
	return envelope
}
