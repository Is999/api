package middleware

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http/httptest"
	"testing"
)

// TestRequestJSONMapRejectsTrailingContent 校验安全请求体不能夹带第二个 JSON 值。
func TestRequestJSONMapRejectsTrailingContent(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/test", bytes.NewBufferString(`{"username":"first"} {"username":"second"}`))
	req.Header.Set("Content-Type", "application/json")

	if _, err := requestJSONMap(req); err == nil {
		t.Fatal("期望尾随 JSON 内容被拒绝")
	}
}

// TestDecodeBase64HeaderRequiresCanonicalEncoding 确保安全请求头只接受标准填充 base64。
func TestDecodeBase64HeaderRequiresCanonicalEncoding(t *testing.T) {
	canonical := base64.StdEncoding.EncodeToString([]byte("site-a1"))
	if got, err := decodeBase64Header(canonical); err != nil || got != "site-a1" {
		t.Fatalf("decodeBase64Header() = %q, %v", got, err)
	}
	for _, raw := range []string{base64.RawStdEncoding.EncodeToString([]byte("site-a1")), " " + canonical, canonical + " "} {
		if _, err := decodeBase64Header(raw); err == nil {
			t.Fatalf("expected non-canonical base64 to be rejected: %q", raw)
		}
	}
}

// TestRequestParamsAcceptsSingleJSONObject 校验合法单对象仍可参与签名参数提取。
func TestRequestParamsAcceptsSingleJSONObject(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/test", bytes.NewBufferString("{\"count\":1}\n"))
	req.Header.Set("Content-Type", "application/json")

	params, err := requestParams(req)
	if err != nil {
		t.Fatalf("解析合法 JSON 失败: %v", err)
	}
	if params["count"] != json.Number("1") {
		t.Fatalf("期望保留 JSON 数字精度，实际 %#v", params["count"])
	}
}

// TestRequestParamsRejectsBodyIgnoredByHandler 防止验签读取 JSON、业务解析却因媒体类型不同而改用 query 参数。
func TestRequestParamsRejectsBodyIgnoredByHandler(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/test?count=2", bytes.NewBufferString(`{"count":1}`))
	req.Header.Set("Content-Type", "text/plain")
	if _, err := requestParams(req); err == nil {
		t.Fatal("expected non-JSON body content type to be rejected")
	}
}

// TestRequestParamsRestoresChunkedJSONLength 确保安全中间件缓冲后，下游仍会按 JSON 请求体读取同一组已验签参数。
func TestRequestParamsRestoresChunkedJSONLength(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/test", io.NopCloser(bytes.NewBufferString(`{"count":1}`)))
	req.Header.Set("Content-Type", "application/json")
	req.ContentLength = -1
	if _, err := requestParams(req); err != nil {
		t.Fatalf("requestParams() error = %v", err)
	}
	if req.ContentLength != int64(len(`{"count":1}`)) {
		t.Fatalf("ContentLength = %d, want buffered JSON length", req.ContentLength)
	}
}

// TestRequestParamsCanonicalizesJSONMediaType 确保合法媒体类型变体通过验签后仍会被 go-zero 解析为 JSON。
func TestRequestParamsCanonicalizesJSONMediaType(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/test", bytes.NewBufferString(`{"count":1}`))
	req.Header.Set("Content-Type", "Application/JSON; Charset=UTF-8")
	if _, err := requestParams(req); err != nil {
		t.Fatalf("requestParams() error = %v", err)
	}
	if got := req.Header.Get("Content-Type"); got != "application/json" {
		t.Fatalf("Content-Type = %q, want application/json", got)
	}
}

// TestRequestParamsRejectsMalformedJSONMediaType 确保包含 JSON 字样的畸形媒体类型不会进入验签和业务解析链。
func TestRequestParamsRejectsMalformedJSONMediaType(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/test", bytes.NewBufferString(`{"count":1}`))
	req.Header.Set("Content-Type", "text/application/json-invalid")
	if _, err := requestParams(req); err == nil {
		t.Fatal("expected malformed JSON media type to be rejected")
	}
}
