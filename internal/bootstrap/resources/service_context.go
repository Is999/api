package resources

import (
	"context"
	"sort"

	"api/internal/bootstrap/configload"
	"api/internal/config"
	"api/internal/infra/loggerx"
	mysqlx "api/internal/infra/mysql"
	"api/internal/infra/redisx"
	"api/internal/infra/tracing"
	cachelogic "api/internal/logic/cache"
	"api/internal/security"
	"api/internal/svc"

	"github.com/Is999/go-utils/errors"
	tablecache "github.com/Is999/table-cache"
	"gorm.io/gorm"
)

// buildResources 聚合 BuildServiceContext 启动过程中已成功初始化、但尚未交给 App 托管的资源。
type buildResources struct {
	svc.Dependencies                             // ServiceContext 可直接复用的依赖集合
	Shutdown         func(context.Context) error // tracing 等基础设施关闭钩子，最后释放
}

// BuildServiceContext 使用配置加载阶段生成的密钥快照初始化基础设施。
func BuildServiceContext(ctx context.Context, c config.Config, version string, securityKeys *security.KeyRegistry) (*svc.ServiceContext, func(context.Context) error, error) {
	// 安全链启用后必须显式注入同轮配置编译结果，禁止以 nil 注册表降级启动。
	if (c.Security.SecretKey.SignStatus == 1 || c.Security.SecretKey.CryptoStatus == 1) && securityKeys == nil {
		return nil, nil, errors.Errorf("安全链已启用但密钥注册表未注入")
	}
	if securityKeys != nil {
		// 注入快照必须与同轮配置的 AppID、版本路由和开关一致。
		route, err := securityKeys.Route(c.AppID)
		secretCfg := c.Security.SecretKey
		if err != nil || route.StableVersion != secretCfg.StableVersion ||
			route.GrayVersion != secretCfg.GrayVersion || route.GrayPercent != secretCfg.GrayPercent ||
			route.GraySalt != secretCfg.GraySalt || route.SignEnabled != (secretCfg.SignStatus == 1) ||
			route.CryptoEnabled != (secretCfg.CryptoStatus == 1) {
			return nil, nil, errors.Errorf("安全密钥注册表与当前配置不一致")
		}
	}
	// 正式日志先于外部资源初始化，确保后续启动错误进入统一日志通道。
	if err := loggerx.Setup(c); err != nil {
		return nil, nil, errors.Tag(err)
	}
	// tracing 在数据库和 Redis 之前启动，使初始化探测使用已注册的 provider。
	shutdown, err := tracing.Setup(ctx, c.Observability)
	if err != nil {
		return nil, nil, errors.Tag(err)
	}
	resources := buildResources{
		Dependencies: svc.Dependencies{SecurityKeys: securityKeys},
		Shutdown:     shutdown,
	}

	siteDBs, err := buildSiteDatabases(ctx, c)
	resources.SiteDBs = siteDBs
	if err != nil {
		// 命名库可能只创建了一部分，失败时连同 tracing 一并回收。
		_ = closeBuildResources(context.Background(), resources)
		return nil, nil, errors.Tag(err)
	}
	rdb, err := redisx.New(ctx, c.Redis, c.Observability)
	if err != nil {
		_ = closeBuildResources(context.Background(), resources)
		return nil, nil, errors.Tag(err)
	}
	resources.Rds = rdb

	tableCacheMetrics, err := tablecache.NewPrometheusMetrics(
		tablecache.WithPrometheusSubsystem(cachelogic.TableCacheMetricsSubsystem),
	)
	if err != nil {
		_ = closeBuildResources(context.Background(), resources)
		return nil, nil, errors.Wrap(err, "初始化表缓存运行指标失败")
	}
	resources.TableCacheMetrics = tableCacheMetrics

	// Snowflake 租约最后创建，关闭时才能先释放租约再断开 Redis。
	snowflakeLease, err := configload.ConfigureSnowflakeWorker(ctx, c.Snowflake, rdb)
	if err != nil {
		_ = closeBuildResources(context.Background(), resources)
		return nil, nil, errors.Wrap(err, "配置雪花 ID worker 失败")
	}
	resources.SnowflakeLease = snowflakeLease

	// 全部依赖成功后再构造 ServiceContext，禁止部分资源逃逸到请求链。
	svcCtx := svc.NewServiceContext(c, version, resources.Dependencies)
	return svcCtx, shutdown, nil
}

// closeBuildResources 回收 BuildServiceContext 已经创建但尚未交给 App 托管的资源。
func closeBuildResources(ctx context.Context, resources buildResources) error {
	var firstErr error
	// 继续回收全部资源，只返回第一处错误。
	recordErr := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = errors.Tag(err)
		}
	}
	if resources.SnowflakeLease != nil {
		// 租约先关闭，阻止后台续租访问已断开的 Redis。
		recordErr(resources.SnowflakeLease.Close(ctx))
	}
	if resources.Rds != nil {
		// Redis 使用方停止后再断开连接。
		recordErr(resources.Rds.Close())
	}
	recordErr(closeSiteDatabases(resources.SiteDBs))
	if resources.Shutdown != nil {
		// tracing 最后关闭，保留资源清理阶段的观测能力。
		recordErr(resources.Shutdown(ctx))
	}
	return errors.Tag(firstErr)
}

