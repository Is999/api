package configload

import (
	"context"
	"testing"
	"time"
)

// TestRedisSegmentManagerCloseCancelsAndJoinsWorkers 验证停机取消预取并等待任务退出，关闭后禁止新增任务。
func TestRedisSegmentManagerCloseCancelsAndJoinsWorkers(t *testing.T) {
	runtimeCtx, cancel := context.WithCancel(context.Background())
	manager := &redisSegmentManager{
		token:     ^uint64(0),
		states:    map[string]*segmentState{},
		ctx:       runtimeCtx,
		cancel:    cancel,
		closeDone: make(chan struct{}),
	}
	if !manager.startWorker() {
		t.Fatal("预取任务登记失败")
	}
	workerDone := make(chan struct{})
	go func() {
		defer manager.workers.Done()
		defer close(workerDone)
		<-manager.ctx.Done()
	}()

	closeCtx, closeCancel := context.WithTimeout(context.Background(), time.Second)
	defer closeCancel()
	if err := manager.Close(closeCtx); err != nil {
		t.Fatalf("Close() error=%v", err)
	}
	select {
	case <-workerDone:
	default:
		t.Fatal("Close 返回时预取任务仍未退出")
	}
	if manager.startWorker() {
		manager.workers.Done()
		t.Fatal("关闭后不应登记新的预取任务")
	}
}
