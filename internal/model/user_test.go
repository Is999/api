package model

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"api/common/idgen"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"gorm.io/plugin/dbresolver"
)

// testUserPrivacySecret 是模型隐私字段测试使用的固定密钥。
const testUserPrivacySecret = "test-user-privacy-secret"

// TestUserQueriesPreserveDBResolverMode 验证模型会话复制不会把显式主库/副本路由清空。
func TestUserQueriesPreserveDBResolverMode(t *testing.T) {
	// 两个独立 SQLite 文件模拟主副本，不覆盖 MySQL 复制延迟或事务隔离。
	const routeShardCount = 2
	userID, _ := userIDInShardRangeForTest(t, 512, 1023)
	sourcePath := filepath.Join(t.TempDir(), "source.db")
	replicaPath := filepath.Join(t.TempDir(), "replica.db")
	prepareUserResolverFixture(t, sourcePath, userID, "source-user", UserStatusDisabled, routeShardCount)
	prepareUserResolverFixture(t, replicaPath, userID, "replica-user", UserStatusEnabled, routeShardCount)

	db, err := gorm.Open(sqlite.Open(sourcePath), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("gorm.Open(source) error = %v", err)
	}
	if err = db.Use(dbresolver.Register(dbresolver.Config{
		Replicas: []gorm.Dialector{sqlite.Open(replicaPath)},
	})); err != nil {
		t.Fatalf("register dbresolver error = %v", err)
	}
	queryCount := 0
	if err = db.Callback().Query().Before("gorm:query").Register("test:count_user_resolver_query", func(*gorm.DB) {
		queryCount++
	}); err != nil {
		t.Fatalf("register query counter error = %v", err)
	}

	// Write 和 Read 各执行一次联查，并分别命中 source 与 replica。
	writeUser, err := FindUserByID(db.Clauses(dbresolver.Write), userID, routeShardCount)
	if err != nil {
		t.Fatalf("FindUserByID(write) error = %v", err)
	}
	if writeUser == nil || writeUser.Username != "source-user" || writeUser.Status != UserStatusDisabled {
		t.Fatalf("write query = %+v, want source user", writeUser)
	}
	if queryCount != 1 {
		t.Fatalf("write query count = %d, want 1", queryCount)
	}

	readUser, err := FindUserByID(db.Clauses(dbresolver.Read), userID, routeShardCount)
	if err != nil {
		t.Fatalf("FindUserByID(read) error = %v", err)
	}
	if readUser == nil || readUser.Username != "replica-user" || readUser.Status != UserStatusEnabled {
		t.Fatalf("read query = %+v, want replica user", readUser)
	}
	if queryCount != 2 {
		t.Fatalf("read query count = %d, want total 2", queryCount)
	}
}

// prepareUserResolverFixture 在独立 SQLite 文件中写入可区分的用户与账号索引。
func prepareUserResolverFixture(t *testing.T, path string, userID int64, username string, status int, routeShardCount int) {
	t.Helper()
	// 每个 SQLite 文件独立创建物理用户表和身份目录。
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("gorm.Open(%s) error = %v", path, err)
	}
	shardNo := idgen.ShardNo(userID)
	tableName, err := UserPhysicalTableName(shardNo, routeShardCount)
	if err != nil {
		t.Fatalf("UserPhysicalTableName() error = %v", err)
	}
	if err = db.Table(tableName).AutoMigrate(&userSQLiteForTest{}); err != nil {
		t.Fatalf("AutoMigrate(user) error = %v", err)
	}
	if err = migrateUserIdentityTablesForTest(db); err != nil {
		t.Fatalf("migrateUserIdentityTablesForTest() error = %v", err)
	}
	// 用户资料与身份索引必须指向同一逻辑分片。
	now := time.Now()
	if err = db.Table(tableName).Create(&userSQLiteForTest{
		ID:           userID,
		ShardNo:      shardNo,
		Username:     username,
		PasswordHash: "hash",
		Status:       status,
		CreatedAt:    now,
		UpdatedAt:    now,
	}).Error; err != nil {
		t.Fatalf("create user fixture error = %v", err)
	}
	// fixture 模型含 default:1，显式更新才能为路由测试预置禁用用户。
	if err = db.Table(tableName).Where("id = ?", userID).Update("status", status).Error; err != nil {
		t.Fatalf("update user fixture status error = %v", err)
	}
	if err = db.Table(TableNameUserIdentityUsername).Create(&userIdentitySQLiteForTest{
		IdentityValue: "resolver-user",
		UserID:        userID,
		UserShardNo:   shardNo,
		CreatedAt:     now,
		UpdatedAt:     now,
	}).Error; err != nil {
		t.Fatalf("create identity fixture error = %v", err)
	}
}

