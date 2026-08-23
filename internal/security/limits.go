package security

import (
	"encoding/json"
	"reflect"
	"strings"

	"github.com/Is999/go-utils/errors"
)

// 安全链路轻量处理上限，防止签名和加密承载大对象。
const (
	// MaxSecurityFieldCount 是单个接口允许参与签名或加密的最大字段数。
	MaxSecurityFieldCount = 8
	// MaxSecurityFieldBytes 是普通签名或加密字段的最大字节数。
	MaxSecurityFieldBytes = 4096
	// MaxSecurityJSONFieldBytes 是 json: 加密小对象的最大字节数。
	MaxSecurityJSONFieldBytes = 8192
	// MaxSecurityRequestBodyBytes 是安全中间件读取请求体的最大字节数。
	MaxSecurityRequestBodyBytes = 65536
)

// ErrSecurityPayloadTooLarge 表示安全链路字段数量、结构或体积超过轻量处理上限。
var ErrSecurityPayloadTooLarge = errors.New("security payload exceeds limits")

// ValidateSecurityFieldCount 校验安全字段数量，避免单接口堆叠过多签名或加密字段。
func ValidateSecurityFieldCount(fields []string, scope string) error {
	seen := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		if field == "" || field != strings.TrimSpace(field) {
			return errors.Errorf("%s字段不能为空或包含首尾空白", scope)
		}
		if _, exists := seen[field]; exists {
			return errors.Errorf("%s字段重复: %s", scope, field)
		}
		seen[field] = struct{}{}
	}
	if len(fields) > MaxSecurityFieldCount {
		return errors.Wrapf(ErrSecurityPayloadTooLarge, "%s字段数量超过上限: %d", scope, MaxSecurityFieldCount)
	}
	return nil
}

// ValidateSecurityScalarValue 校验普通加密字段值，非 json: 加密只允许轻量标量。
func ValidateSecurityScalarValue(scope string, field string, value any) error {
	if isComplexSecurityValue(value) {
		return errors.Wrapf(ErrSecurityPayloadTooLarge, "%s字段[%s]不允许复杂结构", scope, field)
	}
	return ValidateSecurityTextValue(scope, field, SignValueString(value), MaxSecurityFieldBytes)
}

// ValidateSecurityTextValue 校验安全字段字符串长度。
func ValidateSecurityTextValue(scope string, field string, value string, maxBytes int) error {
	field = strings.TrimSpace(field)
	if len(value) > maxBytes {
		return errors.Wrapf(ErrSecurityPayloadTooLarge, "%s字段[%s]长度超过上限: %d", scope, field, maxBytes)
	}
	return nil
}

// ValidateSecurityJSONValue 校验 json: 小对象字段序列化后的大小。
func ValidateSecurityJSONValue(scope string, field string, value any) ([]byte, error) {
	body, err := json.Marshal(value)
	if err != nil {
		return nil, errors.Tag(err)
	}
	if len(body) > MaxSecurityJSONFieldBytes {
		return nil, errors.Wrapf(ErrSecurityPayloadTooLarge, "%s字段[%s]长度超过上限: %d", scope, field, MaxSecurityJSONFieldBytes)
	}
	return body, nil
}

// isComplexSecurityValue 判断字段值是否为非 json: 字段加密不允许的复杂结构。
func isComplexSecurityValue(value any) bool {
	if value == nil {
		return false
	}
	switch reflect.TypeOf(value).Kind() {
	case reflect.Array, reflect.Map, reflect.Slice, reflect.Struct:
		return true
	default:
		return false
	}
}
