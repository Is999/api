package logic

import (
	"testing"

	"api/common/runtimecfg"
)

// useRuntimeAppID 模拟测试进程完成启动配置发布。
func useRuntimeAppID(t *testing.T, appID string) {
	t.Helper()
	// 修改的是进程级快照，调用此辅助方法的用例不能并行运行。
	prev := runtimecfg.Get()
	runtimecfg.Set(runtimecfg.Snapshot{AppID: appID})
	t.Cleanup(func() {
		runtimecfg.Restore(prev)
	})
}
