package model

import (
	"encoding/hex"
	"fmt"
	"strings"
	"time"

	"api/common/idgen"

	"github.com/Is999/go-utils/errors"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// 业务用户表名、身份类型和状态枚举。
const (
	// TableNameUser 表示 user 业务用户表名，通过业务表名与后台管理员表区分。
	TableNameUser = "user"
	// TableNameUserIdentity 表示业务用户登录身份索引表名前缀。
	TableNameUserIdentity = "user_identity"
	// TableNameUserIdentityUsername 表示自定义账号登录身份索引表名。
	TableNameUserIdentityUsername = "user_identity_username"
	// TableNameUserIdentityEmail 表示邮箱登录身份索引表名。
	TableNameUserIdentityEmail = "user_identity_email"
	// TableNameUserIdentityPhone 表示手机号登录身份索引表名。
	TableNameUserIdentityPhone = "user_identity_phone"
	// TableNameUserIdentityOAuth 表示三方登录身份索引表名。
	TableNameUserIdentityOAuth = "user_identity_oauth"

	// UserIdentityTypeUsername 表示自定义账号登录身份。
	UserIdentityTypeUsername = "username"
	// UserIdentityTypeEmail 表示邮箱登录身份。
	UserIdentityTypeEmail = "email"
	// UserIdentityTypePhone 表示手机号登录身份。
	UserIdentityTypePhone = "phone"
	// UserIdentityTypeOAuth 表示三方登录身份。
	UserIdentityTypeOAuth = "oauth"
	// UserIdentityProviderLocal 表示本地账号、邮箱和手机号身份没有三方提供方。
	UserIdentityProviderLocal = ""

	// UserStatusDisabled 表示业务用户禁用状态。
	UserStatusDisabled = 0
	// UserStatusEnabled 表示业务用户正常状态。
	UserStatusEnabled = 1

	// UserRouteShardCountDefault 是业务用户表默认物理分片数。
	UserRouteShardCountDefault = 1
)

var (
	// ErrUserIdentityMissing 表示按用户 ID 查询时缺少主登录身份索引。
	ErrUserIdentityMissing = errors.New("用户身份索引缺失")
)

// User 表示业务用户实体。
type User struct {
	ID              int64     `gorm:"column:id;type:bigint;primaryKey;index:idx_user_shard_no_id,priority:2;index:idx_user_status_id,priority:2;comment:雪花 ID" json:"id"`              // 雪花 ID
	ShardNo         int       `gorm:"column:shard_no;type:int;not null;default:0;index:idx_user_shard_no_id,priority:1;comment:ID 哈希分片，CRC32(id字符串)%1024，用于分表和分片游标查询" json:"shard_no"` // ID 哈希分片，来源 idgen.ShardNo(id)
	Username        string    `gorm:"column:username;type:varchar(32);not null;uniqueIndex:uk_user_username;comment:用户名" json:"username"`                                              // 用户名
	Nickname        string    `gorm:"column:nickname;type:varchar(64);not null;default:'';comment:昵称" json:"nickname"`                                                                 // 昵称
	PasswordHash    string    `gorm:"column:password_hash;type:varchar(255);not null;comment:密码哈希" json:"-"`                                                                           // 密码哈希
	Email           string    `gorm:"-" json:"-"`                                                                                                                                      // 邮箱明文，仅用于写入前生成安全字段
	EmailCiphertext string    `gorm:"column:email_ciphertext;type:varchar(512);not null;default:'';comment:邮箱 AES-GCM 密文" json:"-"`                                                    // 邮箱密文
	EmailHash       string    `gorm:"column:email_hash;type:char(64);not null;default:'';index:idx_user_email_hash;comment:邮箱 HMAC 查询哈希" json:"-"`                                     // 邮箱查询哈希
	EmailMasked     string    `gorm:"column:email_masked;type:varchar(128);not null;default:'';comment:邮箱脱敏展示值" json:"emailMasked"`                                                    // 邮箱脱敏展示值
	EmailKeyVersion string    `gorm:"column:email_key_version;type:varchar(32);not null;default:'';comment:邮箱加密密钥版本" json:"-"`                                                         // 邮箱加密密钥版本
	Phone           string    `gorm:"-" json:"-"`                                                                                                                                      // 手机号明文，仅用于写入前生成安全字段
	PhoneCiphertext string    `gorm:"column:phone_ciphertext;type:varchar(512);not null;default:'';comment:手机号 AES-GCM 密文" json:"-"`                                                   // 手机号密文
	PhoneHash       string    `gorm:"column:phone_hash;type:char(64);not null;default:'';index:idx_user_phone_hash;comment:手机号 HMAC 查询哈希" json:"-"`                                    // 手机号查询哈希
	PhoneMasked     string    `gorm:"column:phone_masked;type:varchar(32);not null;default:'';comment:手机号脱敏展示值" json:"phoneMasked"`                                                    // 手机号脱敏展示值
	PhoneKeyVersion string    `gorm:"column:phone_key_version;type:varchar(32);not null;default:'';comment:手机号加密密钥版本" json:"-"`                                                        // 手机号加密密钥版本
	Avatar          string    `gorm:"column:avatar;type:varchar(255);not null;default:'';comment:头像" json:"avatar"`                                                                    // 头像
	Status          int       `gorm:"column:status;type:tinyint;not null;default:1;index:idx_user_status_id,priority:1;comment:状态：1 正常，0 禁用" json:"status"`                            // 状态：1 正常，0 禁用
	AuthVersion     uint64    `gorm:"column:auth_version;type:bigint unsigned;not null;default:1;comment:认证版本，敏感变更时单调递增" json:"-"`                                                     // 认证版本，用于撤销该版本之前的全部登录态
	LastLoginAt     time.Time `gorm:"column:last_login_at;type:datetime;comment:最后登录时间" json:"last_login_at"`                                                                          // 最后登录时间
	LastLoginIP     string    `gorm:"column:last_login_ip;type:varchar(45);not null;default:'';comment:最后登录 IP" json:"last_login_ip"`                                                  // 最后登录 IP
	CreatedAt       time.Time `gorm:"column:created_at;type:datetime;not null;default:CURRENT_TIMESTAMP;comment:创建时间" json:"created_at"`                                               // 创建时间
	UpdatedAt       time.Time `gorm:"column:updated_at;type:datetime;not null;default:CURRENT_TIMESTAMP;comment:更新时间" json:"updated_at"`                                               // 更新时间
}

// UserIdentity 表示业务用户登录身份索引，负责账号唯一性和固定逻辑桶定位。
type UserIdentity struct {
	ID            uint64    `gorm:"column:id;type:bigint unsigned;primaryKey;autoIncrement:true;comment:主键 ID" json:"id"`                                                           // 主键 ID
	IdentityType  string    `gorm:"-" json:"identityType"`                                                                                                                          // 身份类型，由物理表路由决定
	Provider      string    `gorm:"column:provider;type:varchar(32);not null;default:'';comment:三方身份提供方" json:"provider"`                                                           // 三方身份提供方，仅 oauth 表持久化
	IdentityValue string    `gorm:"column:identity_value;type:varchar(191);not null;comment:归一化身份值" json:"identityValue"`                                                           // 归一化身份值，仅 username/oauth 表持久化
	IdentityHash  string    `gorm:"column:identity_hash;type:char(64);not null;default:'';comment:邮箱或手机号身份 HMAC 哈希" json:"identityHash"`                                            // 邮箱或手机号身份哈希
	UserID        int64     `gorm:"column:user_id;type:bigint;not null;index:idx_user_identity_shard_user,priority:2;comment:业务用户雪花 ID" json:"userId"`                              // 业务用户雪花 ID
	UserShardNo   int       `gorm:"column:user_shard_no;type:int;not null;index:idx_user_identity_shard_user,priority:1;comment:业务用户 ID 哈希分片，CRC32(id字符串)%1024" json:"userShardNo"` // 业务用户逻辑分片
	CreatedAt     time.Time `gorm:"column:created_at;type:datetime;not null;default:CURRENT_TIMESTAMP;comment:创建时间" json:"createdAt"`                                               // 创建时间
	UpdatedAt     time.Time `gorm:"column:updated_at;type:datetime;not null;default:CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP;comment:更新时间" json:"updatedAt"`                   // 更新时间
}

// userByIDRow 承载按 ID 联查身份目录和用户主表的单行结果。
type userByIDRow struct {
	User                   // User 接收目标物理表存在时的完整用户字段
	IdentityValue   string `gorm:"column:identity_value"`    // 账号身份值用于复用现有规范值校验
	IdentityShardNo int    `gorm:"column:identity_shard_no"` // 身份固定桶必须与用户 ID 计算结果一致
	MainID          *int64 `gorm:"column:main_id"`           // nil 表示身份存在但目标物理表记录缺失
}

// TableName 返回业务用户表名。
func (*User) TableName() string {
	return TableNameUser
}

// TableName 返回默认账号登录身份索引表名，真实读写通过身份类型路由。
func (*UserIdentity) TableName() string {
	return TableNameUserIdentityUsername
}

// UserIdentityTableName 返回身份类型对应的物理登录身份索引表名。
func UserIdentityTableName(identityType string) (string, error) {
	switch identityType {
	case UserIdentityTypeUsername:
		return TableNameUserIdentityUsername, nil
	case UserIdentityTypeEmail:
		return TableNameUserIdentityEmail, nil
	case UserIdentityTypePhone:
		return TableNameUserIdentityPhone, nil
	case UserIdentityTypeOAuth:
		return TableNameUserIdentityOAuth, nil
	default:
		return "", errors.Errorf("不支持的用户登录身份类型[%s]", identityType)
	}
}

// UserTableName 按当前启动配置返回身份索引记录对应的用户表名。
func (i *UserIdentity) UserTableName(routeShardCount int) (string, error) {
	if i == nil {
		return "", errors.New("用户身份索引为空")
	}
	if err := validateUserIdentityRoute(i); err != nil {
		return "", errors.Tag(err)
	}
	return UserPhysicalTableName(i.UserShardNo, routeShardCount)
}

// IdentityTableName 返回身份索引记录当前应写入的物理身份表名。
func (i *UserIdentity) IdentityTableName() (string, error) {
	if i == nil {
		return "", errors.New("用户身份索引为空")
	}
	if err := validateUserIdentityRoute(i); err != nil {
		return "", errors.Tag(err)
	}
	return UserIdentityTableName(i.IdentityType)
}

// NormalizeUserIdentity 归一化用户登录身份，保证唯一索引输入稳定。
func NormalizeUserIdentity(identityType string, provider string, identityValue string) (string, string, string, error) {
	normalizedType, normalizedProvider, err := validateUserIdentityTypeProvider(identityType, provider)
	if err != nil {
		return "", "", "", errors.Tag(err)
	}
	value := strings.TrimSpace(identityValue)
	switch normalizedType {
	case UserIdentityTypeUsername, UserIdentityTypeEmail:
		value = strings.ToLower(value)
	}
	if value == "" {
		return "", "", "", errors.New("用户登录身份值不能为空")
	}
	return normalizedType, normalizedProvider, value, nil
}

// UserIdentitySubject 返回风控限流使用的稳定身份主体。
func UserIdentitySubject(identityType string, provider string, identityValue string) string {
	normalizedType, normalizedProvider, normalizedValue, err := NormalizeUserIdentity(identityType, provider, identityValue)
	if err != nil {
		return identityType + ":" + strings.TrimSpace(identityValue)
	}
	if normalizedProvider != "" {
		return normalizedType + ":" + normalizedProvider + ":" + normalizedValue
	}
	return normalizedType + ":" + normalizedValue
}

// FindUserByIdentity 根据登录身份和当前路由配置查询业务用户；未命中时返回 nil。
func FindUserByIdentity(db *gorm.DB, identityType string, provider string, identityValue string, privacySecret string, routeShardCount int) (*User, error) {
	identity, err := FindUserIdentity(db, identityType, provider, identityValue, privacySecret)
	if err != nil {
		return nil, errors.Tag(err)
	}
	return FindUserByIdentityRow(db, identity, routeShardCount)
}

// FindUserByID 根据 ID 联查账号身份和目标物理表，并区分身份、主表缺失状态。
func FindUserByID(db *gorm.DB, id int64, routeShardCount int) (*User, error) {
	if id <= 0 {
		return nil, nil
	}
	shardNo := idgen.ShardNo(id)
	tableName, err := UserPhysicalTableName(shardNo, routeShardCount)
	if err != nil {
		return nil, errors.Tag(err)
	}
	var row userByIDRow
	// 物理表只由请求 ID 的固定桶计算，目录中的分片值仅用于完整性校验，不能参与动态表名。
	query := userDBSession(db).
		Table(TableNameUserIdentityUsername+" AS ui").
		Select([]string{
			"ui.identity_value",
			"ui.user_shard_no AS identity_shard_no",
			"u.id AS main_id",
			"u.shard_no",
			"u.username",
			"u.nickname",
			"u.password_hash",
			"u.email_ciphertext",
			"u.email_hash",
			"u.email_masked",
			"u.email_key_version",
			"u.phone_ciphertext",
			"u.phone_hash",
			"u.phone_masked",
			"u.phone_key_version",
			"u.avatar",
			"u.status",
			"u.auth_version",
			"u.last_login_at",
			"u.last_login_ip",
			"u.created_at",
			"u.updated_at",
		}).
		Joins("LEFT JOIN ? AS u ON u.id = ui.user_id AND u.shard_no = ?", clause.Table{Name: tableName}, shardNo).
		Where("ui.user_id = ?", id).
		Take(&row)
	if errors.Is(query.Error, gorm.ErrRecordNotFound) {
		return nil, errors.Wrapf(ErrUserIdentityMissing, "user_id=%d type=%s", id, UserIdentityTypeUsername)
	}
	if query.Error != nil {
		return nil, errors.Wrapf(query.Error, "User.FindByID 联查用户身份和主表失败 user_id=%d table=%s", id, tableName)
	}
	// 先校验身份目录再判断主表命中，避免损坏目录被误报为普通用户缺失。
	identity := &UserIdentity{
		IdentityType:  UserIdentityTypeUsername,
		IdentityValue: row.IdentityValue,
		UserID:        id,
		UserShardNo:   row.IdentityShardNo,
	}
	if err := validateUserIdentityRoute(identity); err != nil {
		return nil, errors.Tag(err)
	}
	if row.MainID == nil {
		return nil, errors.Errorf("用户身份索引存在但主表记录缺失 user_id=%d table=%s", id, tableName)
	}
	row.User.ID = *row.MainID
	return &row.User, nil
}

// CreateUserWithIdentities 在同一事务写入业务用户及其基础登录身份索引。
func CreateUserWithIdentities(db *gorm.DB, user *User, routeShardCount int, privacySecret string, omitColumns ...string) error {
	if user == nil {
		return errors.New("User.Create 用户为空")
	}
	if db == nil {
		return errors.New("User.Create 数据库为空")
	}
	// 账号规范值在事务前固定，避免无效输入占用数据库连接。
	normalizeUserProfile(user)
	if user.Username == "" {
		return errors.New("User.Create 用户名为空")
	}
	// 联系方式密文和查询哈希在事务前生成，随机数读取不会拉长事务。
	if err := ProtectUserContacts(user, privacySecret); err != nil {
		return errors.Tag(err)
	}
	// 物理表和身份索引在事务前确定，事务内只执行已准备的写入。
	if err := validateUserShardNo(user); err != nil {
		return errors.Tag(err)
	}
	tableName, err := UserPhysicalTableName(user.ShardNo, routeShardCount)
	if err != nil {
		return errors.Tag(err)
	}
	identities, err := userProfileIdentities(user)
	if err != nil {
		return errors.Tag(err)
	}
	// 写入视图覆盖 GORM 的默认值标签，显式禁用（0）不能被改成启用（1）。
	row := struct {
		*User      // 复用实体字段及时间戳回填，不复制整份用户数据。
		Status int `gorm:"column:status"` // 保留调用方确定的状态，包括零值。
	}{User: user, Status: user.Status}
	// 主表与登录身份索引必须同时成功，任一写入失败即整体回滚。
	return db.Transaction(func(tx *gorm.DB) error {
		for index := range identities {
			if err := createUserIdentity(tx, &identities[index]); err != nil {
				return errors.Tag(err)
			}
		}
		query := tx.Table(tableName)
		if len(omitColumns) > 0 {
			query = query.Omit(omitColumns...)
		}
		return errors.Tag(query.Create(&row).Error)
	})
}

// UpdateUser 按当前路由配置和主键更新业务用户可变字段。
func UpdateUser(db *gorm.DB, id int64, updates map[string]any, routeShardCount int) error {
	if id <= 0 || len(updates) == 0 {
		return nil
	}
	updates = safeUserUpdates(updates)
	if len(updates) == 0 {
		return nil
	}
	// 用户表由 ID 固定桶直接路由，避免登录链路重复查询身份目录。
	tableName, err := UserPhysicalTableName(idgen.ShardNo(id), routeShardCount)
	if err != nil {
		return errors.Tag(err)
	}
	// 单表资料更新由单条 SQL 保证原子性，关闭 GORM 默认事务减少协议往返。
	result := userDBSession(db).Session(&gorm.Session{SkipDefaultTransaction: true}).
		Model(&User{}).Table(tableName).
		Where("shard_no = ? AND id = ?", idgen.ShardNo(id), id).
		Updates(updates)
	if result.Error != nil {
		return errors.Tag(result.Error)
	}
	if result.RowsAffected > 0 {
		return nil
	}
	// MySQL 在字段值未变时也可能返回 0；只在该稀有分支回查，区分并发删除和幂等更新。
	row, err := findUserByIDInTable(db, tableName, id)
	if err != nil {
		return errors.Tag(err)
	}
	if row == nil {
		return errors.Errorf("用户更新未命中主表记录 user_id=%d table=%s", id, tableName)
	}
	return nil
}

// UpdateUserProfileWithIdentities 更新用户资料并同步邮箱、手机号登录身份。
func UpdateUserProfileWithIdentities(db *gorm.DB, id int64, updates map[string]any, privacySecret string, routeShardCount int) error {
	if id <= 0 || len(updates) == 0 {
		return nil
	}
	var err error
	updates, err = ProtectUserProfileUpdates(updates, privacySecret)
	if err != nil {
		return errors.Tag(err)
	}
	updates = safeUserUpdates(updates)
	if len(updates) == 0 {
		return nil
	}
	identity, err := FindUserIdentityByUserIDAndType(db, id, UserIdentityTypeUsername, UserIdentityProviderLocal)
	if err != nil {
		return errors.Tag(err)
	}
	if identity == nil {
		return errors.Wrapf(ErrUserIdentityMissing, "user_id=%d type=%s", id, UserIdentityTypeUsername)
	}
	tableName, err := identity.UserTableName(routeShardCount)
	if err != nil {
		return errors.Tag(err)
	}
	emailHash, emailChanged := userContactHashUpdate(updates, "email_hash")
	phoneHash, phoneChanged := userContactHashUpdate(updates, "phone_hash")
	identityChanged := emailChanged || phoneChanged
	// updateProfile 复用同一写入路径；只有联系身份变化时，外层才提供跨表事务。
	updateProfile := func(writeDB *gorm.DB) error {
		result := userDBSession(writeDB).Session(&gorm.Session{SkipDefaultTransaction: true}).
			Model(&User{}).Table(tableName).
			Where("shard_no = ? AND id = ?", identity.UserShardNo, id).
			Updates(updates)
		if result.Error != nil {
			return errors.Tag(result.Error)
		}
		if result.RowsAffected == 0 {
			// MySQL 可能把同值更新计为零行；只在该分支回查，区分幂等更新和主表缺失。
			row, err := findUserByIDInTable(writeDB, tableName, id)
			if err != nil {
				return errors.Tag(err)
			}
			if row == nil {
				return errors.Errorf("用户资料更新未命中主表记录 user_id=%d table=%s", id, tableName)
			}
		}
		if !identityChanged {
			return nil
		}
		user := &User{ID: id, ShardNo: identity.UserShardNo}
		if emailChanged {
			if err := syncUserContactIdentity(writeDB, user, UserIdentityTypeEmail, emailHash); err != nil {
				return errors.Tag(err)
			}
		}
		if phoneChanged {
			return errors.Tag(syncUserContactIdentity(writeDB, user, UserIdentityTypePhone, phoneHash))
		}
		return nil
	}
	if !identityChanged {
		// 单表资料只需一条原子 UPDATE，关闭 GORM 默认事务可省去 BEGIN/COMMIT 往返。
		return updateProfile(db)
	}
	// 联系方式与登录索引共同提交，唯一冲突时不能只留下主表的新值。
	return db.Transaction(updateProfile)
}

// FindUserIdentity 根据身份类型、提供方和身份值查询索引；未命中时返回 nil。
func FindUserIdentity(db *gorm.DB, identityType string, provider string, identityValue string, privacySecret string) (*UserIdentity, error) {
	if identityType == "" || strings.TrimSpace(identityValue) == "" {
		return nil, nil
	}
	// 查询与写入共用身份规范值，避免大小写或首尾空白绕开同一唯一索引。
	identityType, provider, identityValue, err := NormalizeUserIdentity(identityType, provider, identityValue)
	if err != nil {
		return nil, errors.Tag(err)
	}
	tableName, err := UserIdentityTableName(identityType)
	if err != nil {
		return nil, errors.Tag(err)
	}
	var row UserIdentity
	query, err := userIdentityLookupQuery(userDBSession(db).Table(tableName), identityType, provider, identityValue, privacySecret)
	if err != nil {
		return nil, errors.Tag(err)
	}
	if err := query.First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, errors.Wrapf(err, "UserIdentity.Find 查询用户身份 type=%s provider=%s 失败", identityType, provider)
	}
	// 身份类型不落库，由所选物理表恢复，供后续用户分片校验使用。
	row.IdentityType = identityType
	return &row, nil
}

