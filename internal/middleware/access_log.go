package middleware

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Is999/go-utils/errors"

	"api/internal/infra/loggerx"
	"api/internal/requestctx"

	"github.com/zeromicro/go-zero/core/logx"
	"go.opentelemetry.io/otel/attribute"
	otelcodes "go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"
)

// AccessLogMiddleware 在请求结束时统一输出访问日志并更新 span 状态。
type AccessLogMiddleware struct{}

// NewAccessLogMiddleware 创建访问日志中间件实例。
func NewAccessLogMiddleware() *AccessLogMiddleware {
	return &AccessLogMiddleware{}
}

// Handle 在请求结束时统一收口访问日志。
func (m *AccessLogMiddleware) Handle(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		begin := time.Now()
		// handler 返回后再读取最终状态，避免提前记录尚未写出的响应。
		defer func() {
			ctx := r.Context()
			requestctx.SetLatency(ctx, time.Since(begin))
			meta := requestctx.FromContext(ctx)
			if meta == nil {
				return
			}
			// 元数据未记录最终状态时，以实际 ResponseWriter 结果补齐。
			if meta.HTTPStatus == 0 || meta.HTTPStatus == http.StatusOK {
				requestctx.SetResponse(ctx, recorder.status, meta.BizCode, meta.BizMessage, meta.ErrorMessage)
			}
			success := meta.ErrorMessage == "" && recorder.status < http.StatusBadRequest
			// 路由可跳过普通日志，但仍需更新 trace 状态。
			if !shouldSkipAccessLog(meta) {
				fields := []logx.LogField{
					logx.Field("http_status", recorder.status),
					logx.Field("biz_code", meta.BizCode),
					logx.Field("latency_ms", meta.LatencyMS),
					logx.Field("bytes", recorder.bytes),
					logx.Field("success", success),
				}
				if meta.ErrorMessage != "" {
					fields = append(fields,
						logx.Field("error_message", meta.ErrorMessage),
						logx.Field("error", meta.ErrorMessage),
					)
					if meta.ErrorCause == nil {
						// 缺少错误对象时用文本补齐 error_chain，保持日志字段稳定。
						fields = append(fields, logx.Field("error_chain", strings.TrimSpace(meta.ErrorMessage)))
					}
				}
				if meta.ErrorCause != nil {
					fields = append(fields, loggerx.ErrorFields(meta.ErrorCause)...)
				}
				loggerx.Infow(ctx, accessLogMessage(meta, recorder.status, success), fields...)
			}
			if span := trace.SpanFromContext(ctx); span != nil {
				attrs := append(loggerx.TraceAttributesFromMeta(meta),
					attribute.Int64("http.response_content_length", int64(recorder.bytes)),
					attribute.Int64("http.server_duration_ms", meta.LatencyMS),
					attribute.Int64("app.response_bytes", int64(recorder.bytes)),
					attribute.Bool("app.success", success),
				)
				span.SetAttributes(attrs...)
				// HTTP 或业务失败均标记 span，使日志与 trace 使用同一结果。
				if meta.ErrorMessage != "" || recorder.status >= http.StatusBadRequest {
					errMsg := meta.ErrorMessage
					if errMsg == "" {
						errMsg = http.StatusText(recorder.status)
					}
					span.SetStatus(otelcodes.Error, errMsg)
					if meta.ErrorCause != nil {
						span.RecordError(meta.ErrorCause)
					} else {
						span.RecordError(errors.New(errMsg))
					}
				} else {
					span.SetStatus(otelcodes.Ok, "ok")
				}
			}
		}()
		next(recorder, r)
	}
}

// accessLogMessage 拼装单行访问日志摘要。
func accessLogMessage(meta *requestctx.Meta, httpStatus int, success bool) string {
	parts := []string{"请求 访问日志"}
	if meta == nil {
		return strings.Join(parts, " ")
	}
	parts = appendAccessTextKV(parts, "method", meta.Method)
	parts = appendAccessTextKV(parts, "path", meta.Path)
	parts = appendAccessTextKV(parts, "route", meta.Route)
	if httpStatus > 0 {
		parts = append(parts, fmt.Sprintf("http_status=%d", httpStatus))
	}
	if meta.BizCode > 0 {
		parts = append(parts, fmt.Sprintf("biz_code=%d", meta.BizCode))
	}
	if meta.LatencyMS > 0 {
		parts = append(parts, fmt.Sprintf("latency_ms=%d", meta.LatencyMS))
	}
	parts = append(parts, fmt.Sprintf("success=%t", success))
	if meta.UserID > 0 {
		parts = append(parts, fmt.Sprintf("uid=%d", meta.UserID))
	}
	parts = appendAccessTextKV(parts, "node", meta.Node)
	return strings.Join(parts, " ")
}

// appendAccessTextKV 追加非空文本键值，保持访问日志简洁。
func appendAccessTextKV(parts []string, key string, value string) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return parts
	}
	return append(parts, fmt.Sprintf("%s=%s", key, value))
}

// shouldSkipAccessLog 判断当前请求是否按路由规格跳过普通访问日志。
func shouldSkipAccessLog(meta *requestctx.Meta) bool {
	return meta != nil && meta.SkipAccessLog
}

// statusRecorder 记录 handler 实际写出的状态码和响应大小。
type statusRecorder struct {
	http.ResponseWriter      // 原始响应写入器
	status              int  // 实际写出的 HTTP 状态码
	bytes               int  // 实际写出的响应字节数
	wroteHeader         bool // 是否已经写出首个响应状态
}

// WriteHeader 记录实际 HTTP 状态码后继续写出响应头。
func (w *statusRecorder) WriteHeader(status int) {
	if w.wroteHeader {
		return
	}
	w.wroteHeader = true
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

// Write 统计响应字节数并保持 ResponseWriter 原有行为。
func (w *statusRecorder) Write(data []byte) (int, error) {
	w.WriteHeader(http.StatusOK)
	n, err := w.ResponseWriter.Write(data)
	w.bytes += n
	return n, errors.Tag(err)
}

// Unwrap 返回底层响应写入器，供 http.ResponseController 按需透传扩展能力。
func (w *statusRecorder) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
