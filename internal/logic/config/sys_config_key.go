package config

import (
	"strings"

	"api/internal/model"

	"github.com/Is999/go-utils/errors"
)

// SysConfigKey 描述一个业务运行期配置项。
type SysConfigKey struct {
	UUID        string // 系统配置唯一标识，对应 sys_config.uuid
	Type        int    // 系统配置类型，对应 sys_config.type
	HasDefault  bool   // 配置不存在时是否返回默认值
	Default     any    // 配置不存在时返回的默认值
	Description string // 配置用途说明
}

// SysConfigKeyRegistry 保存业务声明的 sys_config key 清单。
type SysConfigKeyRegistry struct {
	items []SysConfigKey          // 按声明顺序保存配置项
	index map[string]SysConfigKey // uuid 到配置项的索引
}

// RequiredSysConfigKey 创建无默认值的配置 key。
func RequiredSysConfigKey(uuid string, typ int, description string) SysConfigKey {
	return SysConfigKey{UUID: uuid, Type: typ, Description: description}
}

// OptionalSysConfigKey 创建带默认值的配置 key。
func OptionalSysConfigKey(uuid string, typ int, defaultValue any, description string) SysConfigKey {
	return SysConfigKey{UUID: uuid, Type: typ, HasDefault: true, Default: defaultValue, Description: description}
}

// NewSysConfigKeyRegistry 创建 sys_config key 注册表。
func NewSysConfigKeyRegistry(items ...SysConfigKey) (*SysConfigKeyRegistry, error) {
	registry := &SysConfigKeyRegistry{index: make(map[string]SysConfigKey, len(items))}
	for _, item := range items {
		key, err := normalizeSysConfigKey(item)
		if err != nil {
			return nil, errors.Tag(err)
		}
		if _, ok := registry.index[key.UUID]; ok {
			return nil, errors.Errorf("系统配置 key 重复 uuid=%s", key.UUID)
		}
		registry.items = append(registry.items, key)
		registry.index[key.UUID] = key
	}
	return registry, nil
}

// Items 返回声明顺序的浅副本；Default 中的容器仍属注册表，只读使用。
func (r *SysConfigKeyRegistry) Items() []SysConfigKey {
	if r == nil || len(r.items) == 0 {
		return nil
	}
	items := make([]SysConfigKey, len(r.items))
	copy(items, r.items)
	return items
}

// Lookup 按 uuid 查找配置 key。
func (r *SysConfigKeyRegistry) Lookup(uuid string) (SysConfigKey, bool) {
	if r == nil || !validSysConfigUUID(uuid) {
		return SysConfigKey{}, false
	}
	key, ok := r.index[uuid]
	return key, ok
}

// GetValue 按声明类型读取配置值。
func (l *SysConfigLogic) GetValue(key SysConfigKey) (any, error) {
	return l.getValueByKey(key, key.Type)
}

// GetString 读取 String 类型配置值。
func (l *SysConfigLogic) GetString(key SysConfigKey) (string, error) {
	value, err := l.getValueByKey(key, model.SysConfigTypeString)
	if err != nil {
		return "", errors.Tag(err)
	}
	str, ok := value.(string)
	if !ok {
		return "", errors.Errorf("系统配置值类型不是 string uuid=%s", key.UUID)
	}
	return str, nil
}

// GetInt 读取 Integer 类型配置值。
func (l *SysConfigLogic) GetInt(key SysConfigKey) (int, error) {
	value, err := l.getValueByKey(key, model.SysConfigTypeInteger)
	if err != nil {
		return 0, errors.Tag(err)
	}
	number, ok := value.(int)
	if !ok {
		return 0, errors.Errorf("系统配置值类型不是 int uuid=%s", key.UUID)
	}
	return number, nil
}

// GetFloat 读取 Float 类型配置值。
func (l *SysConfigLogic) GetFloat(key SysConfigKey) (float64, error) {
	value, err := l.getValueByKey(key, model.SysConfigTypeFloat)
	if err != nil {
		return 0, errors.Tag(err)
	}
	number, ok := value.(float64)
	if !ok {
		return 0, errors.Errorf("系统配置值类型不是 float64 uuid=%s", key.UUID)
	}
	return number, nil
}

// GetBool 读取 Boolean 类型配置值。
func (l *SysConfigLogic) GetBool(key SysConfigKey) (bool, error) {
	value, err := l.getValueByKey(key, model.SysConfigTypeBoolean)
	if err != nil {
		return false, errors.Tag(err)
	}
	flag, ok := value.(bool)
	if !ok {
		return false, errors.Errorf("系统配置值类型不是 bool uuid=%s", key.UUID)
	}
	return flag, nil
}

// GetObject 读取 Object 类型配置值；返回注册表默认容器时只读使用。
func (l *SysConfigLogic) GetObject(key SysConfigKey) (map[string]any, error) {
	value, err := l.getValueByKey(key, model.SysConfigTypeObject)
	if err != nil {
		return nil, errors.Tag(err)
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, errors.Errorf("系统配置值类型不是 object uuid=%s", key.UUID)
	}
	return object, nil
}

// GetArray 读取 Array 类型配置值；返回注册表默认容器时只读使用。
func (l *SysConfigLogic) GetArray(key SysConfigKey) ([]any, error) {
	value, err := l.getValueByKey(key, model.SysConfigTypeArray)
	if err != nil {
		return nil, errors.Tag(err)
	}
	items, ok := value.([]any)
	if !ok {
		return nil, errors.Errorf("系统配置值类型不是 array uuid=%s", key.UUID)
	}
	return items, nil
}

