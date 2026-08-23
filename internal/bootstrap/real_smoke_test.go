//go:build smoke

package bootstrap

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"api/common/codes"
	"api/internal/requestctx"
	"api/internal/security"
)

const realSmokeTimeout = 15 * time.Second // 真实环境启动、请求与停机各阶段共用的单步上限

// TestRealEnvironmentSmoke 使用显式配置连接真实依赖，并验证双监听器、健康检查、指标边界和停机链路。
func TestRealEnvironmentSmoke(t *testing.T) {
	// 真实冒烟只接受显式配置，默认测试流程不得连接外部依赖。
	configFile := strings.TrimSpace(os.Getenv("API_SMOKE_CONFIG"))
	if configFile == "" {
		t.Skip("未设置 API_SMOKE_CONFIG，跳过真实环境冒烟")
	}
	cfg, version, securityKeys, err := LoadConfig(configFile)
	if err != nil {
		t.Fatalf("LoadConfig() error=%v", err)
	}
	// 公网和内网监听器使用不同临时端口，避免污染开发环境。
	publicPort := freeSmokePort(t)
	internalPort := freeSmokePort(t)
	for internalPort == publicPort {
		internalPort = freeSmokePort(t)
	}
	cfg.Host = "127.0.0.1"
	cfg.Port = publicPort
	cfg.InternalServer.Host = "127.0.0.1"
	cfg.InternalServer.Port = internalPort
	cfg.HotReload.Enabled = false

	app, err := New(context.Background(), cfg, version, securityKeys)
	if err != nil {
		t.Fatalf("New() error=%v", err)
	}
	stopped := false
	t.Cleanup(func() {
		if stopped {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), realSmokeTimeout)
		defer cancel()
		_ = app.Stop(ctx)
	})
	// 启动异步执行，探针请求同时负责等待监听器就绪。
	startResult := make(chan error, 1)
	go func() {
		startResult <- app.Start()
	}()

	publicBase := "http://127.0.0.1:" + strconv.Itoa(publicPort)
	internalBase := "http://127.0.0.1:" + strconv.Itoa(internalPort)
	waitSmokeHTTP(t, startResult, publicBase+"/api/live", http.StatusOK)
	assertSmokeHTTP(t, publicBase+"/api/ready", http.StatusOK, "\"status\":true")
	assertSmokeHTTP(t, publicBase+"/api/metrics", http.StatusNotFound, "")
	assertSmokeHTTP(t, internalBase+"/api/metrics", http.StatusOK, "# HELP")

	// 实际 go-zero 全局链必须继承自定义 trace，不能被其内置 tracing 抢先创建的 span 覆盖。
	for _, endpoint := range []string{publicBase + "/api/live", internalBase + "/api/metrics"} {
		for _, supplied := range []string{"0123456789abcdef0123456789abcdef", ""} {
			request, err := http.NewRequest(http.MethodGet, endpoint, nil)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set(requestctx.HeaderTraceID, supplied)
			response, err := (&http.Client{Timeout: realSmokeTimeout}).Do(request)
			if err != nil {
				t.Fatal(err)
			}
			_, _ = io.Copy(io.Discard, response.Body)
			_ = response.Body.Close()
			actual := response.Header.Get(requestctx.HeaderTraceID)
			if len(actual) != 32 || actual == strings.Repeat("0", 32) || supplied != "" && actual != supplied {
				t.Errorf("GET %s trace=%q，传入=%q", endpoint, actual, supplied)
			}
		}
	}
	if cfg.Security.SecretKey.SignStatus == 1 {
		// 参数缺失应到达登录 Handler 返回 1001；签名错误则应在业务前返回 20008。
		signer, _, err := securityKeys.Signer(cfg.AppID, "", "", security.SignatureTypeHMAC)
		if err != nil {
			t.Fatal(err)
		}
		for _, tc := range []struct {
			name        string // 区分仅自定义头、W3C一致和W3C冲突。
			traceParent bool   // 是否发送标准 W3C 父上下文。
			conflict    bool   // 两类追踪头冲突必须继续拒绝签名请求。
		}{
			{name: "custom-only"},
			{name: "matching-w3c", traceParent: true},
			{name: "conflicting-w3c", traceParent: true, conflict: true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				// 真实 Redis 保留防重放记录；重复验收使用新 ID，不清理已有 nonce。
				traceBytes := make([]byte, 16)
				if _, err := rand.Read(traceBytes); err != nil {
					t.Fatal(err)
				}
				traceID := hex.EncodeToString(traceBytes)
				timestamp := strconv.FormatInt(time.Now().Unix(), 10)
				policy, _ := security.LookupRoutePolicy("auth.login")
				sign, err := signer.Sign(security.BuildSignString(nil, policy.RequestSign, traceID, timestamp, cfg.AppID))
				if err != nil {
					t.Fatal(err)
				}
				body, err := json.Marshal(map[string]string{"sign": sign})
				if err != nil {
					t.Fatal(err)
				}
				request, err := http.NewRequest(http.MethodPost, publicBase+"/api/auth/login", strings.NewReader(string(body)))
				if err != nil {
					t.Fatal(err)
				}
				request.Header.Set("Content-Type", "application/json")
				request.Header.Set("X-App-Id", base64.StdEncoding.EncodeToString([]byte(cfg.AppID)))
				request.Header.Set("X-Signature", security.SignatureTypeHMAC)
				request.Header.Set("X-Crypto", security.CryptoTypeAES)
				request.Header.Set(requestctx.HeaderTraceID, traceID)
				request.Header.Set(requestctx.HeaderTimestamp, timestamp)
				if tc.traceParent {
					parentTrace := traceID
					if tc.conflict {
						parentTrace = "fedcba9876543210fedcba9876543210"
					}
					request.Header.Set(requestctx.HeaderTraceParent, "00-"+parentTrace+"-0123456789abcdef-01")
				}
				response, err := (&http.Client{Timeout: realSmokeTimeout}).Do(request)
				if err != nil {
					t.Fatal(err)
				}
				defer response.Body.Close()
				var envelope struct {
					Code int `json:"code"` // 业务码区分验签拒绝和真实参数校验。
				}
				if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
					t.Fatal(err)
				}
				wantCode := codes.ParamError
				if tc.conflict {
					wantCode = codes.SecurityRequestRejected
				}
				if envelope.Code != wantCode {
					t.Fatalf("业务码=%d，期望=%d", envelope.Code, wantCode)
				}
			})
		}
	}

	// 停机必须在统一期限内关闭两个监听器并让 Start 返回。
	stopCtx, cancel := context.WithTimeout(context.Background(), realSmokeTimeout)
	defer cancel()
	if err := app.Stop(stopCtx); err != nil {
		t.Fatalf("Stop() error=%v", err)
	}
	stopped = true
	select {
	case err := <-startResult:
		if err != nil {
			t.Fatalf("Start() after Stop error=%v", err)
		}
	case <-time.After(realSmokeTimeout):
		t.Fatal("Stop 后 HTTP 监听器未按期退出")
	}
}

