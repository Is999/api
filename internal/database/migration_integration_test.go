//go:build integration

package database

import (
	"context"
	stderrors "errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// integrationMySQLDSNEnv 必须指向可删除表的独立测试库，禁止复用业务库。
const integrationMySQLDSNEnv = "INTEGRATION_MYSQL_DSN"

// TestMigrationMetadataOwnershipWithMySQL 验证同库其它工具的登记不参与 API 资产判断。
func TestMigrationMetadataOwnershipWithMySQL(t *testing.T) {
	db := openIntegrationMySQL(t)
	prepareIntegrationTables(t, db, schemaMigrationTable, "admin_schema_migrations", "migration_metadata_isolation")
	migration := newMigration("202606220001", "shared_name", "api-only.sql", "CREATE TABLE IF NOT EXISTS migration_metadata_isolation (id bigint PRIMARY KEY); INSERT IGNORE INTO migration_metadata_isolation (id) VALUES (7)")
	foreign := []AppliedMigration{
		{Version: migration.Version, Name: migration.Name, Asset: "foreign.sql", Checksum: strings.Repeat("f", 64)},
		{Version: "foreign-version", Name: "foreign_name", Asset: "unknown.sql", Checksum: strings.Repeat("e", 64)},
	}
	// 固定测试表模拟独立工具的版本主键与名称唯一键，不借用 API 建表实现。
	if err := db.Exec("CREATE TABLE `admin_schema_migrations` (`version` varchar(32) NOT NULL PRIMARY KEY, `name` varchar(128) NOT NULL UNIQUE, `asset` varchar(255) NOT NULL, `checksum` char(64) NOT NULL)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Table("admin_schema_migrations").Create(&foreign).Error; err != nil {
		t.Fatal(err)
	}
	store := NewGormMigrationStore(db)
	applied, err := store.AppliedMigrations(t.Context())
	if err != nil || len(applied) != 0 {
		t.Fatalf("其它工具登记不得进入 API 集合: applied=%+v err=%v", applied, err)
	}
	migrations := []Migration{migration}
	for _, want := range []string{MigrationStatusExecuted, MigrationStatusApplied} {
		results, err := RunMigrations(t.Context(), store, migrations, MigrationRunOptions{})
		if err != nil || len(results) != 1 || results[0].Status != want {
			t.Fatalf("独立登记或重跑失败: results=%+v want=%s err=%v", results, want, err)
		}
	}
	applied, err = store.AppliedMigrations(t.Context())
	if err != nil || len(applied) != 1 || applied[migration.Version].Checksum != migration.Checksum {
		t.Fatalf("API 应只读取自身登记: applied=%+v err=%v", applied, err)
	}
	var appliedAt time.Time
	if err = db.Table(schemaMigrationTable).Select("applied_at").Where("version = ?", migration.Version).Scan(&appliedAt).Error; err != nil || appliedAt.IsZero() {
		t.Fatalf("登记时间应由数据库默认值生成: applied_at=%v err=%v", appliedAt, err)
	}
	// 本仓元数据损坏必须在新增待办资产执行前拒绝，不能因分表隔离放宽校验。
	pending := newMigration("202606220002", "pending_marker", "pending.sql", "INSERT INTO migration_metadata_isolation (id) VALUES (99)")
	for column, original := range map[string]string{"name": migration.Name, "asset": migration.Asset, "checksum": migration.Checksum, "version": migration.Version} {
		t.Run(column, func(t *testing.T) {
			if err := db.Table(schemaMigrationTable).Where("version = ?", migration.Version).Update(column, "damaged").Error; err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := db.Table(schemaMigrationTable).Where(column+" = ?", "damaged").Update(column, original).Error; err != nil {
					t.Errorf("恢复本次损坏登记失败: %v", err)
				}
			})
			for _, dryRun := range []bool{true, false} {
				if _, err := RunMigrations(t.Context(), store, []Migration{migration, pending}, MigrationRunOptions{DryRun: dryRun}); err == nil {
					t.Errorf("本仓登记损坏必须拒绝: column=%s dry_run=%t", column, dryRun)
				}
			}
			var ids []int64
			if err := db.Table("migration_metadata_isolation").Pluck("id", &ids).Error; err != nil || len(ids) != 1 || ids[0] != 7 {
				t.Fatalf("拒绝后不得执行待办 SQL: ids=%v err=%v", ids, err)
			}
		})
	}
	var rows []AppliedMigration
	if err := db.Table("admin_schema_migrations").Order("version").Find(&rows).Error; err != nil || len(rows) != len(foreign) || rows[0] != foreign[0] || rows[1] != foreign[1] {
		t.Fatalf("其它工具登记不得被修改: rows=%+v err=%v", rows, err)
	}
}