// FindUserIdentityByUserIDAndType 根据用户 ID 和身份类型在对应身份表查询索引。
func FindUserIdentityByUserIDAndType(db *gorm.DB, userID int64, identityType string, provider string) (*UserIdentity, error) {
	if userID <= 0 {
		return nil, nil
	}
	identityType, provider, err := validateUserIdentityTypeProvider(identityType, provider)
	if err != nil {
		return nil, errors.Tag(err)
	}
	tableName, err := UserIdentityTableName(identityType)
	if err != nil {
		return nil, errors.Tag(err)
	}
	var row UserIdentity
	query := userDBSession(db).Table(tableName).Where("user_id = ?", userID)
	// 仅三方身份表持久化 provider，同一用户的不同提供方不能混查。
	if identityType == UserIdentityTypeOAuth {
		query = query.Where("provider = ?", provider)
	}
	if err := query.First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, errors.Wrapf(err, "UserIdentity.FindByUserID 查询用户身份 user_id=%d type=%s provider=%s 失败", userID, identityType, provider)
	}
	// 与按身份值查找保持一致，补回由物理表决定的非持久化类型。
	row.IdentityType = identityType
	return &row, nil
}

// FindUserByIdentityRow 根据身份索引固定桶和当前配置读取用户表。
func FindUserByIdentityRow(db *gorm.DB, identity *UserIdentity, routeShardCount int) (*User, error) {
	if identity == nil {
		return nil, nil
	}
	tableName, err := identity.UserTableName(routeShardCount)
	if err != nil {
		return nil, errors.Tag(err)
	}
	row, err := findUserByIDInTable(db, tableName, identity.UserID)
	if err != nil {
		return nil, errors.Tag(err)
	}
	if row == nil {
		return nil, errors.Errorf("用户身份索引存在但主表记录缺失 user_id=%d table=%s", identity.UserID, tableName)
	}
	return row, nil
}

