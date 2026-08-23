//go:build integration

package database

import (
	"strings"
	"testing"
)

// TestMigrationRetriesUnregisteredAssetOnMySQL 登记失败不回滚已提交 SQL，重跑可以安全补登记。
func TestMigrationRetriesUnregisteredAssetOnMySQL(t *testing.T) {
	db := openIntegrationMySQL(t)
	tables := []string{schemaMigrationTable, "migration_unregistered"}
	prepareIntegrationTables(t, db, tables...)
	migration := testMigration("202606050001", "unregistered")
	migration.SQL = "CREATE TABLE IF NOT EXISTS migration_unregistered (id bigint PRIMARY KEY); INSERT IGNORE INTO migration_unregistered (id) VALUES (7)"
	migration.Checksum = sha256Hex(migration.SQL)
	store := NewGormMigrationStore(db)

	// 缺失登记表只使最后的登记写入失败，前面的建表与种子均已提交。
	if err := store.ExecuteMigration(t.Context(), migration); err == nil || !strings.Contains(err.Error(), "登记数据库迁移版本失败") {
		t.Fatalf("期望资产成功后登记失败: %v", err)
	}
	if !db.Migrator().HasTable("migration_unregistered") {
		t.Fatal("登记失败前的 DDL 应已提交")
	}
	results, err := RunMigrations(t.Context(), store, []Migration{migration}, MigrationRunOptions{})
	if err != nil || len(results) != 1 || results[0].Status != MigrationStatusExecuted {
		t.Fatalf("登记失败后重跑结果不符: results=%+v err=%v", results, err)
	}
	results, err = RunMigrations(t.Context(), store, []Migration{migration}, MigrationRunOptions{})
	if err != nil || len(results) != 1 || results[0].Status != MigrationStatusApplied {
		t.Fatalf("补登记后再次执行结果不符: results=%+v err=%v", results, err)
	}
	var ids []int64
	if err = db.Table("migration_unregistered").Pluck("id", &ids).Error; err != nil || len(ids) != 1 || ids[0] != 7 {
		t.Fatalf("登记失败重跑改变种子: ids=%v err=%v", ids, err)
	}
}

// TestMigrationResumesFailedAssetOnMySQL 验证 DDL 部分提交后可重试，成功资产不会重放。
func TestMigrationResumesFailedAssetOnMySQL(t *testing.T) {
	db := openIntegrationMySQL(t)
	tables := []string{schemaMigrationTable, "migration_resume_first", "migration_resume_second", "migration_resume_source"}
	prepareIntegrationTables(t, db, tables...)
	first := testMigration("202606050001", "resume_first")
	first.SQL = "CREATE TABLE IF NOT EXISTS migration_resume_first (id bigint PRIMARY KEY); INSERT IGNORE INTO migration_resume_first (id) VALUES (1)"
	first.Checksum = sha256Hex(first.SQL)
	second := testMigration("202606050002", "resume_second")
	second.SQL = "CREATE TABLE IF NOT EXISTS migration_resume_second (id bigint PRIMARY KEY); INSERT IGNORE INTO migration_resume_second (id) SELECT id FROM migration_resume_source"
	second.Checksum = sha256Hex(second.SQL)
	store := NewGormMigrationStore(db)
	migrations := []Migration{first, second}

	// 缺少来源表使第二个资产在建表后失败，已登记的第一个资产必须保留。
	results, err := RunMigrations(t.Context(), store, migrations, MigrationRunOptions{})
	if err == nil || !strings.Contains(err.Error(), "migration_resume_source") {
		t.Fatalf("期望来源表缺失导致 SQL 失败: %v", err)
	}
	if len(results) != 1 || results[0].Status != MigrationStatusExecuted || !db.Migrator().HasTable("migration_resume_second") {
		t.Fatalf("DDL 部分提交状态不符: %+v", results)
	}
	applied, err := store.AppliedMigrations(t.Context())
	if err != nil || len(applied) != 1 || applied[first.Version].Checksum != first.Checksum {
		t.Fatalf("仅成功资产应有登记: applied=%+v err=%v", applied, err)
	}

	// 补齐失败原因后原样重跑，不能要求删除已创建的表或修改迁移清单。
	if err = db.Exec("CREATE TABLE migration_resume_source (id bigint PRIMARY KEY)").Error; err != nil {
		t.Fatal(err)
	}
	if err = db.Table("migration_resume_source").Create(map[string]any{"id": 2}).Error; err != nil {
		t.Fatal(err)
	}
	results, err = RunMigrations(t.Context(), store, migrations, MigrationRunOptions{})
	if err != nil || len(results) != 2 || results[0].Status != MigrationStatusApplied || results[1].Status != MigrationStatusExecuted {
		t.Fatalf("续跑结果不符: results=%+v err=%v", results, err)
	}
	results, err = RunMigrations(t.Context(), store, migrations, MigrationRunOptions{})
	if err != nil || len(results) != 2 || results[0].Status != MigrationStatusApplied || results[1].Status != MigrationStatusApplied {
		t.Fatalf("重复执行结果不符: results=%+v err=%v", results, err)
	}
	for table, want := range map[string]int64{"migration_resume_first": 1, "migration_resume_second": 2} {
		var ids []int64
		if err = db.Table(table).Pluck("id", &ids).Error; err != nil || len(ids) != 1 || ids[0] != want {
			t.Fatalf("重跑改变业务行 table=%s ids=%v err=%v", table, ids, err)
		}
	}
}
