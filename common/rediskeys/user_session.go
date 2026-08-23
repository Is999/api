package keys

import "fmt"

// UserSessionHashKey 返回当前应用下的用户会话 Hash Key。
func UserSessionHashKey(userID int64) string {
	return WithPrefix(fmt.Sprintf(UserSessionHash, userID))
}

// UserSessionIndexKey 返回当前应用下的用户会话索引 Key。
func UserSessionIndexKey(userID int64) string {
	return WithPrefix(fmt.Sprintf(UserSessionIndex, userID))
}

// UserSessionAuthVersionKey 返回当前应用下的用户认证版本 Key。
func UserSessionAuthVersionKey(userID int64) string {
	return WithPrefix(fmt.Sprintf(UserSessionAuthVersion, userID))
}

// UserSessionRevokedKey 生成精确 sid 撤销键，供退出与失败补偿在同槽 Lua 中共享撤销状态。
func UserSessionRevokedKey(userID int64, sessionID string) string {
	return WithPrefix(fmt.Sprintf(UserSessionRevoked, userID, sessionID))
}

// UserSessionKeys 返回会话 Lua 使用的同槽 Key，顺序为 Hash、索引、认证版本。
func UserSessionKeys(userID int64) []string {
	return []string{
		UserSessionHashKey(userID),
		UserSessionIndexKey(userID),
		UserSessionAuthVersionKey(userID),
	}
}