// userIdentityLookupQuery 根据身份表结构构造唯一索引查询。
func userIdentityLookupQuery(db *gorm.DB, identityType string, provider string, identityValue string, privacySecret string) (*gorm.DB, error) {
	switch identityType {
	case UserIdentityTypeUsername:
		return db.Where("identity_value = ?", identityValue), nil
	case UserIdentityTypeEmail, UserIdentityTypePhone:
		identityHash, err := UserContactIdentityHash(identityType, identityValue, privacySecret)
		if err != nil {
			return nil, errors.Tag(err)
		}
		return db.Where("identity_hash = ?", identityHash), nil
	case UserIdentityTypeOAuth:
		return db.Where("provider = ? AND identity_value = ?", provider, identityValue), nil
	default:
		return nil, errors.Errorf("不支持的用户登录身份类型[%s]", identityType)
	}
}

// createUserIdentity 按身份表字段差异写入索引，避免本地身份表出现空 provider 列。
func createUserIdentity(db *gorm.DB, identity *UserIdentity) error {
	if identity == nil {
		return errors.New("用户身份索引为空")
	}
	tableName, err := identity.IdentityTableName()
	if err != nil {
		return errors.Tag(err)
	}
	query := userDBSession(db).Table(tableName)
	switch identity.IdentityType {
	case UserIdentityTypeUsername:
		query = query.Select("identity_value", "user_id", "user_shard_no")
	case UserIdentityTypeEmail, UserIdentityTypePhone:
		query = query.Select("identity_hash", "user_id", "user_shard_no")
	case UserIdentityTypeOAuth:
		query = query.Select("provider", "identity_value", "user_id", "user_shard_no")
	default:
		return errors.Errorf("不支持的用户登录身份类型[%s]", identity.IdentityType)
	}
	return errors.Tag(query.Create(identity).Error)
}

