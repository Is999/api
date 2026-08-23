package config

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	keys "api/common/rediskeys"
	corelogic "api/internal/logic"
	cachelogic "api/internal/logic/cache"
	"api/internal/model"
	"api/internal/svc"

	"github.com/Is999/go-utils/errors"
	tablecache "github.com/Is999/table-cache"
)

const (
	sysConfigCacheFieldType  = "type"  // Admin 共享的类型枚举，决定缓存值的解码方式。
	sysConfigCacheFieldValue = "value" // 配置持久化的 JSON 原文，读取时按类型还原。
)

// ErrSysConfigNotFound 表示指定 uuid 的系统配置不存在。
var ErrSysConfigNotFound = errors.New("系统配置不存在")

// SysConfigLogic 承载系统配置缓存读取与刷新能力。
type SysConfigLogic struct {
	*corelogic.BaseLogic // 复用上下文、数据库、Redis 和日志能力
}

// NewSysConfigLogic 创建系统配置业务逻辑对象。
func NewSysConfigLogic(ctx context.Context, svcCtx *svc.ServiceContext) *SysConfigLogic {
	return &SysConfigLogic{BaseLogic: corelogic.NewBaseLogicWithContext(ctx, svcCtx)}
}

// GetCachedValue 读取指定配置值，优先使用 Redis 缓存，缺失时回源主库重建缓存。
func (l *SysConfigLogic) GetCachedValue(uuid string) (any, error) {
	cache, err := l.getCachedEntry(uuid)
	if err != nil {
		return nil, errors.Tag(err)
	}
	typ, err := sysConfigCacheType(uuid, cache)
	if err != nil {
		return nil, errors.Tag(err)
	}
	return decodeSysConfigValue(typ, cache[sysConfigCacheFieldValue])
}

// getCachedEntry 读取指定配置缓存快照，缺失时回源主库重建缓存。
func (l *SysConfigLogic) getCachedEntry(uuid string) (map[string]string, error) {
	if !validSysConfigUUID(uuid) {
		return nil, errors.Errorf("系统配置 uuid 不能为空或包含首尾空白")
	}
	manager, err := cachelogic.TableCacheManager(l.BaseLogic)
	if err != nil {
		return nil, errors.Tag(err)
	}
	var cache map[string]string
	// 锁、回源和空值写入交给 table-cache，业务层只解释最终缓存状态。
	result, err := manager.LoadThrough(l.Ctx, l.sysConfigCacheKey(uuid), &cache, nil)
	if err != nil {
		return nil, errors.Tag(err)
	}
	if result.State == tablecache.LookupStateEmpty || len(cache) == 0 {
		return nil, ErrSysConfigNotFound
	}
	if corelogic.CacheIsEmptyMarker(cache[sysConfigCacheFieldValue]) {
		return nil, ErrSysConfigNotFound
	}
	return cache, nil
}

// RenewByUUID 删除并重新加载指定配置缓存。
func (l *SysConfigLogic) RenewByUUID(uuid string) error {
	if !validSysConfigUUID(uuid) {
		return errors.Errorf("系统配置 uuid 不能为空或包含首尾空白")
	}
	manager, err := cachelogic.TableCacheManager(l.BaseLogic)
	if err != nil {
		return errors.Tag(err)
	}
	key := l.sysConfigCacheKey(uuid)
	// 删除先建立失效栅栏，避免刷新复用更新前已经开始的回源结果。
	if err := manager.DeleteByKey(l.Ctx, key); err != nil {
		return errors.Tag(err)
	}
	// 失效成功后再读取主库；失败保留未命中状态，交由后续读穿重试。
	return manager.RefreshByKey(l.Ctx, key)
}

// GetCacheHash 返回原始缓存投影供诊断，未命中时返回空 map，不回源数据库。
func (l *SysConfigLogic) GetCacheHash(uuid string) (map[string]string, error) {
	if !validSysConfigUUID(uuid) {
		return nil, errors.Errorf("系统配置 uuid 不能为空或包含首尾空白")
	}
	if l.Redis() == nil {
		return nil, errors.Errorf("Redis 未初始化")
	}
	return l.Redis().HGetAll(l.Ctx, l.sysConfigCacheKey(uuid)).Result()
}

