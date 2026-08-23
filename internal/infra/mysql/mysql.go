package mysqlx

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
	"time"

	"api/internal/config"
	"api/internal/infra/loggerx"

	"github.com/Is999/go-utils/errors"
	drivermysql "github.com/go-sql-driver/mysql"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/plugin/dbresolver"
)

const startupPingTimeout = 5 * time.Second // 单个 MySQL 数据源启动探测上限；读副本数量另有配置硬上限。

// New 创建带统一 GORM 日志器的数据库连接，并在启动阶段完成连通性检查。
func New(ctx context.Context, cfg config.MySQLConfig, obs config.ObservabilityConfig) (*gorm.DB, error) {
	// 所有 DSN 和连接池边界在创建资源前校验，避免半初始化连接泄漏。
	if err := validatePoolConfig(cfg); err != nil {
		return nil, errors.Tag(err)
	}
	writeDSN, readDSNs, err := resolveDataSources(cfg)
	if err != nil {
		return nil, errors.Tag(err)
	}
	if err := checkMySQLDataSources(ctx, writeDSN, readDSNs); err != nil {
		return nil, errors.Tag(err)
	}

	gormCfg := &gorm.Config{
		// 单条写入不额外开启事务；跨表或跨批原子性由业务显式事务保证。
		SkipDefaultTransaction: true,
		Logger:                 loggerx.NewGormLogger(time.Duration(obs.SlowSQLMs) * time.Millisecond),
		DisableAutomaticPing:   true, // 连通性统一走带 deadline 的显式 Ping，避免驱动默认探测无限等待。
	}
	// 先建立写库主连接，读库再通过 dbresolver 一次性注册。
	gdb, err := gorm.Open(mysql8Dialector(writeDSN), gormCfg)
	if err != nil {
		return nil, errors.Tag(err)
	}

	if len(readDSNs) > 0 {
		replicas := make([]gorm.Dialector, 0, len(readDSNs))
		for _, dsn := range readDSNs {
			replicas = append(replicas, mysql8Dialector(dsn))
		}
		resolver := dbresolver.Register(dbresolver.Config{
			Replicas:          replicas,
			Policy:            dbresolver.RandomPolicy{},
			TraceResolverMode: true,
		})
		resolver = resolver.SetMaxOpenConns(cfg.MaxOpenConns)
		resolver = resolver.SetMaxIdleConns(cfg.MaxIdleConns)
		resolver = resolver.SetConnMaxLifetime(time.Duration(cfg.ConnMaxLifetime) * time.Second)
		if err := gdb.Use(resolver); err != nil {
			_ = Close(gdb)
			return nil, errors.Wrap(err, "注册 MySQL 读写分离解析器失败")
		}
	}

	// 主连接池沿用同一上限，并在返回前执行带超时的最终探测。
	sqlDB, err := gdb.DB()
	if err != nil {
		_ = Close(gdb)
		return nil, errors.Tag(err)
	}
	sqlDB.SetMaxOpenConns(cfg.MaxOpenConns)
	sqlDB.SetMaxIdleConns(cfg.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(time.Duration(cfg.ConnMaxLifetime) * time.Second)
	if err := pingMySQLWithStartupTimeout(ctx, func(pingCtx context.Context) error {
		return Ping(pingCtx, gdb)
	}); err != nil {
		_ = Close(gdb)
		return nil, errors.Tag(err)
	}
	if cfg.Debug {
		gdb = gdb.Debug()
	}
	return gdb, nil
}

// mysql8Dialector 沿用初始化资产的 MySQL 8 基线，写库和读库都禁用无 deadline 的自动版本查询。
func mysql8Dialector(dsn string) gorm.Dialector {
	return gormmysql.New(gormmysql.Config{
		DSN:                       dsn,
		SkipInitializeWithVersion: true, // 不探测旧版方言；连通性仍由显式 PingContext 校验。
	})
}

// validatePoolConfig 为直接调用 infra/mysql 的入口保留防线，避免绕过 bootstrap 后创建无限连接池。
func validatePoolConfig(cfg config.MySQLConfig) error {
	if cfg.MaxOpenConns <= 0 || cfg.MaxOpenConns > config.MaxMySQLOpenConns {
		return errors.Errorf("mysql.max_open_conns 必须在 1-%d 之间", config.MaxMySQLOpenConns)
	}
	if cfg.MaxIdleConns < 0 || cfg.MaxIdleConns > cfg.MaxOpenConns {
		return errors.Errorf("mysql.max_idle_conns 必须在 0-max_open_conns 之间")
	}
	if cfg.ConnMaxLifetime <= 0 || cfg.ConnMaxLifetime > config.MaxMySQLConnLifetimeSeconds {
		return errors.Errorf("mysql.conn_max_lifetime 必须在 1-%d 秒之间", config.MaxMySQLConnLifetimeSeconds)
	}
	if len(cfg.ReadDataSources) > config.MaxMySQLReadDataSourceCount {
		return errors.Errorf("mysql.read_data_sources 不能超过 %d 个", config.MaxMySQLReadDataSourceCount)
	}
	return nil
}

// sqlPools 返回 GORM 主库及 dbresolver 创建的全部读副本连接池；重复池按指针去重。
func sqlPools(gdb *gorm.DB) ([]*sql.DB, error) {
	if gdb == nil {
		return nil, errors.Errorf("MySQL GORM 连接为空")
	}
	// 主库和 resolver 可能返回同一底层指针，关闭和探测前统一去重。
	seen := make(map[*sql.DB]struct{}, 4)
	pools := make([]*sql.DB, 0, 4)
	var firstErr error
	appendPool := func(pool gorm.ConnPool) {
		sqlDB, ok := pool.(*sql.DB)
		if !ok || sqlDB == nil {
			if firstErr == nil {
				firstErr = errors.Errorf("MySQL 连接池类型不受支持: %T", pool)
			}
			return
		}
		if _, exists := seen[sqlDB]; exists {
			return
		}
		seen[sqlDB] = struct{}{}
		pools = append(pools, sqlDB)
	}

	// 主库先加入结果，保证错误信息和探测顺序稳定。
	sqlDB, err := gdb.DB()
	if err != nil {
		firstErr = errors.Wrap(err, "获取 MySQL 底层连接池失败")
	} else {
		appendPool(sqlDB)
	}
	// resolver 暴露的读写池全部纳入健康检查和关闭流程。
	if plugin, ok := gdb.Config.Plugins["gorm:db_resolver"].(*dbresolver.DBResolver); ok {
		if err := plugin.Call(func(pool gorm.ConnPool) error {
			appendPool(pool)
			return nil
		}); err != nil && firstErr == nil {
			firstErr = errors.Wrap(err, "遍历 MySQL 读写连接池失败")
		}
	}
	return pools, errors.Tag(firstErr)
}

// Ping 探测 GORM 主库及 dbresolver 的全部读副本；任一连接池不可用即返回失败。
func Ping(ctx context.Context, gdb *gorm.DB) error {
	if ctx == nil {
		ctx = context.Background()
	}
	pools, err := sqlPools(gdb)
	if err != nil {
		return errors.Tag(err)
	}
	for index, pool := range pools {
		if err := pool.PingContext(ctx); err != nil {
			return errors.Wrapf(err, "MySQL 连接池探测失败 pool=%d", index)
		}
	}
	return nil
}

// Close 关闭 GORM 主库及 dbresolver 创建的全部读副本连接池。
func Close(gdb *gorm.DB) error {
	if gdb == nil {
		return nil
	}
	pools, collectErr := sqlPools(gdb)
	firstErr := collectErr
	for _, pool := range pools {
		if err := pool.Close(); err != nil && firstErr == nil {
			firstErr = errors.Wrap(err, "关闭 MySQL 连接池失败")
		}
	}
	return errors.Tag(firstErr)
}

// resolveDataSources 严格校验写库和读库 DSN，错误项必须阻断启动而不是被静默丢弃。
func resolveDataSources(cfg config.MySQLConfig) (string, []string, error) {
	writeDSN := strings.TrimSpace(cfg.WriteDataSource)
	if writeDSN == "" {
		return "", nil, errors.Errorf("缺少 mysql.write_data_source 配置")
	}
	if writeDSN != cfg.WriteDataSource {
		return "", nil, errors.Errorf("mysql.write_data_source 不能包含首尾空白")
	}
	if len(cfg.ReadDataSources) == 0 {
		return writeDSN, nil, nil
	}
	replicas := make([]string, 0, len(cfg.ReadDataSources))
	seen := map[string]struct{}{}
	for index, dsn := range cfg.ReadDataSources {
		trimmed := strings.TrimSpace(dsn)
		if trimmed == "" || trimmed != dsn {
			return "", nil, errors.Errorf("mysql.read_data_sources[%d] 不能为空或包含首尾空白", index)
		}
		if trimmed == writeDSN {
			return "", nil, errors.Errorf("mysql.read_data_sources[%d] 不能与写库重复", index)
		}
		if _, ok := seen[trimmed]; ok {
			return "", nil, errors.Errorf("mysql.read_data_sources[%d] 不能重复", index)
		}
		seen[trimmed] = struct{}{}
		replicas = append(replicas, trimmed)
	}
	return writeDSN, replicas, nil
}

// checkMySQLDataSources 在启动期探测所有 MySQL DSN。
func checkMySQLDataSources(ctx context.Context, writeDSN string, readDSNs []string) error {
	return checkMySQLDataSourcesWithPing(ctx, writeDSN, readDSNs, pingMySQLDataSource)
}

// checkMySQLDataSourcesWithPing 注入探测函数，便于单测覆盖启动探测分支。
func checkMySQLDataSourcesWithPing(ctx context.Context, writeDSN string, readDSNs []string, ping func(context.Context, string, string) error) error {
	if ping == nil {
		return errors.Errorf("MySQL 启动探测函数不能为空")
	}
	if err := ping(ctx, "write_data_source", writeDSN); err != nil {
		return errors.Tag(err)
	}
	for idx, dsn := range readDSNs {
		if err := ping(ctx, fmt.Sprintf("read_data_sources[%d]", idx), dsn); err != nil {
			return errors.Tag(err)
		}
	}
	return nil
}

// pingMySQLDataSource 使用最小连接池探测单个 MySQL DSN。
func pingMySQLDataSource(ctx context.Context, label, dsn string) error {
	if err := validateMySQLDataSourceDatabase(label, dsn); err != nil {
		return errors.Tag(err)
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return errors.Wrapf(err, "打开 MySQL %s 失败", label)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(0)
	if err := pingMySQLWithStartupTimeout(ctx, db.PingContext); err != nil {
		return errors.Wrapf(err, "探测 MySQL %s 失败，数据库不存在或不可达", label)
	}
	return nil
}

// pingMySQLWithStartupTimeout 为单次启动探测创建 deadline；上游更短的取消或截止时间仍然优先。
func pingMySQLWithStartupTimeout(ctx context.Context, ping func(context.Context) error) error {
	if ping == nil {
		return errors.Errorf("MySQL 启动探测函数不能为空")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	pingCtx, cancel := context.WithTimeout(ctx, startupPingTimeout)
	defer cancel()
	return errors.Tag(ping(pingCtx))
}

// validateMySQLDataSourceDatabase 要求 DSN 显式包含库名，避免误连默认库。
func validateMySQLDataSourceDatabase(label, dsn string) error {
	parsed, err := drivermysql.ParseDSN(dsn)
	if err != nil {
		return errors.Wrapf(err, "解析 MySQL %s DSN 失败", label)
	}
	if strings.TrimSpace(parsed.DBName) == "" {
		return errors.Errorf("MySQL %s DSN 必须包含数据库名", label)
	}
	return nil
}