// RenewByKey 删除并重新加载指定配置缓存。
func (l *SysConfigLogic) RenewByKey(key SysConfigKey) error {
	key, err := normalizeSysConfigKey(key)
	if err != nil {
		return errors.Tag(err)
	}
	return errors.Tag(l.RenewByUUID(key.UUID))
}

// getValueByKey 按配置 key 和期望类型读取值。
func (l *SysConfigLogic) getValueByKey(key SysConfigKey, expectedType int) (any, error) {
	key, err := normalizeSysConfigKey(key)
	if err != nil {
		return nil, errors.Tag(err)
	}
	if key.Type != expectedType {
		return nil, errors.Errorf("系统配置 key 类型不匹配 uuid=%s want=%s got=%s", key.UUID, sysConfigTypeName(expectedType), sysConfigTypeName(key.Type))
	}
	entry, err := l.getCachedEntry(key.UUID)
	if err != nil {
		// 默认值只覆盖配置不存在，缓存故障、类型漂移不能被默认值掩盖。
		if errors.Is(err, ErrSysConfigNotFound) && key.HasDefault {
			// 容器默认值由注册表共享，调用方需修改时应先复制。
			return key.Default, nil
		}
		return nil, errors.Tag(err)
	}
	actualType, err := sysConfigCacheType(key.UUID, entry)
	if err != nil {
		return nil, errors.Tag(err)
	}
	if actualType != expectedType {
		return nil, errors.Errorf("系统配置实际类型不匹配 uuid=%s want=%s got=%s", key.UUID, sysConfigTypeName(expectedType), sysConfigTypeName(actualType))
	}
	return decodeSysConfigValue(actualType, entry[sysConfigCacheFieldValue])
}

// normalizeSysConfigKey 只裁剪说明文本，UUID 与声明类型必须原样满足注册约束。
func normalizeSysConfigKey(key SysConfigKey) (SysConfigKey, error) {
	key.Description = strings.TrimSpace(key.Description)
	if !validSysConfigUUID(key.UUID) {
		return SysConfigKey{}, errors.Errorf("系统配置 key uuid 不能为空或包含首尾空白")
	}
	if !validSysConfigType(key.Type) {
		return SysConfigKey{}, errors.Errorf("系统配置 key 类型非法 uuid=%s type=%d", key.UUID, key.Type)
	}
	if key.HasDefault {
		defaultValue, err := normalizeSysConfigDefault(key.Type, key.Default)
		if err != nil {
			return SysConfigKey{}, errors.Wrapf(err, "系统配置 key 默认值非法 uuid=%s", key.UUID)
		}
		key.Default = defaultValue
	}
	return key, nil
}

// normalizeSysConfigDefault 校验默认值类型并做必要转换。
func normalizeSysConfigDefault(typ int, value any) (any, error) {
	// 无类型 nil 归一为空容器，保证默认结果可按声明的容器类型断言。
	switch typ {
	case model.SysConfigTypeObject:
		if value == nil {
			return map[string]any{}, nil
		}
		object, ok := value.(map[string]any)
		if !ok {
			return nil, errors.Errorf("默认值必须是 map[string]any")
		}
		return object, nil
	case model.SysConfigTypeArray:
		if value == nil {
			return []any{}, nil
		}
		items, ok := value.([]any)
		if !ok {
			return nil, errors.Errorf("默认值必须是 []any")
		}
		return items, nil
	case model.SysConfigTypeString:
		str, ok := value.(string)
		if !ok {
			return nil, errors.Errorf("默认值必须是 string")
		}
		return str, nil
	case model.SysConfigTypeInteger:
		number, ok := value.(int)
		if !ok {
			return nil, errors.Errorf("默认值必须是 int")
		}
		return number, nil
	case model.SysConfigTypeFloat:
		// 允许 int 转 float64；大整数可能舍入，精确整数配置应声明为 Integer。
		switch number := value.(type) {
		case float64:
			return number, nil
		case int:
			return float64(number), nil
		default:
			return nil, errors.Errorf("默认值必须是 float64")
		}
	case model.SysConfigTypeBoolean:
		flag, ok := value.(bool)
		if !ok {
			return nil, errors.Errorf("默认值必须是 bool")
		}
		return flag, nil
	default:
		return nil, errors.Errorf("不支持配置类型 %d", typ)
	}
}

// validSysConfigType 判断配置类型是否可被业务读取。
func validSysConfigType(typ int) bool {
	switch typ {
	case model.SysConfigTypeObject,
		model.SysConfigTypeArray,
		model.SysConfigTypeString,
		model.SysConfigTypeInteger,
		model.SysConfigTypeFloat,
		model.SysConfigTypeBoolean:
		return true
	default:
		return false
	}
}

// sysConfigTypeName 返回配置类型名称，便于错误排查。
func sysConfigTypeName(typ int) string {
	switch typ {
	case model.SysConfigTypeObject:
		return "object"
	case model.SysConfigTypeArray:
		return "array"
	case model.SysConfigTypeString:
		return "string"
	case model.SysConfigTypeInteger:
		return "integer"
	case model.SysConfigTypeFloat:
		return "float"
	case model.SysConfigTypeBoolean:
		return "boolean"
	default:
		return "unknown"
	}
}
