//lint:file-ignore SA5008 ignore go-zero optional tag

package types

import (
	"fmt"
	"strings"

	codes "api/common/codes"
	i18n "api/common/i18n"

	"github.com/Is999/go-utils/errors"
)

// Error 统一封装业务失败信息。
type Error struct {
	Code       int    // 业务状态码
	MessageKey string // 国际化消息键
	Args       []any  // 可对外展示的国际化参数，禁止携带驱动错误或密钥
	Cause      error  // 保留内部排障错误链，不直接作为响应文案
}

// Error 返回包含原始原因的排障文本，不用于客户端提示。
func (e *Error) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("Error(code=%d, key=%s): %v", e.Code, e.MessageKey, e.Cause)
	}
	return fmt.Sprintf("Error(code=%d, key=%s)", e.Code, e.MessageKey)
}

// Unwrap 返回原始错误，支持 errors.Is 和 errors.As。
func (e *Error) Unwrap() error {
	return e.Cause
}

// ToBizResult 把业务错误转换成统一响应对象。
func (e *Error) ToBizResult() *BizResult {
	// 响应文案只读取消息键和参数，原始原因单独交给统一错误记录。
	return NewBizResult(e.Code).
		SetI18nMessage(e.MessageKey, e.Args...).
		WithError(e.Cause)
}

// NotFound 创建资源不存在错误。
func NotFound(msgKey string, cause error, args ...any) *Error {
	return &Error{
		Code:       codes.NotFound,
		MessageKey: msgKey,
		Args:       unwrapErrorArgs(msgKey, args),
		Cause:      wrapCauseWithContext(msgKey, cause, args),
	}
}

// DBError 创建数据库读写相关错误。
func DBError(msgKey string, cause error, args ...any) *Error {
	return &Error{
		Code:       codes.DBError,
		MessageKey: msgKey,
		Args:       unwrapErrorArgs(msgKey, args),
		Cause:      wrapCauseWithContext(msgKey, cause, args),
	}
}

// ParamError 创建参数校验或解析错误。
func ParamError(cause error) *Error {
	return &Error{
		Code:       codes.ParamError,
		MessageKey: i18n.MsgKeyParamError,
		Cause:      errors.Tag(cause),
	}
}

// ServerError 创建服务端内部错误。
func ServerError(msgKey string, cause error, args ...any) *Error {
	return &Error{
		Code:       codes.ServerError,
		MessageKey: msgKey,
		Args:       unwrapErrorArgs(msgKey, args),
		Cause:      wrapCauseWithContext(msgKey, cause, args),
	}
}

// Forbidden 创建权限不足错误。
func Forbidden(msgKey string, cause error, args ...any) *Error {
	return &Error{
		Code:       codes.Forbidden,
		MessageKey: msgKey,
		Args:       unwrapErrorArgs(msgKey, args),
		Cause:      wrapCauseWithContext(msgKey, cause, args),
	}
}

// Unauthorized 创建未授权错误。
func Unauthorized(msgKey string, cause error, args ...any) *Error {
	return &Error{
		Code:       codes.Unauthorized,
		MessageKey: msgKey,
		Args:       unwrapErrorArgs(msgKey, args),
		Cause:      wrapCauseWithContext(msgKey, cause, args),
	}
}

// Errorf 创建带格式化上下文的业务错误。
func Errorf(code int, msgKey string, cause error, format string, args ...any) *Error {
	// 此入口的格式化参数只补充日志上下文，不填充对外消息模板。
	format = strings.TrimSpace(format)
	if format != "" {
		if cause != nil {
			cause = errors.Wrapf(cause, format, args...)
		} else {
			cause = errors.Errorf(format, args...)
		}
	}
	return &Error{
		Code:       code,
		MessageKey: msgKey,
		Cause:      cause,
	}
}

// wrapCauseWithContext 把格式化排障上下文写入原始错误链。
func wrapCauseWithContext(msgKey string, cause error, args []any) error {
	if cause == nil || len(args) == 0 {
		return errors.Tag(cause)
	}
	format, ok := args[0].(string)
	if !ok || strings.TrimSpace(format) == "" {
		return errors.Tag(cause)
	}
	// 带占位符的词条接受普通展示参数，不把首个参数误当日志说明。
	if i18n.MessageTemplateHasArgs(msgKey) && !strings.Contains(format, "%") {
		return errors.Tag(cause)
	}
	if len(args) > 1 {
		return errors.Wrapf(cause, format, args[1:]...)
	}
	return errors.Wrap(cause, format)
}

// unwrapErrorArgs 拆分内部错误上下文和对外国际化参数。
func unwrapErrorArgs(msgKey string, args []any) []any {
	if len(args) == 0 {
		return args
	}
	// 模板参数会进入客户端文案，格式化后仍只作为一个占位符的值。
	if i18n.MessageTemplateHasArgs(msgKey) {
		format, ok := args[0].(string)
		if !ok || strings.TrimSpace(format) == "" || !strings.Contains(format, "%") {
			return args
		}
		if len(args) == 1 {
			return []any{format}
		}
		return []any{fmt.Sprintf(format, args[1:]...)}
	}
	// 无占位符的词条不消费字符串参数，该参数仅保留在内部错误链。
	if format, ok := args[0].(string); ok && strings.TrimSpace(format) != "" {
		return nil
	}
	return args
}