// updateUserIdentity 按身份表结构更新可变身份值和固定逻辑桶。
func updateUserIdentity(db *gorm.DB, tableName string, id uint64, next *UserIdentity) error {
	if strings.TrimSpace(tableName) == "" || id == 0 || next == nil {
		return nil
	}
	updates := map[string]any{
		"user_shard_no": next.UserShardNo,
		"updated_at":    time.Now(),
	}
	switch next.IdentityType {
	case UserIdentityTypeEmail, UserIdentityTypePhone:
		updates["identity_hash"] = next.IdentityHash
	case UserIdentityTypeUsername, UserIdentityTypeOAuth:
		updates["identity_value"] = next.IdentityValue
	}
	return errors.Tag(userDBSession(db).Table(tableName).Where("id = ?", id).Updates(updates).Error)
}

// findUserByIDInTable 在指定用户表中按固定桶和 ID 查询用户，未命中返回 nil。
func findUserByIDInTable(db *gorm.DB, tableName string, id int64) (*User, error) {
	var row User
	if err := userDBSession(db).Table(tableName).Where("shard_no = ? AND id = ?", idgen.ShardNo(id), id).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil
		}
		return nil, errors.Wrapf(err, "User.FindByID 查询用户 ID[%d]失败", id)
	}
	return &row, nil
}

