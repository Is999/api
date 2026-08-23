package svc

import (
	"gorm.io/gorm"
	"gorm.io/plugin/dbresolver"
)

// DBName 表示可路由的数据库名称。
type DBName string

// 数据库名称枚举。
const (
	// DatabaseMain 表示默认主库。
	DatabaseMain DBName = "main"
)

// DB 根据数据库名称返回默认连接。
func (s *ServiceContext) DB(database DBName) *gorm.DB {
	if s == nil {
		return nil
	}
	return s.SiteDBs.Lookup(database)
}

// ReadDB 指定读库路由；未配置副本时使用主库，不代表数据库账号只有只读权限。
func (s *ServiceContext) ReadDB(database DBName) *gorm.DB {
	if s == nil {
		return nil
	}
	return readDB(s.SiteDBs.Lookup(database))
}

// WriteDB 指定主库路由，用于写操作及写后需要立即读取最新结果的查询。
func (s *ServiceContext) WriteDB(database DBName) *gorm.DB {
	if s == nil {
		return nil
	}
	return writeDB(s.SiteDBs.Lookup(database))
}

// readDB 返回强制走读连接的 GORM 会话。
func readDB(db *gorm.DB) *gorm.DB {
	if db == nil || db.Statement == nil {
		return db
	}
	return db.Clauses(dbresolver.Read)
}

// writeDB 返回强制走写连接的 GORM 会话。
func writeDB(db *gorm.DB) *gorm.DB {
	if db == nil || db.Statement == nil {
		return db
	}
	return db.Clauses(dbresolver.Write)
}
