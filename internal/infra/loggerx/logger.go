package loggerx

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/zeromicro/go-zero/core/logx"
)

// 统一日志字段名，保持日志、trace 和排障维度一致。
const (
	// loggerxCallerSkip 表示从 loggerx 内部写入函数跳到真实业务调用点需要额外跳过的栈层数。
	loggerxCallerSkip = 2
	// loggerxRuntimeCallerSkip 表示 runtime.Caller 从内部写入函数跳到业务调用点的默认栈层数。
	loggerxRuntimeCallerSkip = 3
	// goUtilsCallerSkip 表示 go-utils 日志适配器自身增加的一层封装。
	goUtilsCallerSkip = 1

	fieldTraceID      = "trace_id"      // 请求/任务上下文的 Trace ID，用于串联跨服务日志
	fieldSpanID       = "span_id"       // 当前 span 标识，定位同一 trace 内的调用片段
	fieldRoute        = "route"         // 路由元数据的稳定别名，用于接口聚合
	fieldHTTPMethod   = "http_method"   // HTTP 请求方法，区分同一路径的动作
	fieldPath         = "path"          // 原始请求路径，只作为 content 详情
	fieldLocale       = "locale"        // 请求语言，只作为 content 详情
	fieldIP           = "ip"            // 原始 IP 标识；API 元数据导出不生成该字段
	fieldUID          = "uid"           // 业务用户 ID 短名，不提升为顶层索引
	fieldUserID       = "user_id"       // 认证上下文中的稳定用户 ID，用于索引检索
	fieldUserName     = "user_name"     // 原始用户名标识；API 元数据导出不生成该字段
	fieldNode         = "node"          // 进程节点或当前工作流执行节点，用于定位实例
	fieldMode         = "mode"          // 请求/任务携带的执行模式
	fieldHTTPStatus   = "http_status"   // 已确定的 HTTP 响应状态，与业务码分开记录
	fieldBizCode      = "biz_code"      // 统一响应业务码，用于区分业务失败
	fieldBizMessage   = "biz_message"   // 对外响应文案，只作为 content 详情
	fieldError        = "error"         // 上层传入的错误摘要，供失败事件检索
	fieldErrorChain   = "error_chain"   // go-utils 错误链，保留逐层包装上下文
	fieldErrorTrace   = "error_trace"   // 错误链的人读文本，只作为 content 详情
	fieldErrorCaller  = "error_caller"  // 错误链定位点，优先用于错误日志 caller
	fieldCaller       = "caller"        // 业务调用点；错误日志优先使用错误产生处
	fieldLogCaller    = "log_caller"    // 与错误产生处不同的实际打印点
	fieldErrorMsg     = "error_message" // 请求元数据中的对外错误消息
	fieldTaskID       = "task_id"       // 异步任务实例标识，用于串联任务日志
	fieldWorkflowID   = "workflow_id"   // 同一工作流实例的共享标识
	fieldWorkflowNode = "workflow_node" // 当前 DAG 节点，同时映射到 node 索引
	fieldShard        = "shard"         // shard_index/shard_total 组合摘要
	fieldShardIndex   = "shard_index"   // 任务负载中的分片下标，总数有效时才写入
	fieldShardTotal   = "shard_total"   // 任务负载中的分片总数，非正数不写入
	fieldLatencyMS    = "latency_ms"    // 请求、任务或下游调用耗时，单位毫秒
	fieldSuccess      = "success"       // 调用方记录的最终请求或任务结果
)

// 带单位的通用日志字段名。
const (
	// FieldIntervalSeconds 表示轮询、调度或重试间隔秒数。
	FieldIntervalSeconds = "interval_seconds"
	// FieldWindowStartUnix 表示时间窗口起点 Unix 秒。
	FieldWindowStartUnix = "window_start_unix"
	// FieldWindowEndUnix 表示时间窗口终点排他边界 Unix 秒。
	FieldWindowEndUnix = "window_end_unix"
)

// publicLogFieldNames 仅保留跨请求、任务和错误排障共用的顶层检索字段。
// 路径、SQL、payload 和统计量等详情写入 content，避免日志平台字段膨胀。
var publicLogFieldNames = map[string]struct{}{
	fieldTraceID:     {},
	fieldSpanID:      {},
	fieldRoute:       {},
	fieldHTTPMethod:  {},
	fieldUserID:      {},
	fieldHTTPStatus:  {},
	fieldBizCode:     {},
	fieldError:       {},
	fieldErrorChain:  {},
	fieldErrorCaller: {},
	fieldCaller:      {},
	fieldLogCaller:   {},
	fieldTaskID:      {},
	fieldWorkflowID:  {},
	fieldMode:        {},
	fieldNode:        {},
	fieldShard:       {},
	fieldShardIndex:  {},
	fieldShardTotal:  {},
	fieldLatencyMS:   {},
	fieldSuccess:     {},
}

