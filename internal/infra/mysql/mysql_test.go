package mysqlx

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"api/internal/config"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/plugin/dbresolver"
)

// TestCloseClosesDBResolverPools 确保停机同时关闭主库和读副本，避免只关闭 GORM 默认连接池。
func TestCloseClosesDBResolverPools(t *testing.T) {
	db, pools := newResolverTestDB(t, "close")
	if len(pools) != 2 {
		t.Fatalf("测试连接池数量 = %d，期望主库和读副本共 2 个", len(pools))
	}
	if err := Close(db); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	for index, pool := range pools {
		if err := pool.Ping(); err == nil {
			t.Fatalf("连接池[%d]关闭后仍可用", index)
		}
	}
}

// TestPingChecksDBResolverReplicas 确保读副本掉线时 readiness 不会只因主库正常而误报就绪。
func TestPingChecksDBResolverReplicas(t *testing.T) {
	db, pools := newResolverTestDB(t, "ping")
	t.Cleanup(func() { _ = Close(db) })
	if err := Ping(context.Background(), db); err != nil {
		t.Fatalf("全部连接池正常时 Ping() error = %v", err)
	}
	if err := pools[1].Close(); err != nil {
		t.Fatalf("关闭测试读副本失败: %v", err)
	}
	if err := pools[0].Ping(); err != nil {
		t.Fatalf("测试主库应保持可用: %v", err)
	}
	if err := Ping(context.Background(), db); err == nil {
		t.Fatal("读副本关闭后 Ping() 应返回失败")
	}
}

// newResolverTestDB 用 SQLite 验证 dbresolver 池生命周期，不模拟 MySQL 协议或复制延迟。
func newResolverTestDB(t *testing.T, suffix string) (*gorm.DB, []*sql.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:mysql-"+suffix+"-base?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatalf("创建测试基础库失败: %v", err)
	}
	resolver := dbresolver.Register(dbresolver.Config{
		Replicas: []gorm.Dialector{sqlite.Open("file:mysql-" + suffix + "-replica?mode=memory&cache=shared")},
	})
	if err = db.Use(resolver); err != nil {
		t.Fatalf("注册测试读写分离失败: %v", err)
	}
	pools, err := sqlPools(db)
	if err != nil {
		t.Fatalf("读取测试连接池失败: %v", err)
	}
	return db, pools
}

// TestValidatePoolConfigRejectsUnlimitedConnections 确保绕过 bootstrap 直接建库时仍不能创建无限连接池。
func TestValidatePoolConfigRejectsUnlimitedConnections(t *testing.T) {
	valid := config.MySQLConfig{MaxOpenConns: 20, MaxIdleConns: 10, ConnMaxLifetime: 300}
	if err := validatePoolConfig(valid); err != nil {
		t.Fatalf("有效连接池配置被拒绝: %v", err)
	}
	for _, cfg := range []config.MySQLConfig{
		{MaxOpenConns: 0, MaxIdleConns: 0, ConnMaxLifetime: 300},
		{MaxOpenConns: 20, MaxIdleConns: 21, ConnMaxLifetime: 300},
		{MaxOpenConns: 20, MaxIdleConns: 10, ConnMaxLifetime: 0},
	} {
		if err := validatePoolConfig(cfg); err == nil {
			t.Fatalf("危险连接池配置应被拒绝: %+v", cfg)
		}
	}
}

// TestResolveDataSourcesRejectsSilentCleanup 确保直接装配不会裁剪或丢弃错误 DSN。
func TestResolveDataSourcesRejectsSilentCleanup(t *testing.T) {
	for _, cfg := range []config.MySQLConfig{
		{WriteDataSource: " mysql://write ", ReadDataSources: nil},
		{WriteDataSource: "mysql://write", ReadDataSources: []string{""}},
		{WriteDataSource: "mysql://write", ReadDataSources: []string{" mysql://read"}},
		{WriteDataSource: "mysql://write", ReadDataSources: []string{"mysql://write"}},
		{WriteDataSource: "mysql://write", ReadDataSources: []string{"mysql://read", "mysql://read"}},
	} {
		if _, _, err := resolveDataSources(cfg); err == nil {
			t.Fatalf("resolveDataSources() 应拒绝非规范 DSN: %+v", cfg)
		}
	}
}

// TestPingMySQLWithStartupTimeoutAddsDeadline 确保无截止时间的进程上下文不会让驱动探测无限阻塞。
func TestPingMySQLWithStartupTimeoutAddsDeadline(t *testing.T) {
	startedAt := time.Now()
	err := pingMySQLWithStartupTimeout(context.Background(), func(ctx context.Context) error {
		deadline, ok := ctx.Deadline()
		if !ok {
			t.Fatal("MySQL 启动探测上下文缺少 deadline")
		}
		remaining := time.Until(deadline)
		if remaining <= 0 || remaining > startupPingTimeout || deadline.After(startedAt.Add(startupPingTimeout+time.Second)) {
			t.Fatalf("MySQL 启动探测 deadline 不在预期窗口: remaining=%s deadline=%s", remaining, deadline)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("pingMySQLWithStartupTimeout() error = %v", err)
	}
}
