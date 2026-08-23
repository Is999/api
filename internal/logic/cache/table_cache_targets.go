package cache

import (
	"context"
	"strings"
	"time"

	keys "api/common/rediskeys"
	corelogic "api/internal/logic"
	"api/internal/model"
	"api/internal/svc"

	"github.com/Is999/go-utils/errors"
	tablecache "github.com/Is999/table-cache"
	"gorm.io/gorm"
)

// tableCacheTargets 返回 API 与 admin 共享的 table-cache 缓存目标。
func tableCacheTargets(base *corelogic.BaseLogic) []tablecache.Target {
	return []tablecache.Target{
		{
			Index:            "config_uuid",
			Title:            "系统常量配置",
			Key:              cacheTemplatePrefix(keys.SysConfigUUIDPattern),
			KeyTitle:         keys.SysConfigUUIDPattern,
			Type:             tablecache.TypeHash,
			Remark:           "系统常量配置缓存",
			TTL:              time.Hour,
			AllowEmptyMarker: true,
			Loader:           loadSysConfigTableCache(base),
		},
	}
}

// loadSysConfigTableCache 加载单个系统配置 Hash 缓存数据。
func loadSysConfigTableCache(base *corelogic.BaseLogic) tablecache.Loader {
	return func(ctx context.Context, params tablecache.LoadParams) ([]tablecache.Entry, error) {
		// UUID 允许冒号和内部空白，须还原完整后缀，不能按缓存分段截断查询条件。
		uuid := strings.Join(params.KeyParts, ":")
		if uuid == "" || uuid != strings.TrimSpace(uuid) {
			return nil, errors.Errorf("配置UUID不能为空或包含首尾空白")
		}
		// 回源固定读取写库，避免刚更新的数据被副本延迟覆盖到缓存。
		writeDB, err := tableCacheWriteDB(base, svc.DatabaseMain, "main")
		if err != nil {
			return nil, errors.Tag(err)
		}
		// UUID 唯一索引最多返回一行，只读取标识校验和缓存解码所需字段。
		var cfg model.SysConfig
		if err := writeDB.WithContext(ctx).Select("uuid", "type", "value").Where("uuid = ?", uuid).First(&cfg).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil, nil
			}
			return nil, errors.Tag(err)
		}
		// 数据库排序规则可能忽略大小写，别名不能写入无法按原 UUID 失效的缓存。
		if cfg.UUID != uuid {
			return nil, errors.Wrapf(gorm.ErrRecordNotFound, "配置UUID不匹配: %s", uuid)
		}
		// 业务读取只解码类型和值；标题、层级等管理信息仍从数据库获取。
		cache := map[string]any{
			"type":  cfg.Type,
			"value": cfg.Value,
		}
		return []tablecache.Entry{{
			Key:   params.Key,
			Type:  tablecache.TypeHash,
			Value: cache,
		}}, nil
	}
}
