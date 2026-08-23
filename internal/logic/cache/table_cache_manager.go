package cache

import (
	"strings"
	"time"

	keys "api/common/rediskeys"
	corelogic "api/internal/logic"
	"api/internal/svc"

	"github.com/Is999/go-utils/errors"
	tablecache "github.com/Is999/table-cache"
	"gorm.io/gorm"
)

const (
	// TableCacheMetricsSubsystem 表示表缓存 Prometheus 指标子系统。
	TableCacheMetricsSubsystem = "tcache"
	// tableCacheRebuildLockTTL 表示 table-cache 回源锁默认持有时长。
	tableCacheRebuildLockTTL = 10 * time.Second
	// tableCacheWaitStep 表示等待其他实例回源完成的单次等待时间。
	tableCacheWaitStep = 80 * time.Millisecond
)

// TableCacheManager 创建 API 的表数据缓存管理器。
func TableCacheManager(base *corelogic.BaseLogic) (*tablecache.Manager, error) {
	if base == nil || base.Redis() == nil {
		return nil, errors.Errorf("Redis未初始化")
	}
	// Manager 是请求级组装；Redis 和指标复用进程资源，回源锁及空值 TTL 交由 table-cache 管理。
	return tablecache.NewManager(
		tablecache.NewRedisStore(base.Redis()),
		tableCacheTargets(base),
		tablecache.WithKeyPrefix(tableCacheKeyPrefix(base)),
		tablecache.WithEmptyMarker(keys.EmptyValueMarker, corelogic.EmptyCacheTTL()),
		tablecache.WithLockTTL(tableCacheRebuildLockTTL),
		tablecache.WithWait(tableCacheWaitStep, 3),
		tablecache.WithMetrics(base.Svc.TableCacheMetrics),
	)
}

// tableCacheKeyPrefix 返回当前站点 table-cache 托管缓存使用的 Redis key 前缀。
func tableCacheKeyPrefix(base *corelogic.BaseLogic) string {
	if base == nil || base.AppID() == "" {
		return ""
	}
	return keys.TableCachePrefix()
}

// TableCachePhysicalKey 把逻辑缓存 key 转换为 table-cache 真实 Redis key。
func TableCachePhysicalKey(base *corelogic.BaseLogic, key string) string {
	prefix := tableCacheKeyPrefix(base)
	if key == "" || key != strings.TrimSpace(key) || prefix == "" || keys.HasPrefix(key) {
		return ""
	}
	return prefix + key
}

// tableCacheWriteDB 获取表缓存回源主库连接。
func tableCacheWriteDB(base *corelogic.BaseLogic, database svc.DBName, databaseLabel string) (*gorm.DB, error) {
	if base == nil || base.Svc == nil {
		return nil, errors.Errorf("服务上下文未初始化")
	}
	writeDB := base.Svc.WriteDB(database)
	if writeDB == nil {
		return nil, errors.Errorf("%s主库未初始化", strings.TrimSpace(databaseLabel))
	}
	return writeDB, nil
}

// cacheTemplatePrefix 返回模板型缓存键的固定前缀部分。
func cacheTemplatePrefix(key string) string {
	index := strings.Index(key, "{")
	if index >= 0 {
		return strings.TrimSpace(key[:index])
	}
	index = strings.Index(key, "%")
	if index >= 0 {
		return strings.TrimSpace(key[:index])
	}
	return strings.TrimSpace(key)
}
