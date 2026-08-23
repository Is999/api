package requestctx

import (
	"context"
	"strings"
	"time"
)

// 前后端和 W3C trace 传递使用的 HTTP Header 名称。
const (
	HeaderTraceID     = "X-Trace-Id"  // HeaderTraceID 是前后端约定的请求追踪头。
	HeaderTimestamp   = "X-Timestamp" // HeaderTimestamp 是秒级请求时间戳，参与签名并校验时间窗口。
	HeaderSpanID      = "X-Span-Id"   // HeaderSpanID 是服务端当前处理片段标识。
	HeaderTraceParent = "traceparent" // HeaderTraceParent 是 W3C Trace Context 标准头。
)

// metaKey 是 request meta 在 context 中的私有 key。
type metaKey struct{}

// Meta 保存一次请求顺序更新的链路与审计元数据；异步任务不能并发修改同一份实例。
type Meta struct {
	StartedAt     time.Time // 入口首次创建时间，后续中间件复用，供耗时计算
	TraceID       string    // 链路追踪 ID
	SpanID        string    // 当前服务内处理片段 ID
	Route         string    // 统一路由别名
	Method        string    // HTTP 方法
	Path          string    // HTTP 请求路径
	ClientIP      string    // 按可信代理规则解析的来源 IP，不直接采信任意转发头
	Locale        string    // 已解析请求语言，空值由响应出口采用默认语言
	UserID        int64     // 通过鉴权后写入的用户 ID，0 表示尚未确认身份
	UserName      string    // 通过鉴权后写入的用户名，不采用请求自报值
	authUser      AuthUser  // 鉴权中间件从主库确认的请求级用户快照，零值表示尚未鉴权
	AccessToken   string    // 当前请求访问令牌，仅在请求内传递，禁止记录到日志
	SessionID     string    // JWT 与 Redis 核对后的会话 ID，刷新时保持不变
	Node          string    // 当前服务节点
	Mode          string    // 当前运行模式
	HTTPStatus    int       // 统一响应出口回填的 HTTP 状态，初始值不能代表请求成功
	BizCode       int       // 统一响应出口回填的业务码
	BizMessage    string    // 已按请求语言解析的最终响应文案
	LatencyMS     int64     // 请求总耗时（毫秒）
	ErrorMessage  string    // 内部错误摘要，仅供日志和 trace，不返回客户端
	ErrorCause    error     // 保留包装链的内部错误对象，供错误分类使用
	SkipAccessLog bool      // 是否跳过普通访问日志，供高频探针路由降噪
	TaskID        string    // 当前异步任务 ID
	WorkflowID    string    // 当前工作流实例 ID
	WorkflowNode  string    // 当前工作流节点名称
	ShardIndex    int       // 当前工作流分片索引
	ShardTotal    int       // 当前工作流总分片数
}

// New 为请求创建统一元数据容器。
func New(ctx context.Context) (context.Context, *Meta) {
	// 多层中间件共享既有容器，避免后注册的入口覆盖身份或链路信息。
	if meta := FromContext(ctx); meta != nil {
		if meta.StartedAt.IsZero() {
			meta.StartedAt = time.Now()
		}
		return ctx, meta
	}
	meta := &Meta{
		HTTPStatus: 200,
		StartedAt:  time.Now(),
	}
	return context.WithValue(ctx, metaKey{}, meta), meta
}

// WithMeta 主要用于测试或需要替换整份请求元数据的场景。
func WithMeta(ctx context.Context, meta *Meta) context.Context {
	return context.WithValue(ctx, metaKey{}, meta)
}

// FromContext 读取当前请求元数据。
func FromContext(ctx context.Context) *Meta {
	if ctx == nil {
		return nil
	}
	if meta, ok := ctx.Value(metaKey{}).(*Meta); ok {
		return meta
	}
	return nil
}

// Locale 返回请求语言；上下文没有显式语言时由调用方选择默认值。
func Locale(ctx context.Context) string {
	if meta := FromContext(ctx); meta != nil {
		return strings.TrimSpace(meta.Locale)
	}
	return ""
}

// SetTrace 记录当前请求最终采用的 trace/span。
func SetTrace(ctx context.Context, traceID, spanID string) {
	if meta := FromContext(ctx); meta != nil {
		meta.TraceID = strings.TrimSpace(traceID)
		meta.SpanID = strings.TrimSpace(spanID)
	}
}

// SetRoute 写入统一路由别名。
func SetRoute(ctx context.Context, route string) {
	if meta := FromContext(ctx); meta != nil && route != "" {
		meta.Route = route
	}
}

// SetSkipAccessLog 设置当前请求是否跳过普通访问日志。
func SetSkipAccessLog(ctx context.Context, skip bool) {
	if meta := FromContext(ctx); meta != nil {
		meta.SkipAccessLog = skip
	}
}

