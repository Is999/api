package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"os"
	"strings"

	i18n "api/common/i18n"
	"api/internal/infra/loggerx"
	"api/internal/requestctx"
	"api/internal/svc"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

// TraceMiddleware 在 HTTP 请求入口创建或继承链路上下文。
type TraceMiddleware struct {
	tracer trace.Tracer        // HTTP 入口 span 使用的 tracer 实例
	node   string              // 当前服务节点名称
	svc    *svc.ServiceContext // 客户端 IP 可信代理配置来源
}

// NewTraceMiddleware 创建服务端 span 中间件。
func NewTraceMiddleware(svcCtx *svc.ServiceContext) *TraceMiddleware {
	instanceID := ""
	if svcCtx != nil {
		instanceID = svcCtx.CurrentConfig().InstanceID
	}
	return &TraceMiddleware{
		tracer: otel.Tracer("api/http"),
		node:   resolveNodeName(instanceID),
		svc:    svcCtx,
	}
}

// Handle 基于标准 W3C tracecontext 继承链路，并接收前端 X-Trace-Id。
func (m *TraceMiddleware) Handle(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ctx, _ := requestctx.New(r.Context())
		requestctx.SetRequest(ctx, r.Method, r.URL.Path, requestClientIP(m.svc, r))
		requestctx.SetLocale(ctx, i18n.NormalizeLocale(r.Header.Get("Accept-Language")))
		requestctx.SetNode(ctx, m.node)
		requestctx.SetMode(ctx, "api")
		// 标准 traceparent 优先，只有缺失时才尝试继承前端自定义 trace ID。
		ctx = otel.GetTextMapPropagator().Extract(ctx, propagation.HeaderCarrier(r.Header))
		ctx = inheritTraceIDFromHeader(ctx, r)

		ctx, span := m.tracer.Start(ctx, r.Method+" "+r.URL.Path, trace.WithSpanKind(trace.SpanKindServer))
		defer span.End()

		sc := span.SpanContext()
		requestctx.SetTrace(ctx, sc.TraceID().String(), sc.SpanID().String())
		span.SetAttributes(loggerx.TraceAttributesFromMeta(requestctx.FromContext(ctx))...)
		ctx = loggerx.BindContext(ctx)

		w.Header().Set(requestctx.HeaderTraceID, sc.TraceID().String())
		w.Header().Set(requestctx.HeaderSpanID, sc.SpanID().String())
		next(w, r.WithContext(ctx))
		// 路由别名和业务状态由下游补齐，必须在 span 结束前同步。
		syncSpanWithMeta(span, requestctx.FromContext(ctx))
	}
}

// inheritTraceIDFromHeader 接收前端传入的 X-Trace-Id。
func inheritTraceIDFromHeader(ctx context.Context, r *http.Request) context.Context {
	if r == nil {
		return ctx
	}
	if trace.SpanContextFromContext(ctx).IsValid() {
		return ctx
	}
	if strings.TrimSpace(r.Header.Get(requestctx.HeaderTraceParent)) != "" {
		return ctx
	}
	traceID, ok := parseHeaderTraceID(r.Header.Get(requestctx.HeaderTraceID))
	if !ok {
		return ctx
	}
	parent := trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    traceID,
		SpanID:     newParentSpanID(),
		TraceFlags: trace.FlagsSampled,
		Remote:     true,
	})
	if !parent.IsValid() {
		return ctx
	}
	return trace.ContextWithRemoteSpanContext(ctx, parent)
}

// parseHeaderTraceID 校验并解析 32 位 trace id。
func parseHeaderTraceID(raw string) (trace.TraceID, bool) {
	if len(raw) != 32 || raw != strings.TrimSpace(raw) || raw != strings.ToLower(raw) {
		return trace.TraceID{}, false
	}
	if _, err := hex.DecodeString(raw); err != nil {
		return trace.TraceID{}, false
	}
	traceID, err := trace.TraceIDFromHex(raw)
	if err != nil || !traceID.IsValid() {
		return trace.TraceID{}, false
	}
	return traceID, true
}

// newParentSpanID 为外部 trace id 创建临时父 span id；随机源失败时返回无效值并拒绝继承外部上下文。
func newParentSpanID() trace.SpanID {
	var spanID trace.SpanID
	if _, err := rand.Read(spanID[:]); err != nil || !spanID.IsValid() {
		return trace.SpanID{}
	}
	return spanID
}

// syncSpanWithMeta 在响应结束后同步 route、状态和业务字段到 span。
func syncSpanWithMeta(span trace.Span, meta *requestctx.Meta) {
	if span == nil || meta == nil {
		return
	}
	route := strings.TrimSpace(meta.Route)
	if route == "" {
		route = strings.TrimSpace(meta.Path)
	}
	if route != "" && strings.TrimSpace(meta.Method) != "" {
		span.SetName(meta.Method + " " + route)
	}
	span.SetAttributes(loggerx.TraceAttributesFromMeta(meta)...)
}

// resolveNodeName 优先使用实例配置，未配置时读取主机名。
func resolveNodeName(instanceID string) string {
	if instanceID != "" {
		return instanceID
	}
	if name, err := os.Hostname(); err == nil && strings.TrimSpace(name) != "" {
		return strings.TrimSpace(name)
	}
	return "unknown"
}