// CloseServiceContextResources 释放 ServiceContext 托管的外部资源。
func CloseServiceContextResources(ctx context.Context, svcCtx *svc.ServiceContext) error {
	if ctx == nil {
		ctx = context.Background()
	}
	var firstErr error
	// 继续关闭全部资源，只返回第一处错误。
	recordErr := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = errors.Tag(err)
		}
	}
	if svcCtx == nil {
		return nil
	}
	if registry := svcCtx.ComponentRegistry(); registry != nil && len(registry.Items()) > 0 {
		// 注册表按组件依赖逆序关闭，避免重复释放同一资源。
		recordErr(registry.Close(ctx))
		return errors.Tag(firstErr)
	}
	// 未注入注册表的最小上下文仍按生产依赖顺序关闭。
	if svcCtx.Collector != nil {
		recordErr(svcCtx.Collector.Close(ctx))
	}
	if svcCtx.SnowflakeLease != nil {
		// 雪花租约必须在 Redis 连接前停止。
		recordErr(svcCtx.SnowflakeLease.Close(ctx))
	}
	if svcCtx.Rds != nil {
		recordErr(svcCtx.Rds.Close())
	}
	recordErr(closeSiteDatabases(svcCtx.SiteDBs))
	return firstErr
}

// buildSiteDatabases 初始化默认主库和命名扩展库连接。
func buildSiteDatabases(ctx context.Context, c config.Config) (svc.SiteDatabases, error) {
	if !hasMySQLDataSource(c.MySQL) {
		return svc.SiteDatabases{}, errors.Errorf("缺少 mysql.write_data_source 配置")
	}
	// 主库成功后才继续建立可选命名库。
	mainDB, err := openSiteDatabase(ctx, "mysql", c.MySQL, c.Observability)
	if err != nil {
		return svc.SiteDatabases{}, errors.Tag(err)
	}
	dbs := svc.SiteDatabases{
		MainDB:   mainDB,
		NamedDBs: make(map[svc.DBName]*gorm.DB),
	}
	names := make([]string, 0, len(c.SiteMySQL))
	for name := range c.SiteMySQL {
		names = append(names, name)
	}
	// 固定名称顺序，保证失败位置和清理范围可复现。
	sort.Strings(names)
	for _, name := range names {
		dbCfg := c.SiteMySQL[name]
		if !hasMySQLDataSource(dbCfg) {
			continue
		}
		dbName := svc.DBName(name)
		db, err := openSiteDatabase(ctx, "site_mysql."+string(dbName), dbCfg, c.Observability)
		if err != nil {
			// 任一命名库失败立即回收本轮已建连接池。
			_ = closeSiteDatabases(dbs)
			return svc.SiteDatabases{}, errors.Tag(err)
		}
		dbs.NamedDBs[dbName] = db
	}
	return dbs, nil
}

// openSiteDatabase 校验并打开单个站点数据库连接。
func openSiteDatabase(ctx context.Context, name string, cfg config.MySQLConfig, obs config.ObservabilityConfig) (*gorm.DB, error) {
	if cfg.WriteDataSource == "" {
		return nil, errors.Errorf("缺少 %s.write_data_source 配置", name)
	}
	db, err := mysqlx.New(ctx, cfg, obs)
	if err != nil {
		return nil, errors.Wrapf(err, "打开 MySQL[%s]失败", name)
	}
	return db, nil
}

// hasMySQLDataSource 判断 MySQL 配置是否包含写库 DSN。
func hasMySQLDataSource(cfg config.MySQLConfig) bool {
	return cfg.WriteDataSource != ""
}

// closeSiteDatabases 去重关闭站点数据库连接，避免同一连接池重复关闭。
func closeSiteDatabases(siteDBs svc.SiteDatabases) error {
	var firstErr error
	recordErr := func(err error) {
		if err != nil && firstErr == nil {
			firstErr = errors.Tag(err)
		}
	}
	seen := make(map[*gorm.DB]struct{}, 4)
	closeOne := func(name string, db *gorm.DB) {
		if db == nil {
			return
		}
		if _, ok := seen[db]; ok {
			return
		}
		seen[db] = struct{}{}
		if err := mysqlx.Close(db); err != nil {
			recordErr(errors.Wrapf(err, "关闭 MySQL[%s]连接池失败", name))
		}
	}
	closeOne("mysql", siteDBs.MainDB)
	for name, db := range siteDBs.NamedDBs {
		closeOne("site_mysql."+string(name), db)
	}
	return errors.Tag(firstErr)
}
