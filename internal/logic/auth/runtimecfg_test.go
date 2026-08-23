package auth

import (
	"os"
	"testing"

	"api/common/runtimecfg"
)

// TestMain 为认证包测试固定 Redis Key 使用的 AppID 命名空间。
func TestMain(m *testing.M) {
	runtimecfg.Set(runtimecfg.Snapshot{AppID: "site-a"})
	os.Exit(m.Run())
}