// TestFindUserByIDUsesOneQueryAndPreservesFailures 验证单次联查不混淆身份、路由和主表损坏状态。
func TestFindUserByIDUsesOneQueryAndPreservesFailures(t *testing.T) {
	// 同一数据库预置身份缺失、主表缺失、路由损坏和成功记录。
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "user-by-id.db")), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("gorm.Open(sqlite) error = %v", err)
	}
	if err = db.Table(TableNameUser).AutoMigrate(&userSQLiteForTest{}); err != nil {
		t.Fatalf("AutoMigrate(user) error = %v", err)
	}
	if err = migrateUserIdentityTablesForTest(db); err != nil {
		t.Fatalf("migrateUserIdentityTablesForTest() error = %v", err)
	}

	const (
		identityMissingID int64 = 200001
		mainMissingID     int64 = 200002
		badIdentityShard  int64 = 200003
		badIdentityValue  int64 = 200004
		badMainShard      int64 = 200005
		validID           int64 = 200006
	)
	now := time.Date(2026, time.January, 2, 3, 4, 5, 0, time.UTC)
	validUser := User{
		ID:              validID,
		ShardNo:         idgen.ShardNo(validID),
		Username:        "valid_user",
		Nickname:        "Valid User",
		PasswordHash:    "password-hash",
		EmailCiphertext: "email-ciphertext",
		EmailHash:       strings.Repeat("a", 64),
		EmailMasked:     "v***@example.test",
		EmailKeyVersion: "email-v1",
		PhoneCiphertext: "phone-ciphertext",
		PhoneHash:       strings.Repeat("b", 64),
		PhoneMasked:     "138****0000",
		PhoneKeyVersion: "phone-v1",
		Avatar:          "https://example.test/avatar.png",
		Status:          UserStatusEnabled,
		AuthVersion:     7,
		LastLoginAt:     now,
		LastLoginIP:     "127.0.0.1",
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	wrongMainUser := User{
		ID:           badMainShard,
		ShardNo:      (idgen.ShardNo(badMainShard) + 1) % idgen.ShardMod,
		Username:     "wrong_main_shard",
		PasswordHash: "password-hash",
		Status:       UserStatusEnabled,
		AuthVersion:  1,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	if err = db.Table(TableNameUser).Create(&[]User{wrongMainUser, validUser}).Error; err != nil {
		t.Fatalf("create user fixtures error = %v", err)
	}
	identityRows := []userIdentitySQLiteForTest{
		{IdentityValue: "main_missing", UserID: mainMissingID, UserShardNo: idgen.ShardNo(mainMissingID)},
		{IdentityValue: "bad_identity_shard", UserID: badIdentityShard, UserShardNo: (idgen.ShardNo(badIdentityShard) + 1) % idgen.ShardMod},
		{IdentityValue: " Bad_Identity_Value ", UserID: badIdentityValue, UserShardNo: idgen.ShardNo(badIdentityValue)},
		{IdentityValue: "wrong_main_shard", UserID: badMainShard, UserShardNo: idgen.ShardNo(badMainShard)},
		{IdentityValue: validUser.Username, UserID: validID, UserShardNo: validUser.ShardNo},
	}
	if err = db.Table(TableNameUserIdentityUsername).Create(&identityRows).Error; err != nil {
		t.Fatalf("create identity fixtures error = %v", err)
	}

	// 查询回调按子用例统计，参数错误必须零查询，其余分支恰好一次。
	queryCount := 0
	if err = db.Callback().Query().Before("gorm:query").Register("test:count_find_user_by_id_query", func(*gorm.DB) {
		queryCount++
	}); err != nil {
		t.Fatalf("register query counter error = %v", err)
	}
	cases := []struct {
		name                string // 用于 t.Run 区分缺失、损坏和成功分支
		userID              int64  // 决定本次单查的固定路由桶
		wantQueries         int    // 零值断言参数校验不访问数据库
		wantErrText         string // 空值表示不应返回错误
		wantIdentityMissing bool   // 断言身份缺失哨兵错误未被包装丢失
		wantUser            bool   // 断言成功分支返回完整用户实体
	}{
		{name: "non-positive id", userID: 0, wantQueries: 0},
		{name: "identity missing", userID: identityMissingID, wantQueries: 1, wantErrText: "用户身份索引缺失", wantIdentityMissing: true},
		{name: "main missing", userID: mainMissingID, wantQueries: 1, wantErrText: fmt.Sprintf("用户身份索引存在但主表记录缺失 user_id=%d table=%s", mainMissingID, TableNameUser)},
		{name: "identity shard mismatch", userID: badIdentityShard, wantQueries: 1, wantErrText: "与 user_id="},
		{name: "identity value not canonical", userID: badIdentityValue, wantQueries: 1, wantErrText: "identity_value 必须使用规范值"},
		{name: "main shard mismatch", userID: badMainShard, wantQueries: 1, wantErrText: fmt.Sprintf("用户身份索引存在但主表记录缺失 user_id=%d table=%s", badMainShard, TableNameUser)},
		{name: "success", userID: validID, wantQueries: 1, wantUser: true},
	}
	// 每种损坏状态必须保留独立错误语义，成功分支返回完整实体。
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			queriesBefore := queryCount
			got, findErr := FindUserByID(db, tt.userID, UserRouteShardCountDefault)
			if gotQueries := queryCount - queriesBefore; gotQueries != tt.wantQueries {
				t.Fatalf("query count = %d, want %d", gotQueries, tt.wantQueries)
			}
			if errors.Is(findErr, ErrUserIdentityMissing) != tt.wantIdentityMissing {
				t.Fatalf("identity missing error = %v, want %v; error=%v", errors.Is(findErr, ErrUserIdentityMissing), tt.wantIdentityMissing, findErr)
			}
			if tt.wantErrText == "" {
				if findErr != nil {
					t.Fatalf("FindUserByID() error = %v", findErr)
				}
			} else if findErr == nil || !strings.Contains(findErr.Error(), tt.wantErrText) {
				t.Fatalf("FindUserByID() error = %v, want containing %q", findErr, tt.wantErrText)
			}
			if tt.wantUser {
				if got == nil || *got != validUser {
					t.Fatalf("FindUserByID() = %+v, want %+v", got, validUser)
				}
			} else if got != nil {
				t.Fatalf("FindUserByID() = %+v, want nil", got)
			}
		})
	}
}

