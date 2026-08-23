package database

import (
	"context"
	"strings"

	"github.com/Is999/go-utils/errors"
)

// 迁移状态常量用于命令行输出和测试断言。
const (
	// MigrationStatusApplied 表示迁移版本已登记且 checksum 匹配。
	MigrationStatusApplied = "applied"
	// MigrationStatusPending 表示迁移待执行。
	MigrationStatusPending = "pending"
	// MigrationStatusExecuted 表示本轮已执行并登记。
	MigrationStatusExecuted = "executed"
	// MigrationStatusBlocked 表示迁移被安全策略拦截。
	MigrationStatusBlocked = "blocked"
)

// AppliedMigration 表示 API 独立登记表中的初始化版本，不包含其它工具的资产。
type AppliedMigration struct {
	Version  string // 迁移版本号
	Name     string // 迁移名称
	Asset    string // 迁移资产文件名
	Checksum string // 已登记 SQL checksum
}

// MigrationRunOptions 控制迁移执行策略。
type MigrationRunOptions struct {
	DryRun           bool // 是否只输出计划，不执行 SQL
	AllowBootstrap   bool // 是否允许执行 bootstrap-only 基线迁移
	AllowDestructive bool // 是否允许执行 destructive 迁移
}

// MigrationRunItem 表示单个迁移在本轮计划中的状态。
type MigrationRunItem struct {
	Version  string // 迁移版本号
	Name     string // 迁移名称
	Asset    string // 迁移资产
	Checksum string // 当前 SQL checksum
	Status   string // 迁移状态：applied/pending/executed/blocked
	Reason   string // 状态原因，blocked 时必填
}

// MigrationStore 抽象迁移执行所需的数据库操作，便于命令行和测试复用。
type MigrationStore interface {
	// EnsureSchema 在实际执行迁移前确保版本表存在。
	EnsureSchema(context.Context, string) error
	// AppliedMigrations 读取已登记版本，版本表不存在时返回空集合。
	AppliedMigrations(context.Context) (map[string]AppliedMigration, error)
	// ExecuteMigration 顺序执行单个初始化资产并在成功后登记版本。
	ExecuteMigration(context.Context, Migration) error
}

// RunMigrations 按登记跳过已完成资产，允许在非空库补执行和重试未登记的幂等 SQL。
func RunMigrations(ctx context.Context, store MigrationStore, migrations []Migration, options MigrationRunOptions) ([]MigrationRunItem, error) {
	if store == nil {
		return nil, errors.Errorf("数据库迁移 store 不能为空")
	}
	if err := validateMigrationList(migrations); err != nil {
		return nil, errors.Tag(err)
	}
	applied, err := store.AppliedMigrations(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "读取数据库迁移版本表失败")
	}
	// 已登记元数据必须与当前完整基线一致。
	if err = validateAppliedMigrations(migrations, applied); err != nil {
		return nil, errors.Tag(err)
	}
	hasPending := false
	for _, migration := range migrations {
		if _, ok := applied[migration.Version]; !ok {
			hasPending = true
			break
		}
	}
	if !options.DryRun && hasPending {
		// 仅实际执行待办资产时创建登记表，预览和完整重跑不写数据库。
		if err = store.EnsureSchema(ctx, SchemaMigrationsSQL()); err != nil {
			return nil, errors.Wrap(err, "初始化数据库迁移版本表失败")
		}
	}

	results := make([]MigrationRunItem, 0, len(migrations))
	// 待执行资产严格按清单顺序处理。
	for _, migration := range migrations {
		item := newMigrationRunItem(migration)
		if _, ok := applied[migration.Version]; ok {
			item.Status = MigrationStatusApplied
			results = append(results, item)
			continue
		}

		// 预览列出所有被拦截项；实际执行遇到首个未授权资产即停止。
		if reason := blockMigrationReason(migration, options); reason != "" {
			item.Status = MigrationStatusBlocked
			item.Reason = reason
			results = append(results, item)
			if !options.DryRun {
				return results, errors.Errorf("数据库迁移被安全策略拦截 version=%s name=%s reason=%s", migration.Version, migration.Name, reason)
			}
			continue
		}
		if options.DryRun {
			item.Status = MigrationStatusPending
			results = append(results, item)
			continue
		}
		if err := store.ExecuteMigration(ctx, migration); err != nil {
			return results, errors.Tag(err)
		}
		item.Status = MigrationStatusExecuted
		results = append(results, item)
	}
	return results, nil
}