// normalizeUserProfile 归一化用户资料中的登录身份字段。
func normalizeUserProfile(user *User) {
	if user == nil {
		return
	}
	if user.AuthVersion == 0 {
		user.AuthVersion = 1
	}
	user.Username = strings.TrimSpace(user.Username)
	user.Email = strings.ToLower(strings.TrimSpace(user.Email))
	user.Phone = strings.TrimSpace(user.Phone)
}

// userProfileIdentities 生成用户资料对应的基础登录身份索引。
func userProfileIdentities(user *User) ([]UserIdentity, error) {
	items := make([]UserIdentity, 0, 3)
	usernameIdentity, err := newUserIdentity(user, UserIdentityTypeUsername, UserIdentityProviderLocal, user.Username, "")
	if err != nil {
		return nil, errors.Tag(err)
	}
	items = append(items, *usernameIdentity)
	if strings.TrimSpace(user.EmailHash) != "" {
		emailIdentity, err := newUserIdentity(user, UserIdentityTypeEmail, UserIdentityProviderLocal, "", user.EmailHash)
		if err != nil {
			return nil, errors.Tag(err)
		}
		items = append(items, *emailIdentity)
	}
	if strings.TrimSpace(user.PhoneHash) != "" {
		phoneIdentity, err := newUserIdentity(user, UserIdentityTypePhone, UserIdentityProviderLocal, "", user.PhoneHash)
		if err != nil {
			return nil, errors.Tag(err)
		}
		items = append(items, *phoneIdentity)
	}
	return items, nil
}