// freeSmokePort 向内核申请当前可用的本机 TCP 端口，关闭占位监听后交给候选进程使用。
func freeSmokePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("申请冒烟端口失败: %v", err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if err = listener.Close(); err != nil {
		t.Fatalf("释放冒烟端口失败: %v", err)
	}
	return port
}

// waitSmokeHTTP 等待候选监听器开始服务；启动提前失败时立即返回真实错误。
func waitSmokeHTTP(t *testing.T, startResult <-chan error, url string, wantStatus int) {
	t.Helper()
	deadline := time.NewTimer(realSmokeTimeout)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-startResult:
			t.Fatalf("候选进程启动提前结束: %v", err)
		case <-deadline.C:
			t.Fatalf("等待候选监听器超时: %s", url)
		case <-ticker.C:
			response, err := (&http.Client{Timeout: time.Second}).Get(url)
			if err != nil {
				continue
			}
			_ = response.Body.Close()
			if response.StatusCode == wantStatus {
				return
			}
		}
	}
}

// assertSmokeHTTP 校验真实 HTTP 状态与必要响应片段。
func assertSmokeHTTP(t *testing.T, url string, wantStatus int, wantBody string) {
	t.Helper()
	response, err := (&http.Client{Timeout: realSmokeTimeout}).Get(url)
	if err != nil {
		t.Fatalf("GET %s error=%v", url, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 1<<20))
	if err != nil {
		t.Fatalf("读取 %s 响应失败: %v", url, err)
	}
	if response.StatusCode != wantStatus {
		t.Fatalf("GET %s status=%d，期望=%d body=%s", url, response.StatusCode, wantStatus, body)
	}
	if wantBody != "" && !strings.Contains(string(body), wantBody) {
		t.Fatalf("GET %s 响应缺少 %q: %s", url, wantBody, body)
	}
}
