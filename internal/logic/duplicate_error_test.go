package logic

import (
	"testing"

	"github.com/Is999/go-utils/errors"
	drivermysql "github.com/go-sql-driver/mysql"
)

// TestIsMySQLDuplicateEntryErrorDetectsWrappedDuplicate 确保包装后的 1062 错误仍可识别且不会误判死锁。
func TestIsMySQLDuplicateEntryErrorDetectsWrappedDuplicate(t *testing.T) {
	// 直接构造驱动错误以验证错误链识别，不实际触发数据库唯一索引冲突。
	duplicateErr := &drivermysql.MySQLError{Number: mysqlDuplicateEntryErrorNumber, Message: "Duplicate entry"}
	if !IsMySQLDuplicateEntryError(errors.Wrap(duplicateErr, "create user")) {
		t.Fatal("期望识别被包装的 MySQL duplicate entry 错误")
	}
	otherErr := &drivermysql.MySQLError{Number: 1213, Message: "Deadlock found"}
	if IsMySQLDuplicateEntryError(errors.Wrap(otherErr, "create user")) {
		t.Fatal("不应把非 duplicate entry 的 MySQL 错误识别为重复数据")
	}
}