// syncUserContactIdentity 按资料字段新增、更新或删除单个联系身份。
func syncUserContactIdentity(db *gorm.DB, user *User, identityType string, identityHash string) error {
	identityType, provider, err := validateUserIdentityTypeProvider(identityType, UserIdentityProviderLocal)
	if err != nil {
		return errors.Tag(err)
	}
	exists, err := FindUserIdentityByUserIDAndType(db, user.ID, identityType, provider)
	if err != nil {
		return errors.Tag(err)
	}
	if strings.TrimSpace(identityHash) == "" {
		// 空哈希表示用户清空联系方式，身份索引必须与主表字段在同一事务内删除。
		if exists == nil {
			return nil
		}
		tableName, err := exists.IdentityTableName()
		if err != nil {
			return errors.Tag(err)
		}
		return errors.Tag(userDBSession(db).Table(tableName).Where("id = ?", exists.ID).Delete(&UserIdentity{}).Error)
	}
	next, err := newUserIdentity(user, identityType, provider, "", identityHash)
	if err != nil {
		return errors.Tag(err)
	}
	if exists == nil {
		return errors.Tag(createUserIdentity(db, next))
	}
	existsTableName, err := exists.IdentityTableName()
	if err != nil {
		return errors.Tag(err)
	}
	if exists.IdentityHash == next.IdentityHash && exists.UserShardNo == next.UserShardNo {
		// 同值资料更新不触碰唯一索引，减少无效写入和锁竞争。
		return nil
	}
	return errors.Tag(updateUserIdentity(db, existsTableName, exists.ID, next))
}