// TestUserPhysicalTableName 验证固定逻辑桶稳定路由到用户物理表。
func TestUserPhysicalTableName(t *testing.T) {
	tests := []struct {
		name            string // 区分单表、分桶边界与最大分表数。
		shardNo         int    // 固定逻辑桶号，范围为 0～1023。
		routeShardCount int    // 当前分表档位，必须整除逻辑桶总数。
		want            string // 首段保留 user 表名，其余段带起始桶号。
	}{
		{name: "single", shardNo: 1023, routeShardCount: 1, want: "user"},
		{name: "two first", shardNo: 0, routeShardCount: 2, want: "user"},
		{name: "two boundary", shardNo: 512, routeShardCount: 2, want: "user_b0512"},
		{name: "four middle", shardNo: 700, routeShardCount: 4, want: "user_b0512"},
		{name: "sixteen middle", shardNo: 345, routeShardCount: 16, want: "user_b0320"},
		{name: "full last", shardNo: 1023, routeShardCount: 1024, want: "user_b1023"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := UserPhysicalTableName(tt.shardNo, tt.routeShardCount)
			if err != nil {
				t.Fatalf("UserPhysicalTableName() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("UserPhysicalTableName() = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestUserIdentityTableName 验证身份类型稳定路由到独立物理表。
func TestUserIdentityTableName(t *testing.T) {
	tests := []struct {
		identityType string // 仅使用内部约定的小写规范类型。
		want         string // 四类身份分别使用独立索引表。
	}{
		{identityType: UserIdentityTypeUsername, want: TableNameUserIdentityUsername},
		{identityType: UserIdentityTypeEmail, want: TableNameUserIdentityEmail},
		{identityType: UserIdentityTypePhone, want: TableNameUserIdentityPhone},
		{identityType: UserIdentityTypeOAuth, want: TableNameUserIdentityOAuth},
	}
	for _, tt := range tests {
		t.Run(tt.identityType, func(t *testing.T) {
			got, err := UserIdentityTableName(tt.identityType)
			if err != nil {
				t.Fatalf("UserIdentityTableName() error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("UserIdentityTableName() = %q, want %q", got, tt.want)
			}
		})
	}
	if _, err := UserIdentityTableName("unknown"); err == nil {
		t.Fatal("期望非法身份类型返回错误")
	}
	for _, identityType := range []string{" EMAIL", "Email", "email "} {
		if _, err := UserIdentityTableName(identityType); err == nil {
			t.Fatalf("非规范身份类型不应命中物理表: %q", identityType)
		}
	}
}

// TestNormalizeUserIdentityRejectsNonCanonicalTypeProvider 确保内部身份路由不恢复大小写或空白别名。
func TestNormalizeUserIdentityRejectsNonCanonicalTypeProvider(t *testing.T) {
	tests := []struct {
		identityType string // 待校验身份类型
		provider     string // 待校验提供方
	}{
		{identityType: " EMAIL", provider: UserIdentityProviderLocal},
		{identityType: "Email", provider: UserIdentityProviderLocal},
		{identityType: UserIdentityTypeUsername, provider: "local"},
		{identityType: UserIdentityTypeOAuth, provider: " Google"},
		{identityType: UserIdentityTypeOAuth, provider: "GOOGLE"},
	}
	for _, tt := range tests {
		if _, _, _, err := NormalizeUserIdentity(tt.identityType, tt.provider, "demo"); err == nil {
			t.Fatalf("expected non-canonical identity route to be rejected: type=%q provider=%q", tt.identityType, tt.provider)
		}
	}
}

// TestUserPhysicalTableNameRejectsInvalidRoute 验证物理分片数只接受平滑拆分档位。
func TestUserPhysicalTableNameRejectsInvalidRoute(t *testing.T) {
	if _, err := UserPhysicalTableName(0, 0); err == nil {
		t.Fatal("期望零值物理分片数返回错误")
	}
	if _, err := UserPhysicalTableName(1, 3); err == nil {
		t.Fatal("期望非法物理分片数返回错误")
	}
	if _, err := UserPhysicalTableName(1, -1); err == nil {
		t.Fatal("期望负数路由值返回错误")
	}
	if _, err := UserPhysicalTableName(1024, 2); err == nil {
		t.Fatal("期望非法 shard_no 返回错误")
	}
}

// TestUserIdentityTableNameRejectsMismatchedShardNo 验证身份索引不会接受错误分片号。
func TestUserIdentityTableNameRejectsMismatchedShardNo(t *testing.T) {
	userID := int64(1)
	for idgen.ShardNo(userID) < 512 {
		userID++
	}
	identity := &UserIdentity{
		IdentityType:  UserIdentityTypeUsername,
		Provider:      UserIdentityProviderLocal,
		IdentityValue: "demo_user",
		UserID:        userID,
		UserShardNo:   idgen.ShardNo(userID),
	}
	const currentRouteShardCount = 2
	want, err := UserPhysicalTableName(identity.UserShardNo, currentRouteShardCount)
	if err != nil {
		t.Fatalf("UserPhysicalTableName() error = %v", err)
	}
	got, err := identity.UserTableName(currentRouteShardCount)
	if err != nil {
		t.Fatalf("UserTableName() error = %v", err)
	}
	if got != want {
		t.Fatalf("UserTableName() = %q, want %q", got, want)
	}

	identity.UserShardNo = (identity.UserShardNo + 1) % idgen.ShardMod
	if _, err := identity.UserTableName(currentRouteShardCount); err == nil {
		t.Fatal("期望身份索引 user_shard_no 与 user_id 不一致时返回错误")
	}
}

// TestValidateUserIdentityRouteRejectsNonCanonicalStoredValues 确保身份索引不会把 trim、大小写或错误列组合当成同一身份。
func TestValidateUserIdentityRouteRejectsNonCanonicalStoredValues(t *testing.T) {
	userID := int64(42)
	shardNo := idgen.ShardNo(userID)
	tests := []*UserIdentity{
		{IdentityType: UserIdentityTypeUsername, IdentityValue: " Demo_User ", UserID: userID, UserShardNo: shardNo},
		{IdentityType: UserIdentityTypeUsername, IdentityValue: "demo_user", IdentityHash: strings.Repeat("a", 64), UserID: userID, UserShardNo: shardNo},
		{IdentityType: UserIdentityTypeEmail, IdentityValue: "demo@example.com", IdentityHash: strings.Repeat("a", 64), UserID: userID, UserShardNo: shardNo},
		{IdentityType: UserIdentityTypeEmail, IdentityHash: strings.Repeat("A", 64), UserID: userID, UserShardNo: shardNo},
	}
	for _, identity := range tests {
		if err := validateUserIdentityRoute(identity); err == nil {
			t.Fatalf("期望非规范身份索引被拒绝: %+v", identity)
		}
	}
}

// TestSafeUserUpdatesRejectsImmutableFields 验证通用更新不会修改用户分片和唯一账号字段。
func TestSafeUserUpdatesRejectsImmutableFields(t *testing.T) {
	got := safeUserUpdates(map[string]any{
		"id":            int64(1),
		"shard_no":      12,
		"username":      "changed",
		"password_hash": "unsafe",
		"status":        UserStatusDisabled,
		"auth_version":  uint64(2),
		"email":         "raw@example.com",
		"email_hash":    "hash",
		"unknown":       "must-not-reach-gorm",
		"Nickname":      "wrong-case",
	})
	for _, key := range []string{"id", "shard_no", "username", "password_hash", "email", "status", "auth_version"} {
		if _, ok := got[key]; ok {
			t.Fatalf("safeUserUpdates() should reject %s: %+v", key, got)
		}
	}
	if got["email_hash"] != "hash" {
		t.Fatalf("safeUserUpdates() should keep secure email fields: %+v", got)
	}
	for _, key := range []string{"unknown", "Nickname"} {
		if _, ok := got[key]; ok {
			t.Fatalf("safeUserUpdates() should reject unknown or non-canonical field %s: %+v", key, got)
		}
	}
}

// TestCreateUserWithIdentitiesPreservesStatus 验证显式禁用不会被 GORM 的启用默认值覆盖。
func TestCreateUserWithIdentitiesPreservesStatus(t *testing.T) {
	// SQLite 会实际执行 GORM 创建回调和 SQL；MySQL 初始化资产仍需独立集成验证。
	for _, status := range []int{UserStatusDisabled, UserStatusEnabled} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "user-status.db")), &gorm.Config{
				SkipDefaultTransaction: true, // 与正式工厂一致，保留注册显式事务的真实边界。
				Logger:                 logger.Default.LogMode(logger.Silent),
			})
			if err != nil {
				t.Fatal(err)
			}
			if err = migrateUserIdentityTablesForTest(db); err != nil {
				t.Fatal(err)
			}
			if err = db.Table(TableNameUser).AutoMigrate(&userSQLiteForTest{}); err != nil {
				t.Fatal(err)
			}
			user := userForIdentityConflictTest(100001, "status_user", "")
			user.Status = status
			// 必须经过身份索引和主表的真实创建链路，不能只检查结构体赋值。
			if err = CreateUserWithIdentities(db, user, 1, testUserPrivacySecret, "last_login_at"); err != nil {
				t.Fatal(err)
			}
			got, err := FindUserByIdentity(db, UserIdentityTypeUsername, UserIdentityProviderLocal, user.Username, testUserPrivacySecret, 1)
			if err != nil {
				t.Fatal(err)
			}
			if got == nil || got.Status != status || user.Status != status {
				t.Fatalf("created user = %+v, input status = %d, want status = %d", got, user.Status, status)
			}
		})
	}
}

