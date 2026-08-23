//go:build integration

package mysqlx

import (
	"context"
	"crypto/rand"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"api/internal/config"

	drivermysql "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
)

// TestNewTransactionBoundaries 使用独立 MySQL 库，验证正式工厂的单写与显式事务边界。
func TestNewTransactionBoundaries(t *testing.T) {
	// 沿用集成套件的可丢弃库约定，只创建和删除本次随机命名的测试表。
	dsn := strings.TrimSpace(os.Getenv("INTEGRATION_MYSQL_DSN"))
	if dsn == "" {
		t.Skip("INTEGRATION_MYSQL_DSN 未配置，跳过真实 MySQL 工厂回归")
	}
	replicaConfig, err := drivermysql.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("解析测试 DSN 失败: %v", err)
	}
	// 同库使用不同驱动选项建立独立读池，只验证 resolver 装配，不模拟复制延迟。
	replicaConfig.InterpolateParams = !replicaConfig.InterpolateParams
	for _, test := range []struct {
		name     string   // 区分只写库与启用读池的正式初始化路径。
		replicas []string // 空列表不注册 resolver，非空列表指向同一测试库。
	}{
		{name: "source_only"},
		{name: "with_read_pool", replicas: []string{replicaConfig.FormatDSN()}},
	} {
		t.Run(test.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
			defer cancel()
			db, err := New(ctx, config.MySQLConfig{
				WriteDataSource: dsn,
				ReadDataSources: test.replicas,
				MaxOpenConns:    2,
				MaxIdleConns:    1,
				ConnMaxLifetime: 60,
			}, config.ObservabilityConfig{SlowSQLMs: 1000})
			if err != nil {
				t.Fatalf("正式 New 初始化失败: %v", err)
			}
			t.Cleanup(func() {
				if err := Close(db); err != nil {
					t.Errorf("关闭正式连接池失败: %v", err)
				}
			})
			if !db.Config.SkipDefaultTransaction {
				t.Fatal("正式 New 必须关闭 GORM 默认事务")
			}
			db = db.WithContext(ctx)

			// InnoDB 与业务初始化资产一致，随机表名隔离重复运行和并行套件。
			table := "mysql_tx_test_" + strings.ToLower(rand.Text())
			if err := db.Table(table).Set("gorm:table_options", "ENGINE=InnoDB").Migrator().CreateTable(&mysqlTransactionRow{}); err != nil {
				t.Fatalf("创建独占测试表失败: %v", err)
			}
			t.Cleanup(func() {
				// 清理使用独立期限，避免用例取消后遗留测试表。
				cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
				defer cleanupCancel()
				if err := db.WithContext(cleanupCtx).Migrator().DropTable(table); err != nil {
					t.Errorf("删除独占测试表失败: %v", err)
				}
			})
			// 复用表名但隔离每次语句，避免前一次写入条件污染终态查询。
			db = db.Table(table).Session(&gorm.Session{})
			row := mysqlTransactionRow{ID: 1, Title: "before"}

			wantTransaction := false // 只在下方显式 Transaction 闭包内允许持有 *sql.Tx。
			writes := map[bool]int{} // 按是否持有事务计数，避免回调未执行造成假通过。
			checkTransaction := func(tx *gorm.DB) {
				_, inTransaction := tx.Statement.ConnPool.(gorm.TxCommitter)
				writes[inTransaction]++
				if inTransaction != wantTransaction {
					t.Errorf("写入事务状态 = %v，期望 %v", inTransaction, wantTransaction)
				}
			}
			// 在实际 SQL 前观测连接，默认 BEGIN 若被恢复会在此暴露。
			if err := db.Callback().Create().Before("gorm:create").Register("test:transaction_boundary", checkTransaction); err != nil {
				t.Fatal(err)
			}
			if err := db.Callback().Update().Before("gorm:update").Register("test:transaction_boundary", checkTransaction); err != nil {
				t.Fatal(err)
			}
			if err := db.Callback().Delete().Before("gorm:delete").Register("test:transaction_boundary", checkTransaction); err != nil {
				t.Fatal(err)
			}

			if err := db.Create(&row).Error; err != nil {
				t.Fatalf("单条新增失败: %v", err)
			}
			if err := db.Model(&row).Update("title", "updated").Error; err != nil {
				t.Fatalf("单条更新失败: %v", err)
			}
			stop := errors.New("测试主动终止事务")
			for _, rollback := range []bool{true, false} {
				wantTransaction = true
				err := db.Transaction(func(tx *gorm.DB) error {
					if err := tx.Model(&row).Update("title", "committed").Error; err != nil {
						return err
					}
					if rollback {
						return stop
					}
					return nil
				})
				wantTransaction = false
				if rollback && !errors.Is(err, stop) || !rollback && err != nil {
					t.Fatalf("显式事务 rollback=%v，返回错误=%v", rollback, err)
				}
				// 回滚必须恢复前值，成功事务必须在返回后对独立查询可见。
				wantTitle := "committed"
				if rollback {
					wantTitle = "updated"
				}
				var stored mysqlTransactionRow
				if err := db.Where("id = ?", row.ID).Take(&stored).Error; err != nil {
					t.Fatalf("读取事务终态失败: %v", err)
				}
				if stored.Title != wantTitle {
					t.Fatalf("事务 rollback=%v，title=%q，期望 %q", rollback, stored.Title, wantTitle)
				}
			}
			if err := db.Delete(&row).Error; err != nil {
				t.Fatalf("单条删除失败: %v", err)
			}
			var count int64
			if err := db.Model(&mysqlTransactionRow{}).Where("id = ?", row.ID).Count(&count).Error; err != nil || count != 0 {
				t.Fatalf("删除终态 count=%d，error=%v", count, err)
			}
			if writes[false] != 3 || writes[true] != 2 {
				t.Fatalf("实际写入计数 = %v，期望非事务 3 次、显式事务 2 次", writes)
			}
		})
	}
}

// mysqlTransactionRow 只承载事务终态，不引用或修改业务表结构。
type mysqlTransactionRow struct {
	ID    uint64 `gorm:"primaryKey;autoIncrement:false"` // 固定主键将所有验证限制为一行。
	Title string `gorm:"type:varchar(32);not null"`      // 回滚与提交通过读取该字段区分。
}
