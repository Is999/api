package loggerx

import (
	"encoding/json"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/Is999/go-utils/errors"
	"github.com/zeromicro/go-zero/core/logx"
)

// ErrorChain 把错误链渲染为单行 JSON 字符串。
func ErrorChain(err error) string {
	if err == nil {
		return ""
	}
	traceJSON := strings.TrimSpace(errors.TraceJSON(err))
	if traceJSON != "" {
		return traceJSON
	}
	summary := strings.TrimSpace(err.Error())
	return summary
}

// ErrorTrace 把错误链渲染为便于人眼检索的单行文本。
func ErrorTrace(err error) string {
	if err == nil {
		return ""
	}
	trace := strings.TrimSpace(errors.TraceString(err))
	if trace != "" {
		return trace
	}
	return ErrorChain(err)
}

// ErrorCaller 返回错误链中首个可解析的栈帧位置，不区分业务与运行库来源。
func ErrorCaller(err error) string {
	if err == nil {
		return ""
	}
	if caller := firstTraceCallerFromJSON(ErrorChain(err)); caller != "" {
		return caller
	}
	return firstTraceCallerFromText(ErrorTrace(err))
}

// ErrorFields 返回统一错误日志字段。
func ErrorFields(err error) []logx.LogField {
	if err == nil {
		return nil
	}
	summary := strings.TrimSpace(err.Error())
	chain := ErrorChain(err)
	fields := []logx.LogField{
		logx.Field(fieldError, summary),
		logx.Field(fieldErrorChain, chain),
	}
	if trace := ErrorTrace(err); trace != "" && trace != summary {
		fields = append(fields, logx.Field(fieldErrorTrace, trace))
	}
	if caller := ErrorCaller(err); caller != "" {
		fields = append(fields, logx.Field(fieldErrorCaller, caller))
	}
	return fields
}

// ErrorTextFields 返回纯文本错误对应的统一日志字段。
func ErrorTextFields(message string) []logx.LogField {
	message = strings.TrimSpace(message)
	if message == "" {
		return nil
	}
	return []logx.LogField{
		logx.Field(fieldError, message),
		logx.Field(fieldErrorChain, message),
	}
}

// errorTraceNode 表示 go-utils errors.TraceJSON 中的错误链节点。
type errorTraceNode struct {
	Trace []string          `json:"trace"` // Trace 保存当前错误节点的栈帧文本。
	Err   json.RawMessage   `json:"err"`   // Err 保存单个下级错误节点。
	Errs  []json.RawMessage `json:"errs"`  // Errs 保存多个下级错误节点。
}

// firstTraceCallerFromJSON 从错误链 JSON 中按记录顺序提取首个可解析位置。
func firstTraceCallerFromJSON(traceJSON string) string {
	traceJSON = strings.TrimSpace(traceJSON)
	if traceJSON == "" || traceJSON == "null" {
		return ""
	}
	var node errorTraceNode
	if err := json.Unmarshal([]byte(traceJSON), &node); err != nil {
		// 普通错误可能没有结构化栈，交由调用方继续尝试文本定位。
		return ""
	}
	return firstTraceCallerFromNode(node)
}

// firstTraceCallerFromNode 按当前节点、单错误、多错误顺序查找首个可解析位置。
func firstTraceCallerFromNode(node errorTraceNode) string {
	for _, frame := range node.Trace {
		if caller := traceFrameLocation(frame); caller != "" {
			return caller
		}
	}
	if caller := firstTraceCallerFromRaw(node.Err); caller != "" {
		return caller
	}
	for _, raw := range node.Errs {
		if caller := firstTraceCallerFromRaw(raw); caller != "" {
			return caller
		}
	}
	return ""
}

// firstTraceCallerFromRaw 解析嵌套错误节点；非结构化内容不提供栈帧位置。
func firstTraceCallerFromRaw(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var node errorTraceNode
	if err := json.Unmarshal(raw, &node); err != nil {
		return ""
	}
	return firstTraceCallerFromNode(node)
}

// firstTraceCallerFromText 从文本错误链中提取文件位置，不筛除运行库栈帧。
func firstTraceCallerFromText(traceText string) string {
	traceText = strings.TrimSpace(traceText)
	if traceText == "" {
		return ""
	}
	return traceFrameLocation(traceText)
}

// traceFrameLocation 从单个栈帧文本中截取原有文件路径和行号，不裁剪路径层级。
func traceFrameLocation(frame string) string {
	frame = strings.TrimSpace(frame)
	if frame == "" {
		return ""
	}
	// 带函数名的栈帧把文件位置放在末尾括号内，先去掉函数部分。
	if start := strings.LastIndex(frame, " ("); start >= 0 && strings.HasSuffix(frame, ")") {
		frame = strings.TrimSuffix(frame[start+2:], ")")
	}
	idx := strings.LastIndex(frame, ".go:")
	if idx < 0 {
		return ""
	}
	// 行号只消费连续数字，后续错误文本不属于可点击的源码位置。
	end := idx + len(".go:")
	for end < len(frame) && frame[end] >= '0' && frame[end] <= '9' {
		end++
	}
	start := strings.LastIndexAny(frame[:idx], " \t(")
	if start >= 0 {
		return frame[start+1 : end]
	}
	return frame[:end]
}

// callerLocation 返回 runtime.Caller 对应的短文件名和行号。
func callerLocation(skip int) string {
	if skip < 0 {
		skip = 0
	}
	_, file, line, ok := runtime.Caller(skip)
	if !ok {
		return ""
	}
	return shortCaller(file, line)
}

// shortCaller 保留最后两段路径，避免日志 caller 过长。
func shortCaller(file string, line int) string {
	file = filepath.ToSlash(file)
	idx := strings.LastIndexByte(file, '/')
	if idx >= 0 {
		if prev := strings.LastIndexByte(file[:idx], '/'); prev >= 0 {
			file = file[prev+1:]
		}
	}
	return file + ":" + strconv.Itoa(line)
}

// shouldMoveBuiltinCaller 判断是否需要把 go-zero 内置 caller 改名为 log_caller。
func shouldMoveBuiltinCaller(callerKey string) bool {
	callerKey = strings.TrimSpace(callerKey)
	return callerKey == "" || callerKey == fieldCaller
}

// positiveSkip 把负数 skip 归零，避免调用点向错误方向偏移。
func positiveSkip(skip int) int {
	if skip < 0 {
		return 0
	}
	return skip
}

// normalizeCallerSkip 归一化 caller skip，避免传入负数导致调用点偏移。
func normalizeCallerSkip(skip int) int {
	return loggerxCallerSkip + positiveSkip(skip)
}