// Errorw 统一输出带错误链路的错误日志。
func Errorw(ctx context.Context, msg string, err error, fields ...logx.LogField) {
	errorw(ctx, 0, msg, err, fields...)
}

// errorw 写入错误日志，并按调用方传入的额外 skip 修正 caller。
func errorw(ctx context.Context, skip int, msg string, err error, fields ...logx.LogField) {
	fields = appendLogFields(fields, ErrorFields(err)...)
	fields = appendErrorCallerFields(fields, err, callerLocation(runtimeCallerSkip(skip)))
	msg, fields = splitLogFields(msg, appendContextFields(ctx, fields))
	LoggerWithCallerSkip(normalizeCallerSkip(skip)).Errorw(msg, fields...)
}

// ErrorTextw 统一输出只有错误文本的错误日志。
func ErrorTextw(ctx context.Context, msg string, errorText string, fields ...logx.LogField) {
	errorTextw(ctx, 0, msg, errorText, fields...)
}

// errorTextw 写入文本错误日志，适用于尚未构造成 error 的失败原因。
func errorTextw(ctx context.Context, skip int, msg string, errorText string, fields ...logx.LogField) {
	fields = appendLogFields(fields, ErrorTextFields(errorText)...)
	fields = appendCallerField(fields, callerLocation(runtimeCallerSkip(skip)))
	msg, fields = splitLogFields(msg, appendContextFields(ctx, fields))
	LoggerWithCallerSkip(normalizeCallerSkip(skip)).Errorw(msg, fields...)
}

// ErrorwSkip 统一输出带 caller skip 的错误日志。
func ErrorwSkip(ctx context.Context, skip int, msg string, err error, fields ...logx.LogField) {
	errorw(ctx, skip, msg, err, fields...)
}

// ErrorTextwSkip 统一输出带 caller skip 的文本错误日志。
func ErrorTextwSkip(ctx context.Context, skip int, msg string, errorText string, fields ...logx.LogField) {
	errorTextw(ctx, skip, msg, errorText, fields...)
}

// Infow 统一输出信息日志。
func Infow(ctx context.Context, msg string, fields ...logx.LogField) {
	infow(ctx, 0, msg, fields...)
}

// InfowSkip 输出带 caller skip 的信息日志，适用于第三方适配器等额外封装层。
func InfowSkip(ctx context.Context, skip int, msg string, fields ...logx.LogField) {
	infow(ctx, skip, msg, fields...)
}

// infow 写入信息日志，并统一补充业务 caller 字段。
func infow(ctx context.Context, skip int, msg string, fields ...logx.LogField) {
	fields = appendCallerField(fields, callerLocation(runtimeCallerSkip(skip)))
	msg, fields = splitLogFields(msg, appendContextFields(ctx, fields))
	LoggerWithCallerSkip(normalizeCallerSkip(skip)).Infow(msg, fields...)
}

// Debugw 统一输出调试日志。
func Debugw(ctx context.Context, msg string, fields ...logx.LogField) {
	debugw(ctx, 0, msg, fields...)
}

// DebugwSkip 输出带 caller skip 的调试日志，适用于第三方适配器等额外封装层。
func DebugwSkip(ctx context.Context, skip int, msg string, fields ...logx.LogField) {
	debugw(ctx, skip, msg, fields...)
}

// debugw 写入调试日志，并统一补充业务 caller 字段。
func debugw(ctx context.Context, skip int, msg string, fields ...logx.LogField) {
	fields = appendCallerField(fields, callerLocation(runtimeCallerSkip(skip)))
	msg, fields = splitLogFields(msg, appendContextFields(ctx, fields))
	LoggerWithCallerSkip(normalizeCallerSkip(skip)).Debugw(msg, fields...)
}

// Sloww 统一输出慢操作日志。
func Sloww(ctx context.Context, msg string, fields ...logx.LogField) {
	sloww(ctx, 0, msg, fields...)
}

// SlowwSkip 输出带 caller skip 的慢操作日志，适用于第三方适配器等额外封装层。
func SlowwSkip(ctx context.Context, skip int, msg string, fields ...logx.LogField) {
	sloww(ctx, skip, msg, fields...)
}

// sloww 写入慢操作日志，并统一补充业务 caller 字段。
func sloww(ctx context.Context, skip int, msg string, fields ...logx.LogField) {
	fields = appendCallerField(fields, callerLocation(runtimeCallerSkip(skip)))
	msg, fields = splitLogFields(msg, appendContextFields(ctx, fields))
	LoggerWithCallerSkip(normalizeCallerSkip(skip)).Sloww(msg, fields...)
}

