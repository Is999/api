package loggerx

import (
	"api/internal/config"
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/Is999/go-utils"
	"github.com/Is999/go-utils/errors"
	jsoniter "github.com/json-iterator/go"
	"github.com/zeromicro/go-zero/core/logx"
)

// Setup 初始化 go-zero 日志，并在文件输出模式下额外镜像到 stdout 方便容器采集。
func Setup(c config.Config) error {
	// logx 与 go-utils 配置为进程级状态，只能在接收请求前安装。
	if shouldMoveBuiltinCaller(c.Log.FieldKeys.CallerKey) {
		c.Log.FieldKeys.CallerKey = fieldLogCaller
	}
	if err := logx.SetUp(c.Log); err != nil {
		return errors.Wrap(err, "初始化 go-zero 日志失败")
	}
	wrapCurrentLogWriter()
	if c.Log.Mode == "file" {
		// 文件和 stdout 共用过滤规则，最终由主入口关闭组合 writer。
		logx.AddWriter(wrapLogWriter(logx.NewWriter(os.Stdout)))
	}
	errors.SetStackDepth(32)
	errors.SetTraceEnabled(true)
	utils.Configure(
		utils.WithJSON(jsoniter.Marshal, jsoniter.Unmarshal),
		utils.WithLogger(newGoUtilsLogger(nil)),
	)
	return nil
}

// callerDedupWriter 过滤 go-zero 自动追加且与业务 caller 重复的 log_caller。
type callerDedupWriter struct {
	inner logx.Writer // 被包装的原始日志写入器
}

// wrapCurrentLogWriter 为当前 logx writer 增加 caller 去重能力。
func wrapCurrentLogWriter() {
	current := logx.Reset()
	if current == nil {
		return
	}
	logx.SetWriter(wrapLogWriter(current))
}

// wrapLogWriter 包装日志 writer，避免重复包装。
func wrapLogWriter(w logx.Writer) logx.Writer {
	if w == nil {
		return nil
	}
	if _, ok := w.(*callerDedupWriter); ok {
		return w
	}
	return &callerDedupWriter{inner: w}
}

// Alert 写入告警日志。
func (w *callerDedupWriter) Alert(v any) {
	w.inner.Alert(v)
}

// Close 关闭底层日志 writer。
func (w *callerDedupWriter) Close() error {
	return w.inner.Close()
}

// Debug 写入调试日志。
func (w *callerDedupWriter) Debug(v any, fields ...logx.LogField) {
	w.inner.Debug(v, dedupeLogCallerFields(fields)...)
}

// Error 写入错误日志。
func (w *callerDedupWriter) Error(v any, fields ...logx.LogField) {
	w.inner.Error(v, dedupeLogCallerFields(fields)...)
}

// Info 写入信息日志。
func (w *callerDedupWriter) Info(v any, fields ...logx.LogField) {
	w.inner.Info(v, dedupeLogCallerFields(fields)...)
}

// Severe 写入严重错误日志。
func (w *callerDedupWriter) Severe(v any) {
	w.inner.Severe(v)
}

// Slow 写入慢日志。
func (w *callerDedupWriter) Slow(v any, fields ...logx.LogField) {
	w.inner.Slow(v, dedupeLogCallerFields(fields)...)
}

// Stack 写入堆栈日志。
func (w *callerDedupWriter) Stack(v any) {
	w.inner.Stack(v)
}

// Stat 写入统计日志。
func (w *callerDedupWriter) Stat(v any, fields ...logx.LogField) {
	w.inner.Stat(v, dedupeLogCallerFields(fields)...)
}

// dedupeLogCallerFields 在 caller 与 log_caller 相同时删除 log_caller。
func dedupeLogCallerFields(fields []logx.LogField) []logx.LogField {
	caller := logFieldString(fields, fieldCaller)
	logCaller := logFieldString(fields, fieldLogCaller)
	if caller == "" || logCaller == "" || caller != logCaller {
		return fields
	}
	deduped := make([]logx.LogField, 0, len(fields)-1)
	removed := false
	for _, field := range fields {
		if !removed && field.Key == fieldLogCaller && strings.TrimSpace(fmt.Sprint(field.Value)) == logCaller {
			removed = true
			continue
		}
		deduped = append(deduped, field)
	}
	return deduped
}

// logFieldString 读取日志字段的字符串值。
func logFieldString(fields []logx.LogField, key string) string {
	for _, field := range fields {
		if field.Key == key {
			return strings.TrimSpace(fmt.Sprint(field.Value))
		}
	}
	return ""
}

// goUtilsLogger 把 github.com/Is999/go-utils 的结构化日志接口适配到 go-zero logx。
type goUtilsLogger struct {
	fields []any // fields 保存 With 传入的 slog 风格键值对
}

// newGoUtilsLogger 创建 go-utils 日志适配器。
func newGoUtilsLogger(fields []any) *goUtilsLogger {
	copied := make([]any, 0, len(fields))
	copied = append(copied, fields...)
	return &goUtilsLogger{fields: copied}
}

// Debug 输出调试日志。
func (l *goUtilsLogger) Debug(msg string, args ...any) {
	DebugwSkip(context.Background(), goUtilsCallerSkip, msg, l.logFields(args...)...)
}

// Info 输出信息日志。
func (l *goUtilsLogger) Info(msg string, args ...any) {
	InfowSkip(context.Background(), goUtilsCallerSkip, msg, l.logFields(args...)...)
}

// Warn 输出警告日志。
func (l *goUtilsLogger) Warn(msg string, args ...any) {
	SlowwSkip(context.Background(), goUtilsCallerSkip, msg, l.logFields(args...)...)
}

// Error 输出错误日志。
func (l *goUtilsLogger) Error(msg string, args ...any) {
	ErrorTextwSkip(context.Background(), goUtilsCallerSkip, msg, msg, l.logFields(args...)...)
}

// With 创建携带固定字段的新日志对象。
func (l *goUtilsLogger) With(args ...any) utils.Logger {
	fields := make([]any, 0, len(l.fields)+len(args))
	fields = append(fields, l.fields...)
	fields = append(fields, args...)
	return newGoUtilsLogger(fields)
}

// Enabled 允许适配调用进入 logx，再由 logx 按自身级别过滤。
func (l *goUtilsLogger) Enabled(_ context.Context, _ utils.LogLevel) bool {
	return true
}

// logFields 把 slog 风格键值参数转换成 logx 字段。
func (l *goUtilsLogger) logFields(args ...any) []logx.LogField {
	merged := make([]any, 0, len(l.fields)+len(args))
	merged = append(merged, l.fields...)
	merged = append(merged, args...)
	fields := make([]logx.LogField, 0, (len(merged)+1)/2)
	// 日志适配不因畸形键值对中断业务：非字符串键记为 field，尾部缺值记为空字符串。
	for i := 0; i < len(merged); i += 2 {
		key, ok := merged[i].(string)
		if !ok || strings.TrimSpace(key) == "" {
			key = "field"
		}
		var value any = ""
		if i+1 < len(merged) {
			value = merged[i+1]
		}
		fields = append(fields, logx.Field(key, value))
	}
	return fields
}
