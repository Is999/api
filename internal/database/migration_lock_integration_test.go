//go:build integration

package database

import (
	"context"
	stderrors "errors"
	"strings"
	"testing"
	"time"
)

// TestMigrationLockSerializesWithMySQL 使用真实 MySQL 校验迁移锁互斥、释放和再次获取。
func TestMigrationLockSerializesWithMySQL(t *testing.T) {
	db := openIntegrationMySQL(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("获取底层 MySQL 连接失败: %v", err)
	}

	entered := make(chan struct{})
	release := make(chan struct{})
	firstDone := make(chan error, 1)
	// 同一 MySQL 连接池提供两条会话，用命名锁验证跨连接互斥。
	go func() {
		firstDone <- WithMigrationLock(context.Background(), sqlDB, "api:test-schema-migration", time.Second, func() error {
			close(entered)
			<-release
			return nil
		})
	}()
	<-entered

	err = WithMigrationLock(context.Background(), sqlDB, "api:test-schema-migration", 0, func() error {
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "超时") {
		t.Fatalf("并发获取迁移锁 error = %v, want timeout", err)
	}
	close(release)
	if err = <-firstDone; err != nil {
		t.Fatalf("释放首个迁移锁失败: %v", err)
	}
	if err = WithMigrationLock(context.Background(), sqlDB, "api:test-schema-migration", 0, func() error { return nil }); err != nil {
		t.Fatalf("迁移锁释放后不能再次获取: %v", err)
	}
}

// TestMigrationLockRejectsSingleConnectionPool 验证迁移锁不会在单连接池中自锁等待。
func TestMigrationLockRejectsSingleConnectionPool(t *testing.T) {
	db := openIntegrationMySQL(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("获取底层 MySQL 连接失败: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)

	called := false
	err = WithMigrationLock(context.Background(), sqlDB, "api:test-single-connection", 0, func() error {
		called = true
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "至少需要 2 条连接") {
		t.Fatalf("单连接池迁移锁 error = %v, want pool capacity error", err)
	}
	if called {
		t.Fatal("单连接池不应执行迁移函数")
	}
}

// TestMigrationLockReleasesAfterRunError 验证迁移失败后命名锁仍会释放。
func TestMigrationLockReleasesAfterRunError(t *testing.T) {
	db := openIntegrationMySQL(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("获取底层 MySQL 连接失败: %v", err)
	}
	runErr := stderrors.New("migration failed")
	err = WithMigrationLock(context.Background(), sqlDB, "api:test-run-error", 0, func() error {
		return runErr
	})
	if !stderrors.Is(err, runErr) {
		t.Fatalf("WithMigrationLock() error = %v, want run error", err)
	}
	if err = WithMigrationLock(context.Background(), sqlDB, "api:test-run-error", 0, func() error { return nil }); err != nil {
		t.Fatalf("迁移失败后不能重新获取锁: %v", err)
	}
}