// SetRequest 在请求入口补齐基础 HTTP 信息。
func SetRequest(ctx context.Context, method, path, clientIP string) {
	if meta := FromContext(ctx); meta != nil {
		if method != "" {
			meta.Method = method
		}
		if path != "" {
			meta.Path = path
		}
		if clientIP != "" {
			meta.ClientIP = clientIP
		}
	}
}

// SetLocale 设置请求语言，供响应消息做多语言翻译。
func SetLocale(ctx context.Context, locale string) {
	if meta := FromContext(ctx); meta != nil && locale != "" {
		meta.Locale = locale
	}
}

// SetUser 由鉴权中间件或登录流程补充操作者信息。
func SetUser(ctx context.Context, userID int64, userName, clientIP string) {
	if meta := FromContext(ctx); meta != nil {
		if userID > 0 {
			meta.UserID = userID
		}
		if userName != "" {
			meta.UserName = userName
		}
		if clientIP != "" {
			meta.ClientIP = clientIP
		}
	}
}

// SetAccessToken 仅在当前请求链路内保存 token。
func SetAccessToken(ctx context.Context, token string) {
	if meta := FromContext(ctx); meta != nil && token != "" {
		meta.AccessToken = token
	}
}

// SetSessionID 保存鉴权中间件已经解析出的会话 ID。
func SetSessionID(ctx context.Context, sessionID string) {
	if meta := FromContext(ctx); meta != nil {
		meta.SessionID = strings.TrimSpace(sessionID)
	}
}

// SetNode 写入当前服务节点名。
func SetNode(ctx context.Context, node string) {
	if meta := FromContext(ctx); meta != nil && strings.TrimSpace(node) != "" {
		meta.Node = strings.TrimSpace(node)
	}
}

// SetMode 写入当前运行模式。
func SetMode(ctx context.Context, mode string) {
	if meta := FromContext(ctx); meta != nil && strings.TrimSpace(mode) != "" {
		meta.Mode = strings.TrimSpace(mode)
	}
}

// SetResponse 在统一响应出口回填 HTTP/Biz 结果。
func SetResponse(ctx context.Context, httpStatus, bizCode int, bizMessage, errorMessage string) {
	if meta := FromContext(ctx); meta != nil {
		if httpStatus > 0 {
			meta.HTTPStatus = httpStatus
		}
		meta.BizCode = bizCode
		meta.BizMessage = bizMessage
		meta.ErrorMessage = strings.TrimSpace(errorMessage)
	}
}

// SetErrorResponse 在同一入口同时写入响应结果和内部错误对象。
func SetErrorResponse(ctx context.Context, httpStatus, bizCode int, bizMessage string, err error, fallback string) {
	SetResponse(ctx, httpStatus, bizCode, bizMessage, strings.TrimSpace(fallback))
	SetError(ctx, err, fallback)
}

// SetError 在请求上下文中写入内部错误对象及其内部摘要。
func SetError(ctx context.Context, err error, fallback string) {
	if meta := FromContext(ctx); meta != nil {
		meta.ErrorCause = err
		meta.ErrorMessage = strings.TrimSpace(fallback)
	}
}

// SetLatency 在请求结束时记录总耗时。
func SetLatency(ctx context.Context, d time.Duration) {
	if meta := FromContext(ctx); meta != nil {
		meta.LatencyMS = durationMilliseconds(d)
	}
}

// RefreshLatency 按请求创建时间刷新当前耗时。
func RefreshLatency(ctx context.Context) {
	if meta := FromContext(ctx); meta != nil && !meta.StartedAt.IsZero() {
		meta.LatencyMS = durationMilliseconds(time.Since(meta.StartedAt))
	}
}

// SetTask 为任务入口写入日志关联 ID，空值不覆盖已有上下文。
func SetTask(ctx context.Context, taskID string) {
	if meta := FromContext(ctx); meta != nil && strings.TrimSpace(taskID) != "" {
		meta.TaskID = strings.TrimSpace(taskID)
	}
}

// SetWorkflow 写入当前工作流上下文。
func SetWorkflow(ctx context.Context, workflowID, workflowNode string, shardIndex, shardTotal int) {
	if meta := FromContext(ctx); meta != nil {
		if workflowID != "" {
			meta.WorkflowID = workflowID
		}
		if workflowNode != "" {
			meta.WorkflowNode = workflowNode
		}
		if shardIndex >= 0 {
			meta.ShardIndex = shardIndex
		}
		if shardTotal > 0 {
			meta.ShardTotal = shardTotal
		}
	}
}

// durationMilliseconds 把耗时转换成毫秒；非零亚毫秒请求按 1ms 记录。
func durationMilliseconds(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	milliseconds := d.Milliseconds()
	if milliseconds <= 0 {
		return 1
	}
	return milliseconds
}