// newUserIdentity 构造带固定逻辑桶的用户身份索引。
func newUserIdentity(user *User, identityType string, provider string, identityValue string, identityHash string) (*UserIdentity, error) {
	if user == nil {
		return nil, errors.New("用户为空")
	}
	if err := validateUserShardNo(user); err != nil {
		return nil, errors.Tag(err)
	}
	identityType, provider, err := validateUserIdentityTypeProvider(identityType, provider)
	if err != nil {
		return nil, errors.Tag(err)
	}
	if identityType == UserIdentityTypeUsername || identityType == UserIdentityTypeOAuth {
		_, _, identityValue, err = NormalizeUserIdentity(identityType, provider, identityValue)
		if err != nil {
			return nil, errors.Tag(err)
		}
	}
	identityHash = strings.TrimSpace(identityHash)
	if identityType == UserIdentityTypeEmail || identityType == UserIdentityTypePhone {
		// 联系身份沿用资料派生出的查询哈希，不将邮箱或手机号明文写入目录。
		if len(identityHash) != userContactHashHexSize {
			return nil, errors.Errorf("用户%s身份哈希长度必须为%d", identityType, userContactHashHexSize)
		}
		identityValue = ""
	}
	return &UserIdentity{
		IdentityType:  identityType,
		Provider:      provider,
		IdentityValue: identityValue,
		IdentityHash:  identityHash,
		UserID:        user.ID,
		UserShardNo:   user.ShardNo,
	}, nil
}

