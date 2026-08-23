package keys

import (
	"api/common/runtimecfg"
	"strings"
)

// HasPrefix 判断值是否已经带有完整 app_id 命名空间。
func HasPrefix(key string) bool {
	_, ok := Owner(key)
	return ok
}

// Owner 解析完整 app_id 命名空间中的 app_id。
func Owner(key string) (string, bool) {
	if key == "" || key != strings.TrimSpace(key) || !strings.HasPrefix(key, ScopeRoot) {
		return "", false
	}
	rest := strings.TrimPrefix(key, ScopeRoot)
	index := strings.Index(rest, ":")
	if index <= 0 || index >= len(rest)-1 {
		return "", false
	}
	owner := rest[:index]
	if !validScopeOwner(owner) {
		return "", false
	}
	return owner, true
}

// IsForeignKey 判断完整 Redis key 是否属于其它 app_id。
func IsForeignKey(key string) bool {
	ownerAppID, ok := Owner(key)
	appID := runtimecfg.AppID()
	return ok && (appID == "" || ownerAppID != appID)
}

// Prefix 返回当前应用 Redis key 命名空间前缀。
func Prefix() string {
	appID := runtimecfg.AppID()
	if appID == "" {
		return ""
	}
	return ScopeRoot + appID + ":"
}

// WithPrefix 给逻辑 key 添加当前应用前缀；空白和已带 app: 前缀的输入均拒绝。
func WithPrefix(key string) string {
	if key == "" || key != strings.TrimSpace(key) {
		return ""
	}
	// 调用方只能传逻辑 key；完整 key 再次进入该入口说明分层职责混用。
	if strings.HasPrefix(key, ScopeRoot) {
		return ""
	}
	prefix := Prefix()
	if prefix == "" {
		return ""
	}
	return prefix + key
}

// validScopeOwner 约束 Redis key 中的 app_id，避免畸形前缀被当成另一套逻辑 key。
func validScopeOwner(owner string) bool {
	if owner == "" || len(owner) > 64 {
		return false
	}
	for _, current := range owner {
		if (current >= 'a' && current <= 'z') ||
			(current >= 'A' && current <= 'Z') ||
			(current >= '0' && current <= '9') ||
			current == '-' || current == '_' || current == '.' {
			continue
		}
		return false
	}
	return true
}

// TrimPrefix 去掉任意合法 app_id 前缀；不执行归属鉴权，跨站点隔离须先检查 Owner。
func TrimPrefix(key string) string {
	if key != strings.TrimSpace(key) {
		return key
	}
	appID, ok := Owner(key)
	if !ok {
		return key
	}
	return strings.TrimPrefix(key, ScopeRoot+appID+":")
}