// TestCreateUserWithIdentitiesUsesPhysicalTable 验证新用户和身份目录使用同一物理路由。
func TestCreateUserWithIdentitiesUsesPhysicalTable(t *testing.T) {
	// 选取第二物理路由范围内的用户 ID，避免默认表掩盖路由错误。
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "user-route.db")), &gorm.Config{
		SkipDefaultTransaction: true, // 注册主表与身份目录仍需同一显式事务提交。
		Logger:                 logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("gorm.Open(sqlite) error = %v", err)
	}
	userID, shardNo := userIDInShardRangeForTest(t, 512, 767)
	tableName, err := UserPhysicalTableName(shardNo, 4)
	if err != nil {
		t.Fatalf("UserPhysicalTableName() error = %v", err)
	}
	if tableName != "user_b0512" {
		t.Fatalf("route table = %s, want user_b0512 for shard=%d", tableName, shardNo)
	}
	if err = migrateUserIdentityTablesForTest(db); err != nil {
		t.Fatalf("migrateUserIdentityTablesForTest() error = %v", err)
	}
	if err = db.Table(tableName).AutoMigrate(&userSQLiteForTest{}); err != nil {
		t.Fatalf("AutoMigrate(%s) error = %v", tableName, err)
	}
	now := time.Now()
	user := &User{
		ID:           userID,
		ShardNo:      shardNo,
		Username:     "route_user",
		Nickname:     "route_user",
		PasswordHash: "hash",
		Email:        "Route_User@Example.Test",
		Phone:        "19900000001",
		Status:       UserStatusEnabled,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
	// 创建后核对用户表和联系方式身份索引使用同一分片信息。
	if err = CreateUserWithIdentities(db, user, 4, testUserPrivacySecret); err != nil {
		t.Fatalf("CreateUserWithIdentities() error = %v", err)
	}
	var count int64
	if err = db.Table(tableName).Where("id = ?", userID).Count(&count).Error; err != nil {
		t.Fatalf("count routed user error = %v", err)
	}
	if count != 1 {
		t.Fatalf("routed table count = %d, want 1", count)
	}
	// 单表资料更新不得开启事务，GORM 回调直接检查连接池类型。
	usedTransaction := false
	const callbackName = "test:user_profile_no_transaction"
	if err = db.Callback().Update().Before("gorm:update").Register(callbackName, func(tx *gorm.DB) {
		if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); ok {
			usedTransaction = true
		}
	}); err != nil {
		t.Fatalf("register profile transaction callback error = %v", err)
	}
	if err = UpdateUser(db, userID, map[string]any{"last_login_ip": "127.0.0.1"}, 4); err != nil {
		t.Fatalf("UpdateUser() error = %v", err)
	}
	if err = UpdateUserProfileWithIdentities(db, userID, map[string]any{"nickname": "route_user_changed"}, testUserPrivacySecret, 4); err != nil {
		t.Fatalf("UpdateUserProfileWithIdentities(nickname) error = %v", err)
	}
	if usedTransaction {
		t.Fatal("single-table user updates should not start a database transaction")
	}
	if err = db.Callback().Update().Remove(callbackName); err != nil {
		t.Fatalf("remove profile transaction callback error = %v", err)
	}
	got, err := FindUserByIdentity(db, UserIdentityTypeUsername, UserIdentityProviderLocal, "route_user", testUserPrivacySecret, 4)
	if err != nil {
		t.Fatalf("FindUserByIdentity(username) error = %v", err)
	}
	if got == nil || got.ID != userID {
		t.Fatalf("FindUserByIdentity(username) = %+v, want id=%d", got, userID)
	}
	identity, err := FindUserIdentity(db, UserIdentityTypeEmail, UserIdentityProviderLocal, "route_user@example.test", testUserPrivacySecret)
	if err != nil {
		t.Fatalf("FindUserIdentity(email) error = %v", err)
	}
	if identity == nil || identity.IdentityHash != user.EmailHash || identity.UserShardNo != shardNo || identity.UserID != userID {
		t.Fatalf("identity = %+v, want user=%d shard=%d", identity, userID, shardNo)
	}
	var emailCount int64
	if err = db.Table(TableNameUserIdentityEmail).Where("user_id = ?", userID).Count(&emailCount).Error; err != nil {
		t.Fatalf("count email identity error = %v", err)
	}
	if emailCount != 1 {
		t.Fatalf("email identity table count = %d, want 1", emailCount)
	}
	// 联系方式变化只允许查询必要身份表并更新主表与对应索引表。
	queryTables := make(map[string]int)
	updateTables := make(map[string]int)
	const (
		queryCallbackName  = "test:user_profile_query_tables"
		updateCallbackName = "test:user_profile_update_tables"
	)
	if err = db.Callback().Query().Before("gorm:query").Register(queryCallbackName, func(tx *gorm.DB) {
		queryTables[tx.Statement.Table]++
	}); err != nil {
		t.Fatalf("register profile query callback error = %v", err)
	}
	if err = db.Callback().Update().Before("gorm:update").Register(updateCallbackName, func(tx *gorm.DB) {
		updateTables[tx.Statement.Table]++
	}); err != nil {
		t.Fatalf("register profile update callback error = %v", err)
	}
	if err = UpdateUserProfileWithIdentities(db, userID, map[string]any{"email": "changed@example.test"}, testUserPrivacySecret, 4); err != nil {
		t.Fatalf("UpdateUserProfileWithIdentities() error = %v", err)
	}
	if queryTables[TableNameUserIdentityUsername] != 1 || queryTables[TableNameUserIdentityEmail] != 1 ||
		queryTables[tableName] != 0 || queryTables[TableNameUserIdentityPhone] != 0 {
		t.Fatalf("email update queried unexpected tables: %+v", queryTables)
	}
	if updateTables[tableName] != 1 || updateTables[TableNameUserIdentityEmail] != 1 || updateTables[TableNameUserIdentityPhone] != 0 {
		t.Fatalf("email update wrote unexpected tables: %+v", updateTables)
	}
	// 相同联系方式再次写入时仍更新主表，但不得重复写身份索引。
	clear(queryTables)
	clear(updateTables)
	if err = UpdateUserProfileWithIdentities(db, userID, map[string]any{"email": "changed@example.test"}, testUserPrivacySecret, 4); err != nil {
		t.Fatalf("UpdateUserProfileWithIdentities(same email) error = %v", err)
	}
	if queryTables[TableNameUserIdentityUsername] != 1 || queryTables[TableNameUserIdentityEmail] != 1 ||
		queryTables[tableName] != 0 || queryTables[TableNameUserIdentityPhone] != 0 {
		t.Fatalf("same email update queried unexpected tables: %+v", queryTables)
	}
	if updateTables[tableName] != 1 || updateTables[TableNameUserIdentityEmail] != 0 || updateTables[TableNameUserIdentityPhone] != 0 {
		t.Fatalf("same email update wrote unchanged identity: %+v", updateTables)
	}
	if err = db.Callback().Query().Remove(queryCallbackName); err != nil {
		t.Fatalf("remove profile query callback error = %v", err)
	}
	if err = db.Callback().Update().Remove(updateCallbackName); err != nil {
		t.Fatalf("remove profile update callback error = %v", err)
	}
	if oldIdentity, err := FindUserIdentity(db, UserIdentityTypeEmail, UserIdentityProviderLocal, "route_user@example.test", testUserPrivacySecret); err != nil || oldIdentity != nil {
		t.Fatalf("old email identity = %+v err=%v, want nil", oldIdentity, err)
	}
	if newIdentity, err := FindUserIdentity(db, UserIdentityTypeEmail, UserIdentityProviderLocal, "changed@example.test", testUserPrivacySecret); err != nil || newIdentity == nil || newIdentity.UserID != userID {
		t.Fatalf("new email identity = %+v err=%v, want user=%d", newIdentity, err, userID)
	}
}

