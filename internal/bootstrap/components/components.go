package components

import (
	"context"
	"sort"

	codes "api/common/codes"
	"api/common/idgen"
	"api/internal/bootstrap/register"
	mysqlx "api/internal/infra/mysql"
	"api/internal/svc"

	"github.com/Is999/go-utils/errors"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

const (
	// nameServiceContext 表示 ServiceContext 为空时的兜底组件。
	nameServiceContext = "service_context"
	// nameMySQL 表示默认主库组件。
	nameMySQL = "mysql"
	// nameRedis 表示默认 Redis 组件。
	nameRedis = "redis"
	// nameSnowflake 表示分布式 ID 生成组件。
	nameSnowflake = "snowflake"
	// nameCollector 表示事件收集器组件。
	nameCollector = "collector"
	// sourceSiteMySQL 表示命名扩展库组件来源。
	sourceSiteMySQL = "site_mysql"
)

// gormDBCloseGuard 避免同一 GORM 连接池被重复关闭。
type gormDBCloseGuard struct {
	closed map[*gorm.DB]struct{} // 已关闭的连接池
}

// buildContext 表示构造默认组件时需要的共享依赖。
type buildContext struct {
	ServiceContext *svc.ServiceContext // 全局服务上下文
	CloseGuard     *gormDBCloseGuard   // MySQL 连接池关闭去重器
}

// Spec 描述一个默认组件来源及其构造逻辑。
type Spec struct {
	register.Spec                                    // 组件注册清单元信息
	build         func(buildContext) []svc.Component // 构造该来源下的实际组件
}

// NewRegistry 构造启动期核心组件注册表。
func NewRegistry(svcCtx *svc.ServiceContext) (*svc.ComponentRegistry, error) {
	if svcCtx == nil {
		return svc.NewComponentRegistry(serviceContextComponent())
	}

	closeGuard := &gormDBCloseGuard{closed: make(map[*gorm.DB]struct{}, 4)}
	items := defaultComponents(buildContext{
		ServiceContext: svcCtx,
		CloseGuard:     closeGuard,
	})
	return svc.NewComponentRegistry(items...)
}

// DefaultSpecs 返回默认组件生命周期来源；正序注册，停机时逆序关闭。
func DefaultSpecs() []Spec {
	return []Spec{
		{
			Spec: register.Spec{
				Name:        nameMySQL,
				File:        "internal/bootstrap/components/components.go",
				Method:      "mysqlComponent / NewRegistry",
				Description: "注册默认主库健康探测和关闭入口",
			},
			build: func(ctx buildContext) []svc.Component {
				if ctx.ServiceContext == nil {
					return nil
				}
				return []svc.Component{mysqlComponent(nameMySQL, ctx.ServiceContext.SiteDBs.MainDB, ctx.CloseGuard)}
			},
		},
		{
			Spec: register.Spec{
				Name:        sourceSiteMySQL,
				File:        "internal/bootstrap/components/components.go",
				Method:      "siteMySQLComponents / NewRegistry",
				Description: "按名称注册扩展库健康探测和关闭入口",
			},
			build: siteMySQLComponents,
		},
		{
			Spec: register.Spec{
				Name:        nameRedis,
				File:        "internal/bootstrap/components/components.go",
				Method:      "redisComponent / NewRegistry",
				Description: "注册默认 Redis 健康探测和关闭入口",
			},
			build: func(ctx buildContext) []svc.Component {
				if ctx.ServiceContext == nil {
					return nil
				}
				return []svc.Component{redisComponent(ctx.ServiceContext.Rds)}
			},
		},
		{
			Spec: register.Spec{
				Name:        nameSnowflake,
				File:        "internal/bootstrap/components/components.go",
				Method:      "snowflakeComponent / NewRegistry",
				Description: "注册雪花 ID 运行资源健康探测和关闭入口",
			},
			build: func(ctx buildContext) []svc.Component {
				if ctx.ServiceContext == nil {
					return nil
				}
				return []svc.Component{snowflakeComponent(ctx.ServiceContext.SnowflakeLease)}
			},
		},
		{
			Spec: register.Spec{
				Name:        nameCollector,
				File:        "internal/bootstrap/components/components.go",
				Method:      "collectorComponent / NewRegistry",
				Description: "注册 Collector Kafka 投递链路健康探测和关闭入口",
			},
			build: func(ctx buildContext) []svc.Component {
				if ctx.ServiceContext == nil || !ctx.ServiceContext.CurrentConfig().Collector.Enabled {
					return nil
				}
				return []svc.Component{collectorComponent(ctx.ServiceContext.Collector)}
			},
		},
	}
}

// defaultComponents 从默认组件规格派生组件生命周期清单。
func defaultComponents(ctx buildContext) []svc.Component {
	specs := DefaultSpecs()
	components := make([]svc.Component, 0, len(specs))
	for _, spec := range specs {
		if spec.build == nil {
			continue
		}
		components = append(components, spec.build(ctx)...)
	}
	return components
}

// siteMySQLComponents 按名称排序生成扩展库组件，保证启动与关闭顺序稳定。
func siteMySQLComponents(ctx buildContext) []svc.Component {
	if ctx.ServiceContext == nil {
		return nil
	}
	names := make([]string, 0, len(ctx.ServiceContext.SiteDBs.NamedDBs))
	for name := range ctx.ServiceContext.SiteDBs.NamedDBs {
		names = append(names, string(name))
	}
	sort.Strings(names)
	components := make([]svc.Component, 0, len(names))
	for _, name := range names {
		db := ctx.ServiceContext.SiteDBs.NamedDBs[svc.DBName(name)]
		components = append(components, mysqlComponent("mysql_"+name, db, ctx.CloseGuard))
	}
	return components
}

// serviceContextComponent 返回 ServiceContext 缺失时的启动健康兜底组件。
func serviceContextComponent() svc.Component {
	return svc.Component{
		Name:      nameServiceContext,
		ErrorCode: codes.DependencyUnavailable,
		Check: func(context.Context) error {
			return errors.Errorf("ServiceContext未初始化")
		},
	}
}

// mysqlComponent 创建 MySQL 组件探测和释放入口。
func mysqlComponent(name string, db *gorm.DB, closeGuard *gormDBCloseGuard) svc.Component {
	return svc.Component{
		Name:      name,
		ErrorCode: codes.MySQLUnavailable,
		Check: func(ctx context.Context) error {
			return errors.Tag(mysqlx.Ping(ctx, db))
		},
		Close: func(context.Context) error {
			return closeGuard.close(name, db)
		},
	}
}

// redisComponent 创建 Redis 组件探测和释放入口。
func redisComponent(rds redis.UniversalClient) svc.Component {
	return svc.Component{
		Name:      nameRedis,
		ErrorCode: codes.RedisUnavailable,
		Check: func(ctx context.Context) error {
			if rds == nil {
				return errors.Errorf("Redis客户端未初始化")
			}
			return errors.Tag(rds.Ping(ctx).Err())
		},
		Close: func(context.Context) error {
			if rds == nil {
				return nil
			}
			return errors.Tag(rds.Close())
		},
	}
}

// snowflakeComponent 创建分布式 ID 运行资源探测和释放入口。
func snowflakeComponent(lease svc.SnowflakeLease) svc.Component {
	return svc.Component{
		Name:      nameSnowflake,
		ErrorCode: codes.DependencyUnavailable,
		Check: func(ctx context.Context) error {
			if lease != nil {
				return errors.Tag(lease.Ready(ctx))
			}
			// 手工 worker_id 不持有租约，readiness 改查本地发号器是否已配置。
			if _, ok := idgen.CurrentWorkerID(); !ok {
				return errors.Errorf("雪花 worker_id 未初始化")
			}
			return nil
		},
		Close: func(ctx context.Context) error {
			if lease == nil {
				return nil
			}
			return errors.Tag(lease.Close(ctx))
		},
	}
}

// collectorComponent 创建 Collector Kafka 投递链路探测和释放入口。
func collectorComponent(collector svc.Collector) svc.Component {
	return svc.Component{
		Name:      nameCollector,
		ErrorCode: codes.DependencyUnavailable,
		Check: func(ctx context.Context) error {
			if collector == nil {
				return errors.Errorf("Collector未初始化")
			}
			return errors.Tag(collector.Ready(ctx))
		},
		Close: func(ctx context.Context) error {
			if collector == nil {
				return nil
			}
			return errors.Tag(collector.Close(ctx))
		},
	}
}

// close 去重关闭 GORM 底层连接池。
func (g *gormDBCloseGuard) close(name string, db *gorm.DB) error {
	if g == nil || db == nil {
		return nil
	}
	if _, ok := g.closed[db]; ok {
		return nil
	}
	g.closed[db] = struct{}{}
	if err := mysqlx.Close(db); err != nil {
		return errors.Wrapf(err, "关闭 MySQL[%s]连接池失败", name)
	}
	return nil
}