// TestIntegrationMySQLRejectsUnsafeDatabase 用独立测试进程验证误库在连接前拒绝。
func TestIntegrationMySQLRejectsUnsafeDatabase(t *testing.T) {
	if os.Getenv("API_MIGRATION_GUARD_CHILD") == "unsafe_database" {
		openIntegrationMySQL(t)
		return
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, database := range []string{"api", "api_test_live", ""} {
		t.Run(database, func(t *testing.T) {
			// 不可达端口使遗漏校验表现为超时，不会误连任何已有库。
			t.Setenv(integrationMySQLDSNEnv, "root:password@tcp(127.0.0.1:1)/"+database)
			t.Setenv("API_MIGRATION_GUARD_CHILD", "unsafe_database")
			ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, executable, "-test.run=^TestIntegrationMySQLRejectsUnsafeDatabase$", "-test.count=1")
			output, err := command.CombinedOutput()
			if ctx.Err() != nil || err == nil || !strings.Contains(string(output), "必须以 _test 结尾") {
				t.Fatalf("误库应在连接前被拒绝: error=%v context=%v output=%s", err, ctx.Err(), output)
			}
		})
	}
}

// TestIntegrationMySQLPreservesExistingTable 让真实迁移测试遇到哨兵，拒绝准备和清理必须都不改数据。
func TestIntegrationMySQLPreservesExistingTable(t *testing.T) {
	db := openIntegrationMySQL(t)
	tables, err := db.Migrator().GetTables()
	if err != nil || len(tables) != 0 {
		t.Fatalf("哨兵回归只允许空测试库: tables=%v error=%v", tables, err)
	}
	if err = db.Exec("CREATE TABLE `existing_business_table` (`id` bigint NOT NULL PRIMARY KEY)").Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		// 哨兵由本用例成功创建，清理不触及其它表。
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := db.WithContext(ctx).Migrator().DropTable("existing_business_table"); err != nil {
			t.Errorf("删除自建哨兵失败: %v", err)
		}
	})
	if err = db.Table("existing_business_table").Create(map[string]any{"id": 99}).Error; err != nil {
		t.Fatal(err)
	}
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestAPIMigrationRunWithMySQL$", "-test.count=1")
	output, childErr := command.CombinedOutput()
	if ctx.Err() != nil || childErr == nil || !strings.Contains(string(output), "拒绝复用已有测试表") {
		t.Errorf("已有表应使迁移夹具拒绝准备: error=%v context=%v output=%s", childErr, ctx.Err(), output)
	}
	var ids []int64
	if err = db.Table("existing_business_table").Pluck("id", &ids).Error; err != nil || len(ids) != 1 || ids[0] != 99 {
		t.Errorf("拒绝准备后哨兵数据改变: ids=%v error=%v", ids, err)
	}
	tables, err = db.Migrator().GetTables()
	if err != nil || len(tables) != 1 || tables[0] != "existing_business_table" {
		t.Errorf("拒绝准备不应创建或删除其它表: tables=%v error=%v", tables, err)
	}
}