// TestCreateUserWithIdentitiesRollsBackOnMainConflict 验证关闭默认事务后，主表失败仍回滚先写入的身份目录。
func TestCreateUserWithIdentitiesRollsBackOnMainConflict(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "user-main-conflict.db")), &gorm.Config{
		SkipDefaultTransaction: true, // 只依赖注册流程自身的显式事务。
		Logger:                 logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := migrateUserIdentityTablesForTest(db); err != nil {
		t.Fatal(err)
	}
	if err := db.Table(TableNameUser).AutoMigrate(&userSQLiteForTest{}); err != nil {
		t.Fatal(err)
	}

	// 预占主键使最后一步 INSERT 失败，前三张身份表的成功写入必须全部撤销。
	existing := userForIdentityConflictTest(100001, "occupied_user", "")
	if err := db.Table(TableNameUser).Create(existing).Error; err != nil {
		t.Fatal(err)
	}
	user := userForIdentityConflictTest(existing.ID, "rollback_user", "rollback@example.test")
	user.Phone = "19900000001"
	createCalls := 0 // 确认已经经过三个身份 INSERT，而非在输入校验阶段提前失败。
	if err := db.Callback().Create().Before("gorm:create").Register("test:registration_transaction", func(tx *gorm.DB) {
		createCalls++
		if _, ok := tx.Statement.ConnPool.(gorm.TxCommitter); !ok {
			t.Errorf("写入未使用注册显式事务: %s", tx.Statement.Table)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := CreateUserWithIdentities(db, user, 1, testUserPrivacySecret); err == nil {
		t.Fatal("主表主键冲突应返回错误")
	}
	if createCalls != 4 {
		t.Fatalf("新增回调次数 = %d，期望三条身份和一条主表写入", createCalls)
	}

	// 返回错误之外，还必须保证没有悬空身份，且原有主表记录未被覆盖。
	for _, table := range []string{TableNameUserIdentityUsername, TableNameUserIdentityEmail, TableNameUserIdentityPhone} {
		var count int64
		if err := db.Table(table).Where("user_id = ?", user.ID).Count(&count).Error; err != nil {
			t.Fatal(err)
		}
		if count != 0 {
			t.Errorf("%s 遗留 %d 条部分提交的身份", table, count)
		}
	}
	var saved User
	if err := db.Table(TableNameUser).Where("id = ?", existing.ID).Take(&saved).Error; err != nil {
		t.Fatal(err)
	}
	if saved.Username != existing.Username {
		t.Fatalf("原有主表记录被改变: %+v", saved)
	}
}

// TestUpdateUserProfileWithIdentitiesRollsBackOnIdentityConflict 验证身份索引冲突时主表资料同步回滚。
func TestUpdateUserProfileWithIdentitiesRollsBackOnIdentityConflict(t *testing.T) {
	// 两个用户分别占用不同邮箱身份，构造唯一索引冲突。
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "user-identity-conflict.db")), &gorm.Config{
		SkipDefaultTransaction: true, // 不让 GORM 默认事务掩盖业务回滚边界。
		Logger:                 logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("gorm.Open(sqlite) error = %v", err)
	}
	if err = migrateUserIdentityTablesForTest(db); err != nil {
		t.Fatalf("migrateUserIdentityTablesForTest() error = %v", err)
	}
	if err = db.Table(TableNameUser).AutoMigrate(&userSQLiteForTest{}); err != nil {
		t.Fatalf("AutoMigrate(user) error = %v", err)
	}
	firstUser := userForIdentityConflictTest(100001, "identity_user_001", "first@example.test")
	secondUser := userForIdentityConflictTest(100002, "identity_user_002", "second@example.test")
	if err = CreateUserWithIdentities(db, firstUser, UserRouteShardCountDefault, testUserPrivacySecret); err != nil {
		t.Fatalf("CreateUserWithIdentities(first) error = %v", err)
	}
	if err = CreateUserWithIdentities(db, secondUser, UserRouteShardCountDefault, testUserPrivacySecret); err != nil {
		t.Fatalf("CreateUserWithIdentities(second) error = %v", err)
	}
	// 冲突失败后主表安全字段和双方身份索引都必须保持原值。
	err = UpdateUserProfileWithIdentities(db, firstUser.ID, map[string]any{"email": secondUser.Email}, testUserPrivacySecret, UserRouteShardCountDefault)
	if err == nil {
		t.Fatal("期望邮箱身份冲突时返回错误")
	}
	got, err := FindUserByID(db, firstUser.ID, UserRouteShardCountDefault)
	if err != nil {
		t.Fatalf("FindUserByID(first) error = %v", err)
	}
	if got == nil || got.EmailHash != firstUser.EmailHash || got.EmailMasked != firstUser.EmailMasked {
		t.Fatalf("first user secure email = %+v, want hash=%s masked=%s", got, firstUser.EmailHash, firstUser.EmailMasked)
	}
	firstIdentity, err := FindUserIdentity(db, UserIdentityTypeEmail, UserIdentityProviderLocal, firstUser.Email, testUserPrivacySecret)
	if err != nil {
		t.Fatalf("FindUserIdentity(first email) error = %v", err)
	}
	if firstIdentity == nil || firstIdentity.UserID != firstUser.ID {
		t.Fatalf("first email identity = %+v, want user=%d", firstIdentity, firstUser.ID)
	}
	secondIdentity, err := FindUserIdentity(db, UserIdentityTypeEmail, UserIdentityProviderLocal, secondUser.Email, testUserPrivacySecret)
	if err != nil {
		t.Fatalf("FindUserIdentity(second email) error = %v", err)
	}
	if secondIdentity == nil || secondIdentity.UserID != secondUser.ID {
		t.Fatalf("second email identity = %+v, want user=%d", secondIdentity, secondUser.ID)
	}
}

// TestUserUpdatesRejectMissingMainRow 确保身份目录与主表并发变化时不会把零行更新当成成功。
func TestUserUpdatesRejectMissingMainRow(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "user-missing-main.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatalf("gorm.Open(sqlite) error = %v", err)
	}
	if err = migrateUserIdentityTablesForTest(db); err != nil {
		t.Fatalf("migrateUserIdentityTablesForTest() error = %v", err)
	}
	if err = db.Table(TableNameUser).AutoMigrate(&userSQLiteForTest{}); err != nil {
		t.Fatalf("AutoMigrate(user) error = %v", err)
	}
	user := userForIdentityConflictTest(100003, "missing_main_user", "")
	if err = CreateUserWithIdentities(db, user, UserRouteShardCountDefault, testUserPrivacySecret); err != nil {
		t.Fatalf("CreateUserWithIdentities() error = %v", err)
	}
	// 顺序删除主表复现悬空身份目录，不以此证明并发删除的事务时序。
	if err = db.Table(TableNameUser).Where("id = ?", user.ID).Delete(&userSQLiteForTest{}).Error; err != nil {
		t.Fatalf("delete main user error = %v", err)
	}
	if err = UpdateUser(db, user.ID, map[string]any{"nickname": "changed"}, UserRouteShardCountDefault); err == nil {
		t.Fatal("UpdateUser() expected missing main row error")
	}
	if err = UpdateUserProfileWithIdentities(db, user.ID, map[string]any{"nickname": "changed"}, testUserPrivacySecret, UserRouteShardCountDefault); err == nil {
		t.Fatal("UpdateUserProfileWithIdentities() expected missing main row error")
	}
}