// validateUserIdentityTypeProvider 校验身份类型和三方提供方的规范值。
func validateUserIdentityTypeProvider(identityType string, provider string) (string, string, error) {
	switch identityType {
	case UserIdentityTypeUsername, UserIdentityTypeEmail, UserIdentityTypePhone:
		if provider != UserIdentityProviderLocal {
			return "", "", errors.Errorf("本地用户登录身份 provider 必须为空")
		}
		return identityType, UserIdentityProviderLocal, nil
	case UserIdentityTypeOAuth:
		if provider == "" {
			return "", "", errors.New("三方登录身份 provider 不能为空")
		}
		if provider != strings.TrimSpace(provider) || provider != strings.ToLower(provider) {
			return "", "", errors.New("三方登录身份 provider 必须使用无首尾空白的小写规范值")
		}
		return identityType, provider, nil
	default:
		return "", "", errors.Errorf("不支持的用户登录身份类型[%s]", identityType)
	}
}

// userContactHashUpdate 读取精确字段名对应的联系方式哈希。
func userContactHashUpdate(updates map[string]any, field string) (string, bool) {
	value, ok := updates[field]
	if !ok {
		return "", false
	}
	return fmt.Sprint(value), true
}

// validateUserIdentityRoute 校验身份索引中的身份类型、用户 ID 与逻辑分片一致。
func validateUserIdentityRoute(identity *UserIdentity) error {
	if identity.UserID <= 0 {
		return errors.New("用户身份索引 user_id 必须大于 0")
	}
	// 身份类型先与 provider 联合校验，避免同一值落入多种索引语义。
	identityType, _, err := validateUserIdentityTypeProvider(identity.IdentityType, identity.Provider)
	if err != nil {
		return errors.Tag(err)
	}
	switch identityType {
	case UserIdentityTypeEmail, UserIdentityTypePhone:
		// 联系方式只保存定长小写哈希，明文索引列必须为空。
		if identity.IdentityValue != "" {
			return errors.New("邮箱或手机号身份索引 identity_value 必须为空")
		}
		if len(identity.IdentityHash) != userContactHashHexSize ||
			identity.IdentityHash != strings.TrimSpace(identity.IdentityHash) ||
			identity.IdentityHash != strings.ToLower(identity.IdentityHash) {
			return errors.Errorf("用户身份索引 identity_hash 必须为 %d 位小写十六进制", userContactHashHexSize)
		}
		if _, err := hex.DecodeString(identity.IdentityHash); err != nil {
			return errors.Wrap(err, "用户身份索引 identity_hash 不是十六进制")
		}
	case UserIdentityTypeUsername, UserIdentityTypeOAuth:
		// 用户名和三方身份保存规范明文值，不允许同时写哈希列。
		if identity.IdentityHash != "" {
			return errors.New("账号或三方身份索引 identity_hash 必须为空")
		}
		_, _, normalizedValue, normalizeErr := NormalizeUserIdentity(identity.IdentityType, identity.Provider, identity.IdentityValue)
		if normalizeErr != nil || normalizedValue != identity.IdentityValue {
			return errors.New("用户身份索引 identity_value 必须使用规范值")
		}
	}
	// 分片号必须由用户 ID 唯一推导，禁止调用方自行指定错误路由。
	wantShardNo := idgen.ShardNo(identity.UserID)
	if identity.UserShardNo != wantShardNo {
		return errors.Errorf("用户身份索引 user_shard_no=%d 与 user_id=%d 计算值 %d 不一致", identity.UserShardNo, identity.UserID, wantShardNo)
	}
	return nil
}

// validateUserShardNo 校验用户主表记录的 ID 与逻辑分片一致。
func validateUserShardNo(user *User) error {
	if user.ID <= 0 {
		return errors.New("User.Create 用户 ID 必须大于 0")
	}
	wantShardNo := idgen.ShardNo(user.ID)
	if user.ShardNo != wantShardNo {
		return errors.Errorf("User.Create shard_no=%d 与用户 ID[%d]计算值 %d 不一致", user.ShardNo, user.ID, wantShardNo)
	}
	return nil
}

// safeUserUpdates 只保留当前模型明确允许的可变列，敏感状态必须走认证版本联动专用流程。
func safeUserUpdates(updates map[string]any) map[string]any {
	filtered := make(map[string]any, len(updates))
	for key, value := range updates {
		switch key {
		case "nickname", "avatar", "last_login_at", "last_login_ip", "updated_at":
			filtered[key] = value
		case "email_ciphertext", "email_hash", "email_masked", "email_key_version",
			"phone_ciphertext", "phone_hash", "phone_masked", "phone_key_version":
			filtered[key] = fmt.Sprint(value)
		}
	}
	return filtered
}

// userDBSession 复制调用方提供的干净 base/tx 会话，并保留 dbresolver 读写路由与事务上下文。
func userDBSession(db *gorm.DB) *gorm.DB {
	return db.Session(&gorm.Session{})
}
