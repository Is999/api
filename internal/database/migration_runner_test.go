package database

import (
	"context"
	"strings"
	"testing"
)

// TestRunMigrationsExecutesPending 确保待执行迁移会按顺序执行并登记。
func TestRunMigrationsExecutesPending(t *testing.T) {
	store := newFakeMigrationStore(nil)
	migrations := []Migration{testMigration("202606050001", "create_demo")}

	results, err := RunMigrations(context.Background(), store, migrations, MigrationRunOptions{})
	if err != nil {
		t.Fatalf("RunMigrations() error = %v", err)
	}
	if !store.schemaEnsured {
		t.Fatal("期望执行前初始化 API 登记表")
	}
	if len(store.executed) != 1 || store.executed[0].Version != migrations[0].Version {
		t.Fatalf("执行迁移不符合预期: %+v", store.executed)
	}
	if len(results) != 1 || results[0].Status != MigrationStatusExecuted {
		t.Fatalf("迁移结果不符合预期: %+v", results)
	}
}

// TestRunMigrationsRejectsBlockedMigration 确保空库初始化资产默认不会执行。
func TestRunMigrationsRejectsBlockedMigration(t *testing.T) {
	store := newFakeMigrationStore(nil)
	migrations := []Migration{testMigration("202606050001", "bootstrap_demo")}
	migrations[0].BootstrapOnly = true

	results, err := RunMigrations(context.Background(), store, migrations, MigrationRunOptions{})
	if err == nil {
		t.Fatal("期望空库初始化资产返回错误，实际为 nil")
	}
	if len(store.executed) != 0 {
		t.Fatalf("空库初始化资产不应被执行: %+v", store.executed)
	}
	if len(results) != 1 || results[0].Status != MigrationStatusBlocked {
		t.Fatalf("迁移结果应为 blocked: %+v", results)
	}
}

// TestRunMigrationsDryRunReportsBlockedMigration 确保 dry-run 只报告拦截原因，不执行 SQL。
func TestRunMigrationsDryRunReportsBlockedMigration(t *testing.T) {
	store := newFakeMigrationStore(nil)
	migrations := []Migration{testMigration("202606050001", "bootstrap_demo")}
	migrations[0].BootstrapOnly = true

	results, err := RunMigrations(context.Background(), store, migrations, MigrationRunOptions{DryRun: true})
	if err != nil {
		t.Fatalf("RunMigrations(dry-run) error = %v", err)
	}
	if store.schemaEnsured {
		t.Fatal("dry-run 不应创建 API 登记表")
	}
	if len(results) != 1 || results[0].Status != MigrationStatusBlocked {
		t.Fatalf("dry-run 结果应为 blocked: %+v", results)
	}
}

// TestDefaultAPIBaselineMigrationsBlockedByDefault 确保全部 API 初始化资产都要求显式 bootstrap 授权。
func TestDefaultAPIBaselineMigrationsBlockedByDefault(t *testing.T) {
	migrations := DefaultMigrations()
	results, err := RunMigrations(t.Context(), newFakeMigrationStore(nil), migrations, MigrationRunOptions{DryRun: true})
	if err != nil {
		t.Fatalf("RunMigrations(dry-run default) error = %v", err)
	}
	if len(results) != len(migrations) {
		t.Fatalf("默认迁移结果数量 = %d, want %d", len(results), len(migrations))
	}
	for index, item := range results {
		migration := migrations[index]
		if !migration.BootstrapOnly || migration.Destructive || item.Status != MigrationStatusBlocked {
			t.Fatalf("API 初始化资产默认应被拦截且保持非破坏性: migration=%+v result=%+v", migration, item)
		}
	}
}

// TestRunMigrationsDetectsChecksumMismatch 确保历史版本 SQL 被改动时会被拒绝。
func TestRunMigrationsDetectsChecksumMismatch(t *testing.T) {
	migration := testMigration("202606050001", "create_demo")
	store := newFakeMigrationStore(map[string]AppliedMigration{
		migration.Version: {Version: migration.Version, Name: migration.Name, Asset: migration.Asset, Checksum: "changed"},
	})

	if _, err := RunMigrations(context.Background(), store, []Migration{migration}, MigrationRunOptions{DryRun: true}); err == nil {
		t.Fatal("期望 checksum 不一致返回错误，实际为 nil")
	}
}

// TestRunMigrationsRejectsRegistrationMetadataDrift 确保登记名称和资产路径不能与当前初始化清单漂移。
func TestRunMigrationsRejectsRegistrationMetadataDrift(t *testing.T) {
	migration := testMigration("202606050001", "create_demo")
	for _, applied := range []AppliedMigration{
		{Version: migration.Version, Name: "renamed", Asset: migration.Asset, Checksum: migration.Checksum},
		{Version: migration.Version, Name: migration.Name, Asset: "other.sql.tmpl", Checksum: migration.Checksum},
	} {
		store := newFakeMigrationStore(map[string]AppliedMigration{migration.Version: applied})
		if _, err := RunMigrations(t.Context(), store, []Migration{migration}, MigrationRunOptions{DryRun: true}); err == nil {
			t.Fatalf("期望异常登记被拒绝: %+v", applied)
		}
	}
}

// TestRunMigrationsAcceptsFullyAppliedProvidedList 确保执行器只按调用方传入的清单判断待执行项，不混入全局默认资产。
func TestRunMigrationsAcceptsFullyAppliedProvidedList(t *testing.T) {
	migration := testMigration("202606050001", "create_demo")
	store := newFakeMigrationStore(map[string]AppliedMigration{
		migration.Version: {Version: migration.Version, Name: migration.Name, Asset: migration.Asset, Checksum: migration.Checksum},
	})

	results, err := RunMigrations(t.Context(), store, []Migration{migration}, MigrationRunOptions{})
	if err != nil {
		t.Fatalf("完整登记的传入清单不应被默认清单干扰: %v", err)
	}
	if store.schemaEnsured || len(store.executed) != 0 {
		t.Fatalf("幂等检查不应产生数据库副作用: ensured=%v executed=%+v", store.schemaEnsured, store.executed)
	}
	if len(results) != 1 || results[0].Status != MigrationStatusApplied {
		t.Fatalf("已登记迁移状态不符合预期: %+v", results)
	}
}