// TestAPIMigrationRunWithMySQL 使用真实 MySQL 校验空库、非空库重跑及登记校验。
func TestAPIMigrationRunWithMySQL(t *testing.T) {
	db := openIntegrationMySQL(t)
	// 目标表必须原先不存在，后续只清理本用例在独占库创建的资产。
	tables := []string{schemaMigrationTable, "user", "user_identity_username", "user_identity_email", "user_identity_phone", "user_identity_oauth", "sys_config", "existing_business_table"}
	prepareIntegrationTables(t, db, tables...)
	store := NewGormMigrationStore(db)

	results, err := RunMigrations(context.Background(), store, DefaultMigrations(), MigrationRunOptions{AllowBootstrap: true})
	if err != nil {
		t.Fatalf("RunMigrations(up) error = %v", err)
	}
	assertMigrationStatus(t, results, MigrationStatusExecuted)

	results, err = RunMigrations(context.Background(), store, DefaultMigrations(), MigrationRunOptions{})
	if err != nil {
		t.Fatalf("RunMigrations(idempotent) error = %v", err)
	}
	assertMigrationStatus(t, results, MigrationStatusApplied)

	tampered := DefaultMigrations()
	tampered[0].Checksum = strings.Repeat("0", 64)
	if _, err = RunMigrations(context.Background(), store, tampered, MigrationRunOptions{DryRun: true}); err == nil {
		t.Fatal("期望 checksum 不一致返回错误，实际为 nil")
	}

	resetIntegrationTables(t, db, tables...)
	if err = db.Exec("CREATE TABLE `existing_business_table` (`id` bigint NOT NULL PRIMARY KEY)").Error; err != nil {
		t.Fatalf("创建已有业务表测试前置失败: %v", err)
	}
	if err = db.Table("existing_business_table").Create(map[string]any{"id": 7}).Error; err != nil {
		t.Fatalf("创建已有业务行失败: %v", err)
	}
	// 非空库预览只读；实际执行可补建缺表，不要求清理已有业务数据。
	results, err = RunMigrations(t.Context(), store, DefaultMigrations(), MigrationRunOptions{DryRun: true, AllowBootstrap: true})
	if err != nil {
		t.Fatalf("预览非空库迁移失败: %v", err)
	}
	assertMigrationStatus(t, results, MigrationStatusPending)
	if db.Migrator().HasTable("user") || db.Migrator().HasTable(schemaMigrationTable) {
		t.Fatal("预览不应创建业务表或登记表")
	}
	results, err = RunMigrations(t.Context(), store, DefaultMigrations(), MigrationRunOptions{AllowBootstrap: true})
	if err != nil {
		t.Fatalf("非空库执行迁移失败: %v", err)
	}
	assertMigrationStatus(t, results, MigrationStatusExecuted)

	// 登记缺失时允许重放幂等资产；已有业务数据不能被重建或清空。
	resetIntegrationTables(t, db, schemaMigrationTable)
	results, err = RunMigrations(t.Context(), store, DefaultMigrations(), MigrationRunOptions{AllowBootstrap: true})
	if err != nil {
		t.Fatalf("已有表缺少登记时重跑失败: %v", err)
	}
	assertMigrationStatus(t, results, MigrationStatusExecuted)
	results, err = RunMigrations(t.Context(), store, DefaultMigrations(), MigrationRunOptions{})
	if err != nil {
		t.Fatalf("非空库重复执行失败: %v", err)
	}
	assertMigrationStatus(t, results, MigrationStatusApplied)
	var ids []int64
	if err = db.Table("existing_business_table").Pluck("id", &ids).Error; err != nil || len(ids) != 1 || ids[0] != 7 {
		t.Fatalf("重复执行改变了业务数据: ids=%v err=%v", ids, err)
	}
}

// TestMigrationExecutionDeadlineInterruptsMetadataLock 用真实 MySQL 元数据锁证明迁移 context 能在 DDL 等待期间取消底层查询。
func TestMigrationExecutionDeadlineInterruptsMetadataLock(t *testing.T) {
	// 独立事务持有元数据共享锁，使 ALTER TABLE 稳定进入等待状态。
	const tableName = "migration_timeout_guard"
	db := openIntegrationMySQL(t)
	prepareIntegrationTables(t, db, tableName)
	if err := db.Exec("CREATE TABLE `migration_timeout_guard` (`id` bigint NOT NULL PRIMARY KEY) ENGINE=InnoDB").Error; err != nil {
		t.Fatalf("创建迁移超时测试表失败: %v", err)
	}
	if err := db.Exec("INSERT INTO `migration_timeout_guard` (`id`) VALUES (1)").Error; err != nil {
		t.Fatalf("写入迁移超时测试行失败: %v", err)
	}
	blocker := db.Begin()
	if blocker.Error != nil {
		t.Fatalf("开启元数据锁阻塞事务失败: %v", blocker.Error)
	}
	// 事务先回滚，再由先注册的表清理释放本次资产。
	t.Cleanup(func() { _ = blocker.Rollback().Error })
	var lockedID int64
	// FOR UPDATE 持有表的 metadata shared lock，用于可重复注入 ALTER TABLE 等待，无业务表扫描。
	if err := blocker.Raw("SELECT `id` FROM `migration_timeout_guard` WHERE `id` = ? FOR UPDATE", 1).Scan(&lockedID).Error; err != nil {
		t.Fatalf("获取元数据锁阻塞前置失败: %v", err)
	}

	// 迁移超时必须下传到底层 DDL，而不是只让上层提前返回。
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	started := time.Now()
	err := NewGormMigrationStore(db).ExecuteMigration(ctx, Migration{
		Version:  "timeout-guard",
		Name:     "metadata-lock-timeout",
		Asset:    "integration-only",
		Checksum: strings.Repeat("0", 64),
		SQL:      "ALTER TABLE `migration_timeout_guard` ADD COLUMN `note` varchar(32) NULL",
	})
	elapsed := time.Since(started)
	if err == nil || (!stderrors.Is(err, context.DeadlineExceeded) && !strings.Contains(err.Error(), context.DeadlineExceeded.Error())) {
		t.Fatalf("ExecuteMigration(metadata lock) error = %v, want context deadline", err)
	}
	if elapsed > 5*time.Second {
		t.Fatalf("ExecuteMigration(metadata lock) elapsed = %s, want bounded cancellation", elapsed)
	}
	if lockedID != 1 {
		t.Fatalf("locked row id = %d, want 1", lockedID)
	}
	// 主动释放阻塞事务，确认清理路径不依赖测试进程退出。
	if err := blocker.Rollback().Error; err != nil {
		t.Fatalf("释放元数据锁阻塞事务失败: %v", err)
	}
}

