package model

import "time"

// 系统配置表名和类型枚举。
const (
	// TableNameSysConfig 表示系统配置表名，前台 API 复用后台配置字典命名。
	TableNameSysConfig = "sys_config"

	// SysConfigTypeGroup 表示分组标题配置。
	SysConfigTypeGroup = 0
	// SysConfigTypeObject 表示 Object 配置值。
	SysConfigTypeObject = 1
	// SysConfigTypeArray 表示 Array 配置值。
	SysConfigTypeArray = 2
	// SysConfigTypeString 表示 String 配置值。
	SysConfigTypeString = 3
	// SysConfigTypeInteger 表示 Integer 配置值。
	SysConfigTypeInteger = 4
	// SysConfigTypeFloat 表示 Float 配置值。
	SysConfigTypeFloat = 5
	// SysConfigTypeBoolean 表示数据库中以 0/1 保存的 Boolean 配置值。
	SysConfigTypeBoolean = 6
)

// SysConfig 映射后台维护的配置字典；Value 和 Example 是 JSON 文本，API 按 Type 解析，不能直接当字符串业务值。
type SysConfig struct {
	ID        int       `gorm:"column:id;type:int unsigned;primaryKey;autoIncrement:true;comment:主键" json:"id"`                                                                    // 主键
	UUID      string    `gorm:"column:uuid;type:varchar(100);not null;uniqueIndex:uk_uuid,priority:1;comment:配置唯一标识,命名规则(驼峰)：项目名+key" json:"uuid"`                                 // 配置唯一标识
	Title     string    `gorm:"column:title;type:varchar(100);not null;default:'';comment:配置标题" json:"title"`                                                                      // 配置标题
	Type      int       `gorm:"column:type;type:tinyint unsigned;not null;default:1;comment:展示和校验类型：0 分组; 1 Object; 2 Array; 3 String; 4 Integer; 5 Float; 6 Boolean" json:"type"` // 展示和校验类型
	Value     string    `gorm:"column:value;type:json;not null;comment:配置值(JSON 格式，可为 string/number/bool/array/object)" json:"value"`                                              // 配置值
	Example   string    `gorm:"column:example;type:json;not null;comment:配置示例" json:"example"`                                                                                     // 配置示例
	Remark    string    `gorm:"column:remark;type:varchar(255);not null;default:'';comment:备注" json:"remark"`                                                                      // 备注
	Page      string    `gorm:"column:page;type:varchar(200);not null;default:'';comment:配置项所属页面路径" json:"page"`                                                                   // 所属页面路径
	Pid       int       `gorm:"column:pid;type:int unsigned;not null;default:0;comment:上级ID" json:"pid"`                                                                           // 上级 ID
	Pids      string    `gorm:"column:pids;type:varchar(255);not null;default:'';comment:上级ID族谱" json:"pids"`                                                                      // 上级 ID 族谱
	Version   int       `gorm:"column:version;type:int unsigned;not null;default:0;comment:版本号" json:"version"`                                                                    // 版本号
	CreatedAt time.Time `gorm:"column:created_at;type:datetime;not null;default:CURRENT_TIMESTAMP;comment:创建时间" json:"created_at"`                                                 // 创建时间
	UpdatedAt time.Time `gorm:"column:updated_at;type:datetime;default:CURRENT_TIMESTAMP;comment:更新时间" json:"updated_at"`                                                          // 更新时间
}

// TableName 返回系统配置表名。
func (*SysConfig) TableName() string {
	return TableNameSysConfig
}
