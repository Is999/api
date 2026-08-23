package validators

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/Is999/go-utils/errors"
	yaml "go.yaml.in/yaml/v2"
)

// ValidateKnownYAMLFields 按实际配置结构拒绝未知、重复或非规范字段，避免宽松解码静默启用默认值。
func ValidateKnownYAMLFields(data []byte, target any) error {
	targetType := reflect.TypeOf(target)
	if targetType == nil {
		return errors.New("配置结构类型为空")
	}
	var root yaml.MapSlice
	if err := yaml.Unmarshal(data, &root); err != nil {
		return errors.Wrap(err, "解析配置字段失败")
	}
	return errors.Tag(validateYAMLValue(root, targetType, ""))
}

// validateYAMLValue 递归校验 struct、动态 map 和 slice 中的配置字段。
func validateYAMLValue(value any, targetType reflect.Type, path string) error {
	// 指针层不改变 YAML 形态，递归前统一还原元素类型。
	for targetType.Kind() == reflect.Pointer {
		targetType = targetType.Elem()
	}
	switch targetType.Kind() {
	case reflect.Struct:
		// MapSlice 保留重复键，避免宽松解码覆盖前值。
		items, ok := value.(yaml.MapSlice)
		if !ok {
			return nil
		}
		fields := yamlStructFields(targetType)
		seen := make(map[string]struct{}, len(items))
		for _, item := range items {
			key, ok := item.Key.(string)
			if !ok || key == "" || key != strings.TrimSpace(key) {
				return errors.Errorf("配置字段必须是无首尾空白的非空字符串 path=%s", yamlPath(path, key))
			}
			if _, duplicate := seen[key]; duplicate {
				return errors.Errorf("配置字段重复 path=%s", yamlPath(path, key))
			}
			seen[key] = struct{}{}
			fieldType, exists := fields[key]
			if !exists {
				return errors.Errorf("未知配置字段 path=%s", yamlPath(path, key))
			}
			if err := validateYAMLValue(item.Value, fieldType, yamlPath(path, key)); err != nil {
				return errors.Tag(err)
			}
		}
	case reflect.Map:
		// 动态 map 只约束键契约，值继续按元素类型递归。
		items, ok := value.(yaml.MapSlice)
		if !ok {
			return nil
		}
		seen := make(map[string]struct{}, len(items))
		for _, item := range items {
			key, ok := item.Key.(string)
			if !ok || key == "" || key != strings.TrimSpace(key) {
				return errors.Errorf("动态配置键必须是无首尾空白的非空字符串 path=%s", yamlPath(path, key))
			}
			if _, duplicate := seen[key]; duplicate {
				return errors.Errorf("动态配置键重复 path=%s", yamlPath(path, key))
			}
			seen[key] = struct{}{}
			if err := validateYAMLValue(item.Value, targetType.Elem(), yamlPath(path, key)); err != nil {
				return errors.Tag(err)
			}
		}
	case reflect.Slice, reflect.Array:
		// 数组下标写入错误路径，便于定位具体配置项。
		items, ok := value.([]any)
		if !ok {
			return nil
		}
		for index, item := range items {
			if err := validateYAMLValue(item, targetType.Elem(), yamlIndexPath(path, index)); err != nil {
				return errors.Tag(err)
			}
		}
	}
	return nil
}

// yamlStructFields 展开匿名配置结构，并按 go-zero 使用的 json tag 建立字段表。
func yamlStructFields(targetType reflect.Type) map[string]reflect.Type {
	fields := make(map[string]reflect.Type)
	for index := 0; index < targetType.NumField(); index++ {
		field := targetType.Field(index)
		if field.PkgPath != "" {
			continue
		}
		tagName := strings.Split(field.Tag.Get("json"), ",")[0]
		if tagName == "-" {
			continue
		}
		if field.Anonymous && tagName == "" {
			anonymousType := field.Type
			for anonymousType.Kind() == reflect.Pointer {
				anonymousType = anonymousType.Elem()
			}
			if anonymousType.Kind() == reflect.Struct {
				for name, itemType := range yamlStructFields(anonymousType) {
					fields[name] = itemType
				}
				continue
			}
		}
		if tagName == "" {
			tagName = canonicalYAMLFieldName(targetType, field)
		}
		fields[tagName] = field.Type
	}
	return fields
}

// canonicalYAMLFieldName 固定项目对 go-zero 无显式 tag 字段的写法，避免依赖其大小写宽松匹配。
func canonicalYAMLFieldName(parent reflect.Type, field reflect.StructField) string {
	if parent.PkgPath() == "github.com/zeromicro/go-zero/core/service" && field.Name == "Log" {
		return "log"
	}
	if parent.PkgPath() == "github.com/zeromicro/go-zero/core/logx" {
		return camelToSnake(field.Name)
	}
	return field.Name
}

// camelToSnake 把 go-zero 日志字段名转换为项目样例使用的 snake_case 配置键。
func camelToSnake(value string) string {
	var result strings.Builder
	for index, current := range value {
		if current >= 'A' && current <= 'Z' {
			if index > 0 {
				result.WriteByte('_')
			}
			result.WriteByte(byte(current - 'A' + 'a'))
			continue
		}
		result.WriteRune(current)
	}
	return result.String()
}

// yamlPath 拼接配置字段路径，供启动错误直接定位。
func yamlPath(parent string, child string) string {
	if parent == "" {
		return child
	}
	return parent + "." + child
}

// yamlIndexPath 拼接配置数组下标路径。
func yamlIndexPath(parent string, index int) string {
	return fmt.Sprintf("%s[%d]", parent, index)
}
