package main

import (
	"context"
	stderrors "errors"
	"strings"
	"testing"
	"time"

	"github.com/Is999/go-utils/errors"
)

// TestMergeMigrationCloseError 验证关闭失败会产生非空结果，且不会覆盖迁移主错误链。
func TestMergeMigrationCloseError(t *testing.T) {
	runErr := stderrors.New("migration failed")
	closeErr := stderrors.New("close failed")
	merged := mergeMigrationCloseError(runErr, closeErr)
	if !errors.Is(merged, runErr) || !strings.Contains(merged.Error(), closeErr.Error()) {
		t.Fatalf("合并错误未保留主错误和关闭详情: %v", merged)
	}
	if got := mergeMigrationCloseError(nil, closeErr); !errors.Is(got, closeErr) {
		t.Fatalf("仅关闭失败时未返回关闭错误: %v", got)
	}
	if got := mergeMigrationCloseError(runErr, nil); !errors.Is(got, runErr) {
		t.Fatalf("关闭成功时未保留迁移错误: %v", got)
	}
}

// TestRunRejectsUnboundedMigrationTimeout 确保非正值或超大时限在读取配置和连接数据库前就被拒绝。
func TestRunRejectsUnboundedMigrationTimeout(t *testing.T) {
	for _, timeout := range []time.Duration{0, -time.Second, maxMigrationTimeout + time.Second} {
		err := run(context.Background(), "/missing/config.yaml", actionStatus, false, false, timeout)
		if err == nil || !strings.Contains(err.Error(), "总时限") {
			t.Fatalf("run(timeout=%s) error = %v, want timeout validation", timeout, err)
		}
	}
}

// TestRunHonorsCanceledParentContext 确保终止信号取消后不再读取配置或建立新连接。
func TestRunHonorsCanceledParentContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := run(ctx, "/missing/config.yaml", actionStatus, false, false, time.Minute)
	if err == nil || !stderrors.Is(err, context.Canceled) {
		t.Fatalf("run(canceled) error = %v, want context.Canceled", err)
	}
}