// sysConfigCacheKey 生成当前站点下的系统配置缓存 Key。
func (l *SysConfigLogic) sysConfigCacheKey(uuid string) string {
	return cachelogic.TableCachePhysicalKey(l.BaseLogic, fmt.Sprintf(keys.SysConfigUUID, uuid))
}

// validSysConfigUUID 保证注册表、数据库查询和 Redis key 使用同一个精确标识。
func validSysConfigUUID(uuid string) bool {
	return uuid != "" && uuid == strings.TrimSpace(uuid)
}

// sysConfigCacheType 解析系统配置缓存声明类型。
func sysConfigCacheType(uuid string, cache map[string]string) (int, error) {
	raw := cache[sysConfigCacheFieldType]
	if raw == "" {
		return 0, errors.Errorf("系统配置缓存类型为空 uuid=%s", uuid)
	}
	if raw != strings.TrimSpace(raw) {
		return 0, errors.Errorf("系统配置缓存类型包含首尾空白 uuid=%s", uuid)
	}
	typ, err := strconv.Atoi(raw)
	if err != nil {
		return 0, errors.Wrapf(err, "系统配置缓存类型非法 uuid=%s", uuid)
	}
	return typ, nil
}

// decodeSysConfigValue 把缓存中的字符串值还原为业务类型。
func decodeSysConfigValue(typ int, raw string) (any, error) {
	// 缓存值禁止隐式修剪，避免签名值和业务读取结果不一致。
	if raw != strings.TrimSpace(raw) {
		return nil, errors.Errorf("系统配置值不能包含首尾空白")
	}
	switch typ {
	case model.SysConfigTypeGroup:
		return nil, nil
	case model.SysConfigTypeObject, model.SysConfigTypeArray:
		// 容器内数字保留原始文本；只接受单个 JSON 值，不能把尾随内容静默丢弃。
		decoder := json.NewDecoder(strings.NewReader(raw))
		decoder.UseNumber()
		var value any
		if err := decoder.Decode(&value); err != nil {
			return nil, errors.Tag(err)
		}
		var extra any
		if err := decoder.Decode(&extra); err != io.EOF {
			return nil, errors.Errorf("系统配置值只能包含一个 JSON 值")
		}
		// 类型枚举仍约束容器形状，null 不能作为对象或数组配置。
		if typ == model.SysConfigTypeObject {
			if _, ok := value.(map[string]any); !ok {
				return nil, errors.Errorf("系统配置 Object 值必须是 JSON 对象")
			}
		} else if _, ok := value.([]any); !ok {
			return nil, errors.Errorf("系统配置 Array 值必须是 JSON 数组")
		}
		return value, nil
	case model.SysConfigTypeString:
		var value string
		if err := json.Unmarshal([]byte(raw), &value); err != nil {
			return nil, errors.Tag(err)
		}
		return value, nil
	case model.SysConfigTypeInteger:
		// 数值先验证 JSON 语法，再转换为业务使用的 Go 数值类型。
		if !json.Valid([]byte(raw)) {
			return 0, errors.Errorf("系统配置 Integer 值必须是合法 JSON 整数")
		}
		value, err := strconv.Atoi(raw)
		if err != nil {
			return 0, errors.Tag(err)
		}
		return value, nil
	case model.SysConfigTypeFloat:
		if !json.Valid([]byte(raw)) {
			return 0, errors.Errorf("系统配置 Float 值必须是合法 JSON 数字")
		}
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return 0, errors.Tag(err)
		}
		return value, nil
	case model.SysConfigTypeBoolean:
		// 数据库存储约定布尔值只使用 0/1，不接受其它文本别名。
		switch raw {
		case "0":
			return false, nil
		case "1":
			return true, nil
		default:
			return nil, errors.Errorf("系统配置布尔值必须是 0 或 1")
		}
	default:
		return nil, errors.Errorf("不支持的系统配置类型: %d", typ)
	}
}