// userIDInShardRangeForTest 在指定逻辑分片区间内寻找测试 ID，未找到时终止用例。
func userIDInShardRangeForTest(t *testing.T, minShardNo int, maxShardNo int) (int64, int) {
	t.Helper()
	for id := int64(100000); id < 200000; id++ {
		shardNo := idgen.ShardNo(id)
		if shardNo >= minShardNo && shardNo <= maxShardNo {
			return id, shardNo
		}
	}
	t.Fatalf("cannot find test user id in shard range %d-%d", minShardNo, maxShardNo)
	return 0, 0
}

// userForIdentityConflictTest 构造身份冲突测试用户。
func userForIdentityConflictTest(id int64, username string, email string) *User {
	now := time.Now()
	return &User{
		ID:           id,
		ShardNo:      idgen.ShardNo(id),
		Username:     username,
		Nickname:     username,
		PasswordHash: "hash",
		Email:        email,
		Status:       UserStatusEnabled,
		CreatedAt:    now,
		UpdatedAt:    now,
	}
}

// migrateUserIdentityTablesForTest 创建四张登录身份索引物理表。
func migrateUserIdentityTablesForTest(db *gorm.DB) error {
	for _, tableName := range []string{
		TableNameUserIdentityUsername,
		TableNameUserIdentityEmail,
		TableNameUserIdentityPhone,
		TableNameUserIdentityOAuth,
	} {
		if err := db.Table(tableName).AutoMigrate(&userIdentitySQLiteForTest{}); err != nil {
			return err
		}
		if err := createUserIdentitySQLiteIndexes(db, tableName); err != nil {
			return err
		}
	}
	return nil
}

