package shared

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"api/common/codes"
	"api/internal/svc"
	"api/internal/types"
)

// requestMediaInput 使用可选字段暴露框架忽略请求体时的静默零值。
type requestMediaInput struct {
	Value string `json:"value,optional" form:"value,optional"` // 同一值分别覆盖 JSON 和 URL-encoded 契约
}

// TestStandardRequestPreservesMediaBody 验证未启用签密时，JSON 大小写、chunked 和表单仍进入业务层。
func TestStandardRequestPreservesMediaBody(t *testing.T) {
	for _, test := range []struct {
		name        string // 具体媒体类型及传输方式
		contentType string // 客户端 Content-Type 原值
		body        string // 期望映射到 Value 的原始请求体
		chunked     bool   // 是否模拟未知 Content-Length
	}{
		{"json case", "Application/JSON; charset=utf-8", `{"value":"retain"}`, false},
		{"chunked json", "APPLICATION/JSON", `{"value":"retain"}`, true},
		{"form", "application/x-www-form-urlencoded", "value=retain", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := ""
			handler := RespHandler(func(_ *http.Request, _ *svc.ServiceContext, input *requestMediaInput) *types.BizResult {
				got = input.Value
				return types.NewBizResult(codes.Success)
			})(nil)
			req := httptest.NewRequest(http.MethodPost, "/api/test", strings.NewReader(test.body))
			req.Header.Set("Content-Type", test.contentType)
			if test.chunked {
				req.ContentLength = -1
			}
			response := httptest.NewRecorder()
			handler(response, req)
			if got != "retain" || response.Code != http.StatusOK {
				t.Fatalf("业务字段丢失: got=%q HTTP=%d body=%s", got, response.Code, response.Body.String())
			}
		})
	}
}

// TestStandardRequestRejectsInvalidBody 验证异常媒体类型和尾随 JSON 不会以空参数进入业务。
func TestStandardRequestRejectsInvalidBody(t *testing.T) {
	for _, test := range []struct {
		contentType string // 不受支持或格式错误的媒体类型
		body        string // 与媒体类型对应的请求体
	}{
		{"", `{"value":"retain"}`},
		{"text/plain", `{"value":"retain"}`},
		{"application/json; broken", `{"value":"retain"}`},
		{"application/json", `{"value":"retain"}{}`},
	} {
		req := httptest.NewRequest(http.MethodPost, "/api/test", strings.NewReader(test.body))
		req.Header.Set("Content-Type", test.contentType)
		if err := ParseStandardRequest(req, &requestMediaInput{}); err == nil {
			t.Fatalf("非法请求体被接受: type=%q body=%q", test.contentType, test.body)
		}
	}
}
