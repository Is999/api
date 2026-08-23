package mysqlx

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync/atomic"
	"testing"

	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/plugin/dbresolver"
)

// TestMySQL8DialectorsSkipVersionQuery 通过真实 GORM 与 dbresolver 初始化，确认写库和读库都不会进入无界版本探测。
func TestMySQL8DialectorsSkipVersionQuery(t *testing.T) {
	var queries atomic.Int32 // 仅统计初始化可能发出的查询，不访问网络。
	var pools []*sql.DB      // 测试持有的写库和读库池，用于验证关闭结果。
	newTestDialector := func() gorm.Dialector {
		pool := sql.OpenDB(mysqlProbeConnector{queries: &queries})
		pools = append(pools, pool)
		t.Cleanup(func() { _ = pool.Close() })
		dialector, ok := mysql8Dialector("unused").(*gormmysql.Dialector)
		if !ok {
			t.Fatal("MySQL 基线必须使用已配置的 GORM MySQL dialector")
		}
		dialector.Conn = pool
		return dialector
	}
	// 写库与读副本走生产使用的同一方言入口；任何版本查询都由哨兵驱动拒绝。
	db, err := gorm.Open(newTestDialector(), &gorm.Config{DisableAutomaticPing: true})
	if err != nil {
		t.Fatalf("初始化写库失败: %v", err)
	}
	if err = db.Use(dbresolver.Register(dbresolver.Config{Replicas: []gorm.Dialector{newTestDialector()}})); err != nil {
		t.Fatalf("初始化读副本失败: %v", err)
	}
	if got := queries.Load(); got != 0 {
		t.Fatalf("初始化不应执行 VERSION 查询，实际次数=%d", got)
	}
	// 显式探测仍能建立连接；停机随后必须回收写库和全部读池。
	for _, pool := range pools {
		if err = pingMySQLWithStartupTimeout(t.Context(), pool.PingContext); err != nil {
			t.Fatalf("显式连接探测失败: %v", err)
		}
	}
	if err = Close(db); err != nil {
		t.Fatalf("关闭读写池失败: %v", err)
	}
	for index, pool := range pools {
		if err = pool.PingContext(t.Context()); err == nil {
			t.Fatalf("连接池[%d]关闭后仍可用", index)
		}
	}
}

// TestStartupPingPreservesCancellation 确认启动超时不会遮蔽上游已经发出的取消。
func TestStartupPingPreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	err := pingMySQLWithStartupTimeout(ctx, func(ctx context.Context) error { return ctx.Err() })
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("期望返回上游取消，实际=%v", err)
	}
}

// mysqlProbeConnector 为 sql.OpenDB 提供不访问网络的连接，避免注册全局测试驱动。
type mysqlProbeConnector struct {
	queries *atomic.Int32 // 共享查询计数；写库和读库都必须保持为零。
}

// Connect 只创建内存连接，生产 MySQL 协议不在该测试中模拟。
func (c mysqlProbeConnector) Connect(context.Context) (driver.Conn, error) {
	return mysqlProbeConn{queries: c.queries}, nil
}

// Driver 满足 database/sql 的连接器契约；Open 不参与本测试建连。
func (mysqlProbeConnector) Driver() driver.Driver { return mysqlProbeDriver{} }

// mysqlProbeDriver 明确拒绝绕过 Connector 的建连路径。
type mysqlProbeDriver struct{}

// Open 拒绝测试未声明的建连入口，避免误以为覆盖真实驱动。
func (mysqlProbeDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("测试只能通过 Connector 建连")
}

// mysqlProbeConn 记录初始化查询并支持显式 Ping 与关闭。
type mysqlProbeConn struct {
	queries *atomic.Int32 // 初始化期间的 QueryContext 调用次数。
}

// Prepare 拒绝测试未声明的预编译查询。
func (mysqlProbeConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("测试不使用预编译查询")
}

// Close 不持有网络资源；池关闭可通过后续 Ping 失败验证。
func (mysqlProbeConn) Close() error { return nil }

// Begin 拒绝初始化测试之外的事务。
func (mysqlProbeConn) Begin() (driver.Tx, error) { return nil, errors.New("测试不使用事务") }

// Ping 保留调用方取消，让显式启动探测仍走受控错误边界。
func (mysqlProbeConn) Ping(ctx context.Context) error { return ctx.Err() }

// QueryContext 让意外恢复的 VERSION 探测立即失败，不靠测试超时结束。
func (c mysqlProbeConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	c.queries.Add(1)
	return nil, errors.New("MySQL 8 初始化不应自动查询版本")
}
