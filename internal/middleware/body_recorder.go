package middleware

import (
	"bytes"
	"net/http"

	"github.com/Is999/go-utils/errors"
)

// bodyRecorder 捕获下游响应头、状态码和响应体，供签名/加密中间件二次处理。
type bodyRecorder struct {
	header          http.Header  // header 保存下游写入的响应头
	body            bytes.Buffer // body 保存下游写入的响应体
	status          int          // status 保存下游写入的 HTTP 状态码
	wroteHeader     bool         // wroteHeader 标记首个响应状态已经写入
	securityFailure bool         // securityFailure 标记内层安全校验已失败，外层不得再加工响应
}

// markSecurityFailure 标记内层安全中间件已经写出失败响应。
func markSecurityFailure(w http.ResponseWriter) {
	if recorder, ok := w.(*bodyRecorder); ok {
		recorder.securityFailure = true
	}
}

// flushSecurityFailureResponse 直接写出安全失败响应，并移除可用于区分失败阶段的响应头。
func flushSecurityFailureResponse(w http.ResponseWriter, recorder *bodyRecorder) bool {
	if recorder == nil || !recorder.securityFailure {
		return false
	}
	clearSecurityResponseHeaders(recorder.Header())
	clearSecurityResponseHeaders(w.Header())
	flushRecordedResponse(w, recorder)
	return true
}

// clearSecurityResponseHeaders 清理签名和加密链路产生的响应头。
func clearSecurityResponseHeaders(header http.Header) {
	header.Del("X-Cipher")
	header.Del("X-Crypto")
	header.Del(secretKeyVersionHeader)
	header.Del("X-Signature")
}

// newBodyRecorder 创建响应捕获器。
func newBodyRecorder() *bodyRecorder {
	return &bodyRecorder{
		header: make(http.Header),
		status: http.StatusOK,
	}
}

// parseSecurityResponseData 校验需要签名或加密的成功响应，业务失败响应不再二次加工。
func parseSecurityResponseData(recorder *bodyRecorder, scope string) (map[string]any, map[string]any, bool, error) {
	if recorder == nil {
		return nil, nil, false, errors.Errorf("%s响应为空", scope)
	}
	// 复用无损数字解析，安全字段改写不得舍入响应中的其它业务数值。
	envelope, err := decodeSingleJSONMap(recorder.body.Bytes())
	if err != nil {
		return nil, nil, false, errors.Wrapf(err, "%s响应不是合法JSON", scope)
	}
	status, ok := envelope["status"].(bool)
	if !ok {
		return nil, nil, false, errors.Errorf("%s响应缺少布尔字段status", scope)
	}
	if !status {
		// 业务失败没有成功数据契约，不应再因缺少敏感字段产生第二个错误。
		return envelope, nil, false, nil
	}
	data, ok := envelope["data"].(map[string]any)
	if !ok || data == nil {
		return nil, nil, false, errors.Errorf("%s成功响应data必须是对象", scope)
	}
	return envelope, data, true, nil
}

// Header 返回可写响应头集合。
func (r *bodyRecorder) Header() http.Header {
	return r.header
}

// WriteHeader 记录 HTTP 状态码。
func (r *bodyRecorder) WriteHeader(status int) {
	// 首次状态已经确定响应结果，后续 Write 不得将失败码覆盖成 200。
	if r.wroteHeader {
		return
	}
	r.wroteHeader = true
	r.status = status
}

// Write 记录响应体内容。
func (r *bodyRecorder) Write(data []byte) (int, error) {
	r.WriteHeader(http.StatusOK)
	return r.body.Write(data)
}

// flushRecordedResponse 把捕获到的响应写回真实 ResponseWriter。
func flushRecordedResponse(w http.ResponseWriter, recorder *bodyRecorder) {
	copyHeader(w.Header(), recorder.Header())
	status := recorder.status
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = w.Write(recorder.body.Bytes())
}

// copyHeader 复制响应头，避免签名/加密中间件丢失下游设置的 Header。
func copyHeader(dst http.Header, src http.Header) {
	for key, values := range src {
		dst.Del(key)
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}
