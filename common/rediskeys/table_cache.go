package keys

import "strings"

// TableCachePrefix 返回当前应用 table-cache 真实 Redis key 前缀。
// 最终 key 形如 `app:{app_id}:table:{key}`，业务 key 本身不带 table 前缀。
func TableCachePrefix() string {
	return WithPrefix(tableCacheLogicalPrefix())
}

// IsTableCacheKey 判断 key 是否属于当前应用的 table-cache 真实 Redis key。
func IsTableCacheKey(key string) bool {
	if key == "" || key != strings.TrimSpace(key) {
		return false
	}
	prefix := Prefix()
	if prefix == "" || !strings.HasPrefix(key, prefix) {
		return false
	}
	logicalKey := TrimPrefix(key)
	logicalPrefix := tableCacheLogicalPrefix()
	return strings.HasPrefix(logicalKey, logicalPrefix) && len(logicalKey) > len(logicalPrefix)
}

// TrimTableCachePrefix 去掉当前应用的 table-cache 项目前缀，返回业务逻辑 key。
// 只有当前 app_id 的 `app:{app_id}:table:` 会被截断，跨站点或直接 Redis key 会保持原样。
func TrimTableCachePrefix(key string) string {
	if !IsTableCacheKey(key) {
		return key
	}
	return strings.TrimPrefix(TrimPrefix(key), tableCacheLogicalPrefix())
}

// tableCacheLogicalPrefix 返回不含 app_id 的 table-cache 逻辑前缀。
func tableCacheLogicalPrefix() string {
	return tableCacheSegment + ":"
}