// appendContextFields 先补上下文字段，再追加调用点字段，保证调用点的事件结果优先。
func appendContextFields(ctx context.Context, fields []logx.LogField) []logx.LogField {
	return appendLogFields(FieldsFromContext(ctx), fields...)
}

// splitLogFields 将非公共字段折叠进 content，减少日志平台动态字段数量。
func splitLogFields(msg string, fields []logx.LogField) (string, []logx.LogField) {
	if len(fields) == 0 {
		return msg, nil
	}
	publicFields := make([]logx.LogField, 0, len(fields))
	details := make([]string, 0, len(fields))
	// 公共字段保留为索引，其余只进入正文；两组均维持调用方顺序。
	for _, field := range fields {
		field.Key = strings.TrimSpace(field.Key)
		if field.Key == "" {
			field.Key = "field"
		}
		if isPublicLogField(field.Key) {
			publicFields = append(publicFields, field)
			continue
		}
		details = append(details, formatLogDetail(field))
	}
	if len(details) == 0 {
		return msg, publicFields
	}
	msg = strings.TrimSpace(msg)
	detailText := strings.Join(details, " ")
	if msg == "" {
		return detailText, publicFields
	}
	return msg + " | " + detailText, publicFields
}

// publicLogFields 过滤出允许写入顶层索引的公共字段，供直接绑定 logx context 的场景复用。
func publicLogFields(fields []logx.LogField) []logx.LogField {
	if len(fields) == 0 {
		return nil
	}
	publicFields := make([]logx.LogField, 0, len(fields))
	for _, field := range fields {
		field.Key = strings.TrimSpace(field.Key)
		if field.Key == "" || !isPublicLogField(field.Key) {
			continue
		}
		publicFields = append(publicFields, field)
	}
	return publicFields
}

// isPublicLogField 判断字段是否属于公共检索字段。
func isPublicLogField(key string) bool {
	_, ok := publicLogFieldNames[key]
	return ok
}

// formatLogDetail 把单个非公共字段格式化成稳定的 key=value 文本。
func formatLogDetail(field logx.LogField) string {
	return field.Key + "=" + formatLogValue(field.Value)
}

// formatLogValue 将字段值转换成单行文本，复杂值优先使用 JSON 保真。
func formatLogValue(value any) string {
	switch val := value.(type) {
	case nil:
		return "null"
	case string:
		return formatLogString(val)
	case error:
		return formatLogString(val.Error())
	case fmt.Stringer:
		return formatLogString(val.String())
	default:
		raw, err := json.Marshal(val)
		if err == nil {
			return string(raw)
		}
		return formatLogString(fmt.Sprint(val))
	}
}

// formatLogString 将字符串压成单行，必要时加 JSON 引号避免空格和等号歧义。
func formatLogString(value string) string {
	value = strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ", "\t", " ").Replace(value))
	if value == "" {
		return `""`
	}
	if strings.ContainsAny(value, " =\"'{}[],:|") {
		raw, err := json.Marshal(value)
		if err == nil {
			return string(raw)
		}
	}
	return value
}

// appendLogFields 复制后追加字段，避免复用底层切片使并发日志串写。
func appendLogFields(base []logx.LogField, extra ...logx.LogField) []logx.LogField {
	merged := make([]logx.LogField, 0, len(base)+len(extra))
	merged = append(merged, base...)
	merged = append(merged, extra...)
	return merged
}

// runtimeCallerSkip 返回 runtime.Caller 使用的最终 skip。
func runtimeCallerSkip(skip int) int {
	return loggerxRuntimeCallerSkip + positiveSkip(skip)
}

// appendErrorCallerFields 优先使用错误链定位点，无栈信息时改用日志调用点。
func appendErrorCallerFields(fields []logx.LogField, err error, logCaller string) []logx.LogField {
	sourceCaller := ErrorCaller(err)
	if sourceCaller == "" {
		sourceCaller = logCaller
	}
	fields = appendCallerField(fields, sourceCaller)
	if logCaller != "" && logCaller != sourceCaller {
		fields = appendLogFields(fields, logx.Field(fieldLogCaller, logCaller))
	}
	return fields
}

// appendCallerField 追加统一 caller 字段，空值不输出。
func appendCallerField(fields []logx.LogField, caller string) []logx.LogField {
	caller = strings.TrimSpace(caller)
	if caller == "" {
		return fields
	}
	return appendLogFields(fields, logx.Field(fieldCaller, caller))
}
