package database

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// TestDefaultMigrationsValid 确保默认迁移清单完整、版本递增且资产存在。
func TestDefaultMigrationsValid(t *testing.T) {
	if err := validateMigrationList(DefaultMigrations()); err != nil {
		t.Fatalf("validateMigrationList(DefaultMigrations()) error = %v", err)
	}
}

// TestDefaultMigrationsContainCoreTables 确保前台核心表进入迁移清单。
func TestDefaultMigrationsContainCoreTables(t *testing.T) {
	migrations := DefaultMigrations()
	if len(migrations) == 0 {
		t.Fatal("DefaultMigrations() 不能为空")
	}
	for _, item := range migrations {
		if len(item.Checksum) != 64 {
			t.Fatalf("migration checksum length = %d, want 64: %+v", len(item.Checksum), item)
		}
	}
	expected := []struct {
		version string // version 表示期望迁移版本。
		name    string // name 表示期望迁移名称。
		asset   string // asset 表示期望 SQL 资产。
	}{
		{version: "202606220001", name: "bootstrap_user", asset: userSchemaAsset},
		{version: "202606220002", name: "bootstrap_sys_config", asset: sysConfigSchemaAsset},
		{version: "202606220003", name: "bootstrap_user_identity_username", asset: userIdentityUsernameSchemaAsset},
		{version: "202606220004", name: "bootstrap_user_identity_email", asset: userIdentityEmailSchemaAsset},
		{version: "202606220005", name: "bootstrap_user_identity_phone", asset: userIdentityPhoneSchemaAsset},
		{version: "202606220006", name: "bootstrap_user_identity_oauth", asset: userIdentityOAuthSchemaAsset},
	}
	if len(migrations) != len(expected) {
		t.Fatalf("DefaultMigrations() len = %d, want %d: %+v", len(migrations), len(expected), migrations)
	}
	for index, want := range expected {
		got := migrations[index]
		if got.Version != want.version || got.Name != want.name || got.Asset != want.asset {
			t.Fatalf("DefaultMigrations()[%d] = %+v, want version=%s name=%s asset=%s", index, got, want.version, want.name, want.asset)
		}
		if !got.BootstrapOnly || got.Destructive {
			t.Fatalf("DefaultMigrations()[%d] 必须是非破坏性空库基线: %+v", index, got)
		}
	}
}

// TestDefaultMigrationsCoverDatabaseSQLAssets 确保业务 DDL 资产都纳入默认迁移规格。
func TestDefaultMigrationsCoverDatabaseSQLAssets(t *testing.T) {
	assets, err := MigrationAssetNames()
	if err != nil {
		t.Fatalf("MigrationAssetNames() error = %v", err)
	}
	covered := make(map[string]struct{}, len(DefaultMigrations()))
	for _, item := range DefaultMigrations() {
		covered[item.Asset] = struct{}{}
	}
	for _, asset := range assets {
		if asset == schemaMigrationsAsset {
			continue
		}
		if _, ok := covered[asset]; !ok {
			t.Fatalf("database SQL asset missing migration spec: %s", asset)
		}
	}
}

// TestMigrationAssetsAvoidFragmentedRepairs 确保 SQL 资产按稳定表或业务域收口。
func TestMigrationAssetsAvoidFragmentedRepairs(t *testing.T) {
	assets, err := MigrationAssetNames()
	if err != nil {
		t.Fatalf("MigrationAssetNames() error = %v", err)
	}
	for _, asset := range assets {
		assertNotFragmentedMigrationName(t, strings.TrimSuffix(asset, ".sql.tmpl"))
	}
	for _, item := range DefaultMigrations() {
		assertNotFragmentedMigrationName(t, item.Name)
	}
}

// TestMigrationSeedInsertIDsAscending 确保显式主键 seed 按自增 id 递增排列。
func TestMigrationSeedInsertIDsAscending(t *testing.T) {
	for _, item := range DefaultMigrations() {
		assertSeedInsertIDsAscending(t, item.Asset, item.SQL)
	}
}

// assertNotFragmentedMigrationName 拦截零散同步、种子修复和临时补偿命名。
func assertNotFragmentedMigrationName(t *testing.T, name string) {
	t.Helper()
	for _, fragment := range []string{"sync_", "_seed_", "_repair_", "repair_"} {
		if strings.Contains(name, fragment) {
			t.Fatalf("SQL migration asset should stay consolidated by stable table/domain, found fragmented name: %s", name)
		}
	}
}

// assertSeedInsertIDsAscending 检查同一资产同一表内带 id 的 seed 行顺序。
func assertSeedInsertIDsAscending(t *testing.T, asset string, sql string) {
	t.Helper()
	insertRe := regexp.MustCompile("(?i)^INSERT\\s+(?:IGNORE\\s+)?INTO\\s+`([^`]+)`\\s*\\(([^)]*)\\)\\s+VALUES\\s*\\((\\d+)\\s*,")
	lastIDByTable := make(map[string]int64)
	lastLineByTable := make(map[string]int)
	for lineNo, line := range strings.Split(sql, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		matches := insertRe.FindStringSubmatch(trimmed)
		if len(matches) != 4 || !insertColumnsStartWithID(matches[2]) {
			continue
		}
		id, err := strconv.ParseInt(matches[3], 10, 64)
		if err != nil {
			t.Fatalf("%s line %d seed id parse failed: %v", asset, lineNo+1, err)
		}
		table := matches[1]
		if lastID, ok := lastIDByTable[table]; ok && id <= lastID {
			t.Fatalf("%s table %s seed id order drift at line %d: id=%d after id=%d at line %d; append by auto-increment id order", asset, table, lineNo+1, id, lastID, lastLineByTable[table])
		}
		lastIDByTable[table] = id
		lastLineByTable[table] = lineNo + 1
	}
}

// insertColumnsStartWithID 判断 INSERT 列清单是否显式以主键 id 开头。
func insertColumnsStartWithID(columns string) bool {
	columns = strings.TrimSpace(columns)
	return strings.HasPrefix(columns, "`id`,") || columns == "`id`"
}

// TestPendingMigrations 确保已登记版本不会再次进入待执行列表。
func TestPendingMigrations(t *testing.T) {
	migrations := DefaultMigrations()
	pending := PendingMigrations(map[string]struct{}{migrations[0].Version: {}})
	if len(pending) != len(migrations)-1 {
		t.Fatalf("PendingMigrations() len = %d, want %d", len(pending), len(migrations)-1)
	}
	if pending[0].Version == migrations[0].Version {
		t.Fatalf("applied migration still pending: %+v", pending[0])
	}
}
