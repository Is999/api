package database

import (
	"strings"
	"testing"
)

// TestUserSchemaAsset 验证业务用户表 DDL 会剥离说明头。
func TestUserSchemaAsset(t *testing.T) {
	// 读取器应剥离资产说明，但保留完整建表语句。
	sql := readMigrationSQL(userSchemaAsset)

	if strings.Contains(sql, "代码资产") {
		t.Fatalf("readMigrationSQL(userSchemaAsset) should strip header comments: %q", sql)
	}
	if !strings.Contains(sql, "CREATE TABLE IF NOT EXISTS `user`") {
		t.Fatalf("readMigrationSQL(userSchemaAsset) missing user DDL: %q", sql)
	}
	// 主表必须包含分片、安全联系方式和认证版本约束。
	for _, want := range []string{
		"`id` bigint NOT NULL COMMENT '雪花ID'",
		"`shard_no` int NOT NULL DEFAULT 0 COMMENT 'ID哈希分片，CRC32(id字符串)%1024，用于分表和分片游标查询'",
		"`email_ciphertext` varchar(512) NOT NULL DEFAULT '' COMMENT '邮箱AES-GCM密文'",
		"`email_hash` char(64) NOT NULL DEFAULT '' COMMENT '邮箱HMAC查询哈希'",
		"`phone_ciphertext` varchar(512) NOT NULL DEFAULT '' COMMENT '手机号AES-GCM密文'",
		"`phone_hash` char(64) NOT NULL DEFAULT '' COMMENT '手机号HMAC查询哈希'",
		"`auth_version` bigint unsigned NOT NULL DEFAULT 1 COMMENT '认证版本，敏感变更时单调递增'",
		"KEY `idx_user_shard_no_id` (`shard_no`, `id`)",
		"KEY `idx_user_email_hash` (`email_hash`)",
		"KEY `idx_user_phone_hash` (`phone_hash`)",
		"KEY `idx_user_status_id` (`status`, `id`)",
		"CONSTRAINT `chk_user_shard_no` CHECK (`shard_no` BETWEEN 0 AND 1023)",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("readMigrationSQL(userSchemaAsset) missing %q: %q", want, sql)
		}
	}
	// 禁止恢复旧的取模分片规则和联系方式明文列。
	for _, forbidden := range []string{"MOD(`id`, 1024)", "`id` % 1024", "id%1024"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("readMigrationSQL(userSchemaAsset) should not use id modulo rule %q: %q", forbidden, sql)
		}
	}
	for _, forbidden := range []string{"`email` varchar", "`phone` varchar", "idx_user_email`", "idx_user_phone`"} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("readMigrationSQL(userSchemaAsset) should not keep plaintext contact column/index %q: %q", forbidden, sql)
		}
	}
}

