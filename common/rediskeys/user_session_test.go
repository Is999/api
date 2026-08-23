package keys

import "testing"

// TestUserSessionKeys 验证会话 Key 文本和 Redis Cluster hash tag。
func TestUserSessionKeys(t *testing.T) {
	useAppID(t, "1")
	// 撤销标记与三个会话键共用 UID hash tag；这里只断言命名，不建立真实 Cluster。
	want := []string{
		"app:1:user:session:{42}",
		"app:1:user:session:index:{42}",
		"app:1:user:session:auth_version:{42}",
		"app:1:user:session:revoked:{42}:session-id",
	}
	got := append(UserSessionKeys(42), UserSessionRevokedKey(42, "session-id"))
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("UserSessionKeys()[%d] = %q, want %q", index, got[index], want[index])
		}
	}
}
