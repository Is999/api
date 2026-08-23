package database

import (
	"embed"
	"io/fs"
	"sort"
	"strings"

	"api/common/embedasset"

	"github.com/Is999/go-utils/errors"
)

// 迁移 SQL 资产文件名。
const (
	userSchemaAsset                 = "user_schema.sql.tmpl"                   // 业务用户表 DDL 资产
	userIdentityUsernameSchemaAsset = "user_identity_username_schema.sql.tmpl" // 业务用户账号身份索引表 DDL 资产
	userIdentityEmailSchemaAsset    = "user_identity_email_schema.sql.tmpl"    // 业务用户邮箱身份索引表 DDL 资产
	userIdentityPhoneSchemaAsset    = "user_identity_phone_schema.sql.tmpl"    // 业务用户手机号身份索引表 DDL 资产
	userIdentityOAuthSchemaAsset    = "user_identity_oauth_schema.sql.tmpl"    // 业务用户三方身份索引表 DDL 资产
	sysConfigSchemaAsset            = "sys_config_schema.sql.tmpl"             // 系统配置表 DDL 资产
	schemaMigrationsAsset           = "schema_migrations.sql.tmpl"             // 迁移版本表 DDL 资产
	databaseMigrationAssetDir       = "assets"                                 // go:embed 中的迁移资产目录
)

// databaseMigrationAssets 嵌入数据库迁移 SQL 资产。
//
//go:embed assets/*.sql.tmpl
var databaseMigrationAssets embed.FS

// SchemaMigrationsSQL 返回剥离文件头说明后的迁移版本表 DDL。
func SchemaMigrationsSQL() string {
	return readMigrationSQL(schemaMigrationsAsset)
}

// MigrationAssetNames 返回仓库内 database/assets 目录的一层 SQL 模板资产名。
func MigrationAssetNames() ([]string, error) {
	matches, err := fs.Glob(databaseMigrationAssets, migrationAssetPath("*.sql.tmpl"))
	if err != nil {
		return nil, errors.Tag(err)
	}
	for index, item := range matches {
		matches[index] = strings.TrimPrefix(item, databaseMigrationAssetDir+"/")
	}
	sort.Strings(matches)
	return matches, nil
}

// readMigrationSQL 读取指定迁移 SQL 资产并剥离文件头说明。
func readMigrationSQL(asset string) string {
	data, err := databaseMigrationAssets.ReadFile(migrationAssetPath(asset))
	if err != nil {
		// 空 SQL 由清单校验统一拒绝，读取资产不在启动路径触发 panic。
		return ""
	}
	return embedasset.StripLeadingLineComments(string(data), "--")
}

// migrationAssetPath 返回 go:embed 文件系统内的资产路径，迁移记录仍保留短文件名。
func migrationAssetPath(asset string) string {
	if strings.HasPrefix(asset, databaseMigrationAssetDir+"/") {
		return asset
	}
	return databaseMigrationAssetDir + "/" + asset
}
