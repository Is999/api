package keys

import "testing"

// TestTableCachePrefix 确保表缓存前缀包含当前 AppID 与固定 table 段。
func TestTableCachePrefix(t *testing.T) {
	useAppID(t, "site-a")
	if got, want := TableCachePrefix(), "app:site-a:table:"; got != want {
		t.Fatalf("TableCachePrefix() = %q, want %q", got, want)
	}
}

// TestIsTableCacheKey 确保扫描只识别当前 AppID 下的完整表缓存 key。
func TestIsTableCacheKey(t *testing.T) {
	tests := []struct {
		name  string // 用于 t.Run 区分当前、外部和非法表缓存 key
		appID string // 作为表缓存命名空间比较基准
		key   string // 覆盖完整、残缺和非表缓存 key
		want  bool   // 仅当前作用域的完整表缓存 key 应命中
	}{
		{name: "current app table key", appID: "site-a", key: "app:site-a:table:config_uuid:featureFlag", want: true},
		{name: "other app table key", appID: "site-a", key: "app:site-b:table:config_uuid:featureFlag", want: false},
		{name: "direct app key", appID: "site-a", key: "app:site-a:config_uuid:featureFlag", want: false},
		{name: "logical table segment", appID: "site-a", key: "table:config_uuid:featureFlag", want: false},
		{name: "incomplete table prefix", appID: "site-a", key: "app:site-a:table", want: false},
		{name: "empty app id", appID: "", key: "app:site-a:table:config_uuid:featureFlag", want: false},
		{name: "whitespace key", appID: "site-a", key: " app:site-a:table:config_uuid:featureFlag ", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			useAppID(t, tt.appID)
			if got := IsTableCacheKey(tt.key); got != tt.want {
				t.Fatalf("IsTableCacheKey() = %t, want %t", got, tt.want)
			}
		})
	}
}

// TestTrimTableCachePrefix 确保仅从当前 AppID 的表缓存 key 移除固定前缀。
func TestTrimTableCachePrefix(t *testing.T) {
	tests := []struct {
		name  string // 用于 t.Run 区分裁剪和原样保留分支
		appID string // 作为表缓存命名空间比较基准
		key   string // 覆盖当前、外部和普通 key
		want  string // 保存裁剪结果或应保留的原值
	}{
		{name: "trims table key", appID: "site-a", key: "app:site-a:table:config_uuid:featureFlag", want: "config_uuid:featureFlag"},
		{name: "keeps other app table key", appID: "site-a", key: "app:site-b:table:config_uuid:featureFlag", want: "app:site-b:table:config_uuid:featureFlag"},
		{name: "keeps direct app key", appID: "site-a", key: "app:site-a:config_uuid:featureFlag", want: "app:site-a:config_uuid:featureFlag"},
		{name: "keeps logical key", appID: "site-a", key: "config_uuid:featureFlag", want: "config_uuid:featureFlag"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			useAppID(t, tt.appID)
			if got := TrimTableCachePrefix(tt.key); got != tt.want {
				t.Fatalf("TrimTableCachePrefix() = %q, want %q", got, tt.want)
			}
		})
	}
}
