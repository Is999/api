package health

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"api/common/codes"
	"api/internal/httpresp"
	"api/internal/requestctx"

	"github.com/zeromicro/go-zero/core/logx"
)

// TestReadyHandlerLogsFinalResponse 验证错误摘要使用已回填的响应结果，不把日志错误链写入响应。
func TestReadyHandlerLogsFinalResponse(t *testing.T) {
	var logs bytes.Buffer
	previousWriter := logx.Reset()
	logx.SetWriter(logx.NewWriter(&logs))
	t.Cleanup(func() { logx.SetWriter(previousWriter) })

	ctx, meta := requestctx.New(t.Context())
	requestctx.SetRequest(ctx, http.MethodGet, "/api/ready", "127.0.0.1")
	requestctx.SetTrace(ctx, strings.Repeat("a", 32), strings.Repeat("b", 16))
	request := httptest.NewRequestWithContext(ctx, http.MethodGet, "/api/ready", nil)
	response := httptest.NewRecorder()
	// 缺少服务依赖稳定触发真实 handler 的失败出口，不监听端口或连接外部服务。
	ReadyHandler(nil)(response, request)

	var body httpresp.ResponseJSON
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusServiceUnavailable || body.Status || body.Code != codes.DependencyUnavailable {
		t.Fatalf("ready 失败响应不符: http=%d body=%s", response.Code, response.Body.String())
	}
	if meta.HTTPStatus != response.Code || meta.BizCode != body.Code || meta.ErrorCause == nil {
		t.Fatalf("请求元数据未回填最终结果: meta=%+v body=%s", meta, response.Body.String())
	}
	var entry struct {
		HTTPStatus int    `json:"http_status"` // 摘要中的传输状态必须来自最终响应。
		BizCode    int    `json:"biz_code"`    // 摘要中的业务码必须与响应一致。
		Content    string `json:"content"`     // 只捕获健康检查摘要，不以日志存在替代字段断言。
		Error      string `json:"error"`       // 内部包装错误保留在日志，不能附加到响应。
		TraceID    string `json:"trace_id"`    // 摘要与响应继续共享原链路。
	}
	if err := json.Unmarshal(logs.Bytes(), &entry); err != nil {
		t.Fatalf("应恰有一条可解析的健康错误日志: err=%v output=%s", err, logs.String())
	}
	if entry.HTTPStatus != response.Code || entry.BizCode != body.Code {
		t.Errorf("摘要日志必须匹配最终响应: log_http=%d log_code=%d response_http=%d response_code=%d", entry.HTTPStatus, entry.BizCode, response.Code, body.Code)
	}
	if !strings.Contains(entry.Content, "健康检查 依赖未就绪") || !strings.Contains(entry.Error, "ready依赖检查失败") || entry.TraceID != body.TraceID {
		t.Errorf("摘要丢失错误或链路上下文: entry=%+v body=%+v", entry, body)
	}
	// 保留既有依赖详情；只禁止本次日志包装错误、错误链或调用栈被附加到公开响应。
	for _, private := range []string{"ready依赖检查失败", "\"error_chain\"", "\"error_caller\""} {
		if strings.Contains(response.Body.String(), private) {
			t.Errorf("响应混入内部日志字段 %q: %s", private, response.Body.String())
		}
	}
}