// TestUserIdentitySchemaAssets 验证四张登录身份索引表 DDL 各自独立。
func TestUserIdentitySchemaAssets(t *testing.T) {
	tests := []struct {
		asset     string   // asset 表示 SQL 资产文件。
		tableName string   // tableName 表示身份索引物理表名。
		want      []string // want 表示该身份表必须包含的字段和索引。
		forbid    []string // forbid 表示该身份表不得保留的旧字段和索引。
	}{
		{
			asset:     userIdentityUsernameSchemaAsset,
			tableName: "user_identity_username",
			want: []string{
				"`identity_value` varchar(32) NOT NULL COMMENT '归一化用户名'",
				"UNIQUE KEY `uk_user_identity_value` (`identity_value`)",
				"UNIQUE KEY `uk_user_identity_user` (`user_id`)",
			},
			forbid: []string{"`provider`", "`identity_hash`", "uk_user_identity_provider_value", "uk_user_identity_user_provider"},
		},
		{
			asset:     userIdentityEmailSchemaAsset,
			tableName: "user_identity_email",
			want: []string{
				"`identity_hash` char(64) NOT NULL COMMENT '归一化邮箱HMAC查询哈希'",
				"UNIQUE KEY `uk_user_identity_hash` (`identity_hash`)",
				"UNIQUE KEY `uk_user_identity_user` (`user_id`)",
			},
			forbid: []string{"`provider`", "`identity_value`", "uk_user_identity_provider_value", "uk_user_identity_user_provider"},
		},
		{
			asset:     userIdentityPhoneSchemaAsset,
			tableName: "user_identity_phone",
			want: []string{
				"`identity_hash` char(64) NOT NULL COMMENT '归一化手机号HMAC查询哈希'",
				"UNIQUE KEY `uk_user_identity_hash` (`identity_hash`)",
				"UNIQUE KEY `uk_user_identity_user` (`user_id`)",
			},
			forbid: []string{"`provider`", "`identity_value`", "uk_user_identity_provider_value", "uk_user_identity_user_provider"},
		},
		{
			asset:     userIdentityOAuthSchemaAsset,
			tableName: "user_identity_oauth",
			want: []string{
				"`provider` varchar(32) COLLATE utf8mb4_bin NOT NULL DEFAULT '' COMMENT '三方身份提供方'",
				"`identity_value` varchar(191) COLLATE utf8mb4_bin NOT NULL COMMENT '三方平台用户主体'",
				"UNIQUE KEY `uk_user_identity_provider_value` (`provider`, `identity_value`)",
				"UNIQUE KEY `uk_user_identity_user_provider` (`user_id`, `provider`)",
				"CONSTRAINT `chk_user_identity_oauth_provider` CHECK (`provider` <> '')",
			},
			forbid: []string{"`identity_hash`"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.tableName, func(t *testing.T) {
			// 每个资产只能创建自己的身份表，并共享基础分片索引。
			sql := readMigrationSQL(tt.asset)
			if strings.Contains(sql, "代码资产") {
				t.Fatalf("readMigrationSQL(%s) should strip header comments: %q", tt.asset, sql)
			}
			for _, want := range []string{
				"CREATE TABLE IF NOT EXISTS `" + tt.tableName + "`",
				"`user_shard_no` int NOT NULL COMMENT '业务用户ID哈希分片，CRC32(id字符串)%1024'",
				"KEY `idx_user_identity_shard_user` (`user_shard_no`, `user_id`)",
			} {
				if !strings.Contains(sql, want) {
					t.Fatalf("readMigrationSQL(%s) missing %q: %q", tt.asset, want, sql)
				}
			}
			// 表类型专属字段和索引必须完整存在。
			for _, want := range tt.want {
				if !strings.Contains(sql, want) {
					t.Fatalf("readMigrationSQL(%s) missing %q: %q", tt.asset, want, sql)
				}
			}
			// 冗余类型列、旧路由列和其它身份表结构不得混入当前资产。
			for _, forbidden := range append([]string{"`identity_type`", "idx_user_identity_type_user", "user_route_shard_count", "idx_user_identity_user_route"}, tt.forbid...) {
				if strings.Contains(sql, forbidden) {
					t.Fatalf("readMigrationSQL(%s) should not keep redundant identity type %q: %q", tt.asset, forbidden, sql)
				}
			}
			for _, otherTable := range []string{"user_identity_username", "user_identity_email", "user_identity_phone", "user_identity_oauth"} {
				if otherTable != tt.tableName && strings.Contains(sql, "CREATE TABLE IF NOT EXISTS `"+otherTable+"`") {
					t.Fatalf("readMigrationSQL(%s) should only create %s, got: %q", tt.asset, tt.tableName, sql)
				}
			}
		})
	}
}

// TestSysConfigSchemaAsset 验证系统配置表 DDL 使用 sys_config 表名并剥离说明头。
func TestSysConfigSchemaAsset(t *testing.T) {
	sql := readMigrationSQL(sysConfigSchemaAsset)

	if strings.Contains(sql, "代码资产") {
		t.Fatalf("readMigrationSQL(sysConfigSchemaAsset) should strip header comments: %q", sql)
	}
	if !strings.Contains(sql, "CREATE TABLE IF NOT EXISTS `sys_config`") {
		t.Fatalf("readMigrationSQL(sysConfigSchemaAsset) missing sys_config DDL: %q", sql)
	}
	if strings.Contains(sql, "`api_sys_config`") {
		t.Fatalf("readMigrationSQL(sysConfigSchemaAsset) should not use api_ table prefix: %q", sql)
	}
}

// TestSchemaMigrationsSQL 验证迁移版本表 DDL 会剥离说明头。
func TestSchemaMigrationsSQL(t *testing.T) {
	sql := SchemaMigrationsSQL()

	if strings.Contains(sql, "代码资产") {
		t.Fatalf("SchemaMigrationsSQL() should strip header comments: %q", sql)
	}
	if schemaMigrationTable != "api_schema_migrations" || !strings.Contains(sql, "CREATE TABLE IF NOT EXISTS `"+schemaMigrationTable+"`") {
		t.Fatalf("SchemaMigrationsSQL() missing API-owned DDL: %q", sql)
	}
	for _, want := range []string{"PRIMARY KEY (`version`)", "UNIQUE KEY `uk_api_schema_migrations_name` (`name`)", "`applied_at` datetime NOT NULL DEFAULT CURRENT_TIMESTAMP"} {
		if !strings.Contains(sql, want) {
			t.Fatalf("API 登记主键、唯一约束或默认时间缺失: %s", want)
		}
	}
}