// TestRunMigrationsResumesPartialInitialization 预览不写库，续跑只执行未登记资产。
func TestRunMigrationsResumesPartialInitialization(t *testing.T) {
	first := testMigration("202606050001", "create_first")
	second := testMigration("202606050002", "create_second")
	store := newFakeMigrationStore(map[string]AppliedMigration{
		first.Version: {Version: first.Version, Name: first.Name, Asset: first.Asset, Checksum: first.Checksum},
	})

	results, err := RunMigrations(t.Context(), store, []Migration{first, second}, MigrationRunOptions{DryRun: true})
	if err != nil {
		t.Fatalf("预览部分初始化失败: %v", err)
	}
	if store.schemaEnsured || len(store.executed) != 0 {
		t.Fatal("预览不能创建登记表或执行资产")
	}
	if len(results) != 2 || results[0].Status != MigrationStatusApplied || results[1].Status != MigrationStatusPending {
		t.Fatalf("部分初始化状态不符合预期: %+v", results)
	}

	results, err = RunMigrations(t.Context(), store, []Migration{first, second}, MigrationRunOptions{})
	if err != nil {
		t.Fatalf("续跑部分初始化失败: %v", err)
	}
	if len(results) != 2 || results[0].Status != MigrationStatusApplied || results[1].Status != MigrationStatusExecuted {
		t.Fatalf("续跑状态不符合预期: %+v", results)
	}
	if len(store.executed) != 1 || store.executed[0].Version != second.Version {
		t.Fatalf("续跑只应执行未登记资产: %+v", store.executed)
	}

	results, err = RunMigrations(t.Context(), store, []Migration{first, second}, MigrationRunOptions{})
	if err != nil || len(store.executed) != 1 || len(results) != 2 || results[0].Status != MigrationStatusApplied || results[1].Status != MigrationStatusApplied {
		t.Fatalf("重复执行不应重写已登记资产: results=%+v executed=%+v err=%v", results, store.executed, err)
	}
}

// TestRunMigrationsRejectsNonCanonicalStoredChecksum 确保登记摘要必须与当前生成的小写 SHA256 逐字节一致。
func TestRunMigrationsRejectsNonCanonicalStoredChecksum(t *testing.T) {
	migration := testMigration("202606050001", "create_demo")
	store := newFakeMigrationStore(map[string]AppliedMigration{
		migration.Version: {Version: migration.Version, Name: migration.Name, Asset: migration.Asset, Checksum: strings.ToUpper(migration.Checksum)},
	})
	if _, err := RunMigrations(t.Context(), store, []Migration{migration}, MigrationRunOptions{DryRun: true}); err == nil {
		t.Fatal("大写 checksum 不应作为同一登记值接受")
	}
}

// TestRunMigrationsRejectsUnmarkedDestructiveSQL 确保破坏性 SQL 必须显式标记。
func TestRunMigrationsRejectsUnmarkedDestructiveSQL(t *testing.T) {
	migration := testMigration("202606050001", "drop_demo")
	migration.SQL = "DROP TABLE demo"
	migration.Checksum = sha256Hex(migration.SQL)

	if _, err := RunMigrations(context.Background(), newFakeMigrationStore(nil), []Migration{migration}, MigrationRunOptions{DryRun: true}); err == nil {
		t.Fatal("期望未标记 destructive 的 DROP SQL 返回错误，实际为 nil")
	}
}

// testMigration 构造摘要与 SQL 匹配的最小迁移资产。
func testMigration(version string, name string) Migration {
	return Migration{
		Version:  version,
		Name:     name,
		Asset:    name + ".sql.tmpl",
		SQL:      "CREATE TABLE demo (id int)",
		Checksum: sha256Hex("CREATE TABLE demo (id int)"),
	}
}

// fakeMigrationStore 只记录执行器的调用和内存登记，不执行 SQL，也不模拟 MySQL DDL 事务。
type fakeMigrationStore struct {
	applied       map[string]AppliedMigration // 预置登记快照并承接执行后的版本写入
	schemaEnsured bool                        // 断言执行资产前已经创建版本表
	executed      []Migration                 // 断言资产执行次数和顺序
}

// newFakeMigrationStore 使用给定登记快照构造内存存储。
func newFakeMigrationStore(applied map[string]AppliedMigration) *fakeMigrationStore {
	if applied == nil {
		applied = map[string]AppliedMigration{}
	}
	return &fakeMigrationStore{applied: applied}
}

// EnsureSchema 记录版本表创建请求。
func (s *fakeMigrationStore) EnsureSchema(context.Context, string) error {
	s.schemaEnsured = true
	return nil
}

// AppliedMigrations 返回当前用例预置的版本登记。
func (s *fakeMigrationStore) AppliedMigrations(context.Context) (map[string]AppliedMigration, error) {
	return s.applied, nil
}

// ExecuteMigration 模拟单个迁移成功执行。
func (s *fakeMigrationStore) ExecuteMigration(_ context.Context, migration Migration) error {
	s.executed = append(s.executed, migration)
	s.applied[migration.Version] = AppliedMigration{
		Version:  migration.Version,
		Name:     migration.Name,
		Asset:    migration.Asset,
		Checksum: migration.Checksum,
	}
	return nil
}