// createUserIdentitySQLiteIndexes 使用表名前缀规避 SQLite 全库索引名唯一限制。
func createUserIdentitySQLiteIndexes(db *gorm.DB, tableName string) error {
	var statements []string
	switch tableName {
	case TableNameUserIdentityUsername:
		statements = []string{
			fmt.Sprintf("CREATE UNIQUE INDEX IF NOT EXISTS `%s_identity_value` ON `%s` (`identity_value`)", tableName, tableName),
			fmt.Sprintf("CREATE UNIQUE INDEX IF NOT EXISTS `%s_user` ON `%s` (`user_id`)", tableName, tableName),
		}
	case TableNameUserIdentityEmail, TableNameUserIdentityPhone:
		statements = []string{
			fmt.Sprintf("CREATE UNIQUE INDEX IF NOT EXISTS `%s_identity_hash` ON `%s` (`identity_hash`)", tableName, tableName),
			fmt.Sprintf("CREATE UNIQUE INDEX IF NOT EXISTS `%s_user` ON `%s` (`user_id`)", tableName, tableName),
		}
	case TableNameUserIdentityOAuth:
		statements = []string{
			fmt.Sprintf("CREATE UNIQUE INDEX IF NOT EXISTS `%s_provider_value` ON `%s` (`provider`, `identity_value`)", tableName, tableName),
			fmt.Sprintf("CREATE UNIQUE INDEX IF NOT EXISTS `%s_user_provider` ON `%s` (`user_id`, `provider`)", tableName, tableName),
		}
	default:
		return fmt.Errorf("unknown identity table %s", tableName)
	}
	for _, statement := range statements {
		if err := db.Exec(statement).Error; err != nil {
			return err
		}
	}
	return nil
}

