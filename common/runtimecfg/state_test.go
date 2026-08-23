package runtimecfg

import "testing"

// TestSetPreservesAppID 验证运行快照不再暗中改写启动配置。
func TestSetPreservesAppID(t *testing.T) {
	prev := Get()
	Set(Snapshot{AppID: " 215 "})
	t.Cleanup(func() {
		Restore(prev)
	})
	if got := AppID(); got != " 215 " {
		t.Fatalf("AppID() = %q, want exact input", got)
	}
}

// TestGetReturnsEmptyBeforeSet 确保空快照不会生成 AppID。
func TestGetReturnsEmptyBeforeSet(t *testing.T) {
	prev := Get()
	Set(Snapshot{})
	t.Cleanup(func() {
		Restore(prev)
	})
	if got := Get().AppID; got != "" {
		t.Fatalf("Get().AppID = %q, want empty", got)
	}
}

// TestRestoreSnapshot 确保测试或重载可恢复先前运行快照。
func TestRestoreSnapshot(t *testing.T) {
	prev := Get()
	Set(Snapshot{AppID: "new-app"})
	Restore(Snapshot{AppID: "old-app"})
	t.Cleanup(func() {
		Restore(prev)
	})
	if got := AppID(); got != "old-app" {
		t.Fatalf("AppID() = %q, want old-app", got)
	}
}
