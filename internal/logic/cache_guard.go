package logic

import (
	"time"

	keys "api/common/rediskeys"
)

// CacheIsEmptyMarker 判断缓存值是否为空值占位符。
func CacheIsEmptyMarker(value string) bool {
	return value == keys.EmptyValueMarker
}

// JitterTTL 为基础过期时间添加抖动，降低同类缓存集中失效导致的雪崩风险。
func JitterTTL(base time.Duration) time.Duration {
	if base <= 0 {
		return 0
	}
	jitterRange := base / 10
	if jitterRange <= 0 {
		jitterRange = time.Second
	}
	return base + time.Duration(time.Now().UnixNano()%int64(jitterRange))
}

// EmptyCacheTTL 为不存在的数据缓存约两分钟，并增加不超过 10% 的过期抖动。
func EmptyCacheTTL() time.Duration {
	return JitterTTL(2 * time.Minute)
}