// assertMigrationStatus 校验所有迁移执行结果符合期望状态。
func assertMigrationStatus(t *testing.T, results []MigrationRunItem, status string) {
	t.Helper()
	if len(results) != len(DefaultMigrations()) {
		t.Fatalf("迁移结果数量 = %d, want %d", len(results), len(DefaultMigrations()))
	}
	for _, item := range results {
		if item.Status != status {
			t.Fatalf("迁移状态 = %s, want %s: %+v", item.Status, status, item)
		}
	}
}

// openIntegrationMySQL 只连接明确命名的独占测试库，未配置 DSN 时跳过。
func openIntegrationMySQL(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv(integrationMySQLDSNEnv))
	if dsn == "" {
		t.Skipf("%s 未配置，跳过 MySQL 集成测试", integrationMySQLDSNEnv)
	}
	// 连接前校验目标库，不能仅凭环境变量名称允许后续建表和清理。
	parsed, err := mysqldriver.ParseDSN(dsn)
	if err != nil || !strings.HasSuffix(parsed.DBName, "_test") {
		t.Fatal("迁移集成测试数据库必须以 _test 结尾")
	}
	var lastErr error
	for i := 0; i < 30; i++ {
		db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
		if err == nil {
			sqlDB, dbErr := db.DB()
			if dbErr == nil && sqlDB.Ping() == nil {
				t.Cleanup(func() { _ = sqlDB.Close() })
				return db
			}
			if dbErr != nil {
				lastErr = dbErr
			}
			if sqlDB != nil {
				_ = sqlDB.Close()
			}
		} else {
			lastErr = err
		}
		time.Sleep(time.Second)
	}
	t.Fatalf("连接集成 MySQL 失败: %v", lastErr)
	return nil
}

// prepareIntegrationTables 拒绝已有目标表，只回收本用例随后在独占库创建的表。
func prepareIntegrationTables(t *testing.T, db *gorm.DB, tables ...string) {
	t.Helper()
	var existing []string
	if err := db.WithContext(t.Context()).Table("information_schema.tables").
		Where("TABLE_SCHEMA = DATABASE() AND TABLE_NAME IN ?", tables).
		Pluck("TABLE_NAME", &existing).Error; err != nil {
		t.Fatalf("检查测试表失败: %v", err)
	}
	if len(existing) != 0 {
		t.Fatalf("拒绝复用已有测试表: %s", strings.Join(existing, ", "))
	}
	// 测试 context 已取消时仍使用独立五秒预算清理本次资产。
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		for _, table := range tables {
			if err := db.WithContext(ctx).Migrator().DropTable(table); err != nil {
				t.Errorf("删除本次测试表 %s 失败: %v", table, err)
			}
		}
	})
}

// resetIntegrationTables 只供迁移重跑场景删除已经过空表检查、由当前用例创建的明确对象。
func resetIntegrationTables(t *testing.T, db *gorm.DB, tables ...string) {
	t.Helper()
	for _, table := range tables {
		if err := db.Migrator().DropTable(table); err != nil {
			t.Fatalf("drop integration table %s: %v", table, err)
		}
	}
}
