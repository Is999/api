package keys

import (
	"testing"

	"api/common/runtimecfg"
)

// useAppID 临时切换测试进程的 app_id，并在用例结束后恢复。
func useAppID(t *testing.T, appID string) {
	t.Helper()
	// 这里只恢复进程级快照，不能隔离并发用例；调用此夹具的测试不得 t.Parallel。
	prev := runtimecfg.Get()
	runtimecfg.Set(runtimecfg.Snapshot{AppID: appID})
	t.Cleanup(func() {
		runtimecfg.Restore(prev)
	})
}
