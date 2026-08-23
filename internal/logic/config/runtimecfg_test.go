package config

import (
	"os"
	"testing"

	"api/common/runtimecfg"
)

// TestMain 为配置包测试固定共享缓存使用的 AppID 命名空间。
func TestMain(m *testing.M) {
	runtimecfg.Set(runtimecfg.Snapshot{AppID: "site-a"})
	os.Exit(m.Run())
}