// userSQLiteForTest 以 SQLite 可执行类型建表，业务读写仍走 User，不替代 MySQL DDL 校验。
type userSQLiteForTest struct {
	ID              int64     `gorm:"column:id;type:integer;primaryKey"`                                                 // 测试显式指定 ID，以复现物理路由边界。
	ShardNo         int       `gorm:"column:shard_no;type:int;not null;index:idx_user_shard_no_id,priority:1"`           // 必须等于 ID 计算出的逻辑桶号。
	Username        string    `gorm:"column:username;type:varchar(32);not null;uniqueIndex:uk_user_username"`            // 唯一约束用于断言账号不能重复创建。
	Nickname        string    `gorm:"column:nickname;type:varchar(64);not null;default:''"`                              // 资料单表更新使用此字段验证免事务路径。
	PasswordHash    string    `gorm:"column:password_hash;type:varchar(255);not null"`                                   // 夹具不校验密码，只验证该字段不会被通用更新改写。
	AuthVersion     uint64    `gorm:"column:auth_version;type:integer;not null;default:1"`                               // 联查必须完整返回，通用资料更新不能推进版本。
	EmailCiphertext string    `gorm:"column:email_ciphertext;type:varchar(512);not null;default:''"`                     // 联系方式更新由真实隐私逻辑生成密文。
	EmailHash       string    `gorm:"column:email_hash;type:char(64);not null;default:'';index:idx_user_email_hash"`     // 与邮箱身份索引使用相同 HMAC 值。
	EmailMasked     string    `gorm:"column:email_masked;type:varchar(128);not null;default:''"`                         // 身份冲突回滚后必须保留更新前展示值。
	EmailKeyVersion string    `gorm:"column:email_key_version;type:varchar(32);not null;default:''"`                     // 与邮箱密文同时写入，空邮箱使用空字符串。
	PhoneCiphertext string    `gorm:"column:phone_ciphertext;type:varchar(512);not null;default:''"`                     // 主表不保存原始手机号。
	PhoneHash       string    `gorm:"column:phone_hash;type:char(64);not null;default:'';index:idx_user_phone_hash"`     // 与手机号身份索引使用相同 HMAC 值。
	PhoneMasked     string    `gorm:"column:phone_masked;type:varchar(32);not null;default:''"`                          // 用户联查直接返回的脱敏手机号。
	PhoneKeyVersion string    `gorm:"column:phone_key_version;type:varchar(32);not null;default:''"`                     // 与手机号密文同时写入，空手机号使用空字符串。
	Avatar          string    `gorm:"column:avatar;type:varchar(255);not null;default:''"`                               // 未设置时为空，完整联查不能遗漏此列。
	Status          int       `gorm:"column:status;type:tinyint;not null;default:1;index:idx_user_status_id,priority:1"` // 保留 GORM 默认值以复现显式禁用被覆盖的风险。
	LastLoginAt     time.Time `gorm:"column:last_login_at;type:datetime"`                                                // 未登录夹具允许省略，完整联查须保留已有值。
	LastLoginIP     string    `gorm:"column:last_login_ip;type:varchar(45);not null;default:''"`                         // 成功登录单表更新的测试目标字段。
	CreatedAt       time.Time `gorm:"column:created_at;type:datetime;not null;default:CURRENT_TIMESTAMP"`                // 联查断言使用固定时间，避免只验证身份字段。
	UpdatedAt       time.Time `gorm:"column:updated_at;type:datetime;not null;default:CURRENT_TIMESTAMP"`                // 由 GORM 写入流程维护。
}

// userIdentitySQLiteForTest 使用 SQLite 创建身份索引表，业务读写仍走 UserIdentity。
type userIdentitySQLiteForTest struct {
	ID            uint64    `gorm:"column:id;type:integer;primaryKey;autoIncrement:true"`               // SQLite 自动分配的索引行主键，不承担用户路由。
	Provider      string    `gorm:"column:provider;type:varchar(32);not null;default:''"`               // OAuth 联合唯一键的一部分，本地身份必须为空。
	IdentityValue string    `gorm:"column:identity_value;type:varchar(191);not null;default:''"`        // 用户名和 OAuth 使用此列，联系方式必须留空。
	IdentityHash  string    `gorm:"column:identity_hash;type:char(64);not null;default:''"`             // 联系方式使用 HMAC，用户名和 OAuth 必须留空。
	UserID        int64     `gorm:"column:user_id;type:integer;not null"`                               // 与用户主表的显式 ID 一致。
	UserShardNo   int       `gorm:"column:user_shard_no;type:int;not null"`                             // 错桶夹具用于验证身份目录损坏会返回错误。
	CreatedAt     time.Time `gorm:"column:created_at;type:datetime;not null;default:CURRENT_TIMESTAMP"` // 由真实身份创建流程填充。
	UpdatedAt     time.Time `gorm:"column:updated_at;type:datetime;not null;default:CURRENT_TIMESTAMP"` // 联系方式绑定变化时随索引一起更新。
}

// TableName 将 SQLite 测试模型固定映射到账号身份索引表。
func (*userIdentitySQLiteForTest) TableName() string {
	return TableNameUserIdentityUsername
}