// validateAppliedMigrations 确保数据库登记只包含当前完整初始化资产且摘要未漂移。
func validateAppliedMigrations(migrations []Migration, applied map[string]AppliedMigration) error {
	current := make(map[string]Migration, len(migrations))
	for _, migration := range migrations {
		current[migration.Version] = migration
	}
	for version, appliedItem := range applied {
		migration, ok := current[version]
		if !ok {
			return errors.Errorf("数据库存在当前初始化清单之外的登记 version=%s", version)
		}
		if appliedItem.Name != migration.Name || appliedItem.Asset != migration.Asset {
			return errors.Errorf(
				"数据库迁移登记与当前初始化资产不一致 version=%s applied_name=%s current_name=%s applied_asset=%s current_asset=%s",
				migration.Version,
				appliedItem.Name,
				migration.Name,
				appliedItem.Asset,
				migration.Asset,
			)
		}
		if appliedItem.Checksum != migration.Checksum {
			return errors.Errorf("数据库迁移 checksum 不一致 version=%s name=%s applied=%s current=%s", migration.Version, migration.Name, appliedItem.Checksum, migration.Checksum)
		}
	}
	return nil
}

// newMigrationRunItem 从迁移定义生成本轮执行结果的基础信息。
func newMigrationRunItem(migration Migration) MigrationRunItem {
	return MigrationRunItem{
		Version:  migration.Version,
		Name:     migration.Name,
		Asset:    migration.Asset,
		Checksum: migration.Checksum,
	}
}

// blockMigrationReason 返回迁移被发布安全开关拦截的原因。
func blockMigrationReason(migration Migration, options MigrationRunOptions) string {
	if migration.BootstrapOnly && !options.AllowBootstrap {
		return "bootstrap-only 迁移需要显式允许"
	}
	if migration.Destructive && !options.AllowDestructive {
		return "destructive 迁移需要显式允许"
	}
	return ""
}

// validateMigrationList 校验迁移清单顺序、唯一性和破坏性标记。
func validateMigrationList(migrations []Migration) error {
	if len(migrations) == 0 {
		return errors.Errorf("数据库迁移清单不能为空")
	}
	seenVersions := make(map[string]struct{}, len(migrations))
	seenNames := make(map[string]struct{}, len(migrations))
	previousVersion := ""
	for _, item := range migrations {
		if strings.TrimSpace(item.Version) == "" || strings.TrimSpace(item.Name) == "" || strings.TrimSpace(item.Asset) == "" || strings.TrimSpace(item.SQL) == "" || strings.TrimSpace(item.Checksum) == "" {
			return errors.Errorf("数据库迁移清单存在空字段: %+v", item)
		}
		if _, ok := seenVersions[item.Version]; ok {
			return errors.Errorf("数据库迁移版本重复: %s", item.Version)
		}
		if _, ok := seenNames[item.Name]; ok {
			return errors.Errorf("数据库迁移名称重复: %s", item.Name)
		}
		if previousVersion != "" && item.Version <= previousVersion {
			return errors.Errorf("数据库迁移版本必须递增: %s <= %s", item.Version, previousVersion)
		}
		if containsDestructiveSQL(item.SQL) && !item.Destructive {
			return errors.Errorf("检测到破坏性 SQL 但迁移未标记 destructive: %s", item.Name)
		}
		seenVersions[item.Version] = struct{}{}
		seenNames[item.Name] = struct{}{}
		previousVersion = item.Version
	}
	return nil
}

// containsDestructiveSQL 检测迁移 SQL 是否包含需显式放行的破坏性语句。
func containsDestructiveSQL(sqlText string) bool {
	for _, statement := range splitMigrationStatements(sqlText) {
		normalized := normalizeMigrationStatement(statement)
		if normalized == "" {
			continue
		}
		for _, marker := range destructiveMigrationSQLMarkers {
			if strings.Contains(normalized, marker) {
				return true
			}
		}
		if strings.HasPrefix(normalized, "ALTER TABLE ") && strings.Contains(normalized, " DROP ") {
			return true
		}
	}
	return false
}

// normalizeMigrationStatement 归一化 SQL 语句，供破坏性关键字检测使用。
func normalizeMigrationStatement(statement string) string {
	statement = trimLeadingSQLComments(statement)
	return strings.ToUpper(strings.Join(strings.Fields(statement), " "))
}

// destructiveMigrationSQLMarkers 定义必须标记 destructive 的高风险 SQL 片段。
var destructiveMigrationSQLMarkers = []string{
	"DROP TABLE",
	"DROP DATABASE",
	"TRUNCATE TABLE",
	"DELETE FROM",
}
