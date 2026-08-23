package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"api/internal/bootstrap"
	"api/internal/database"
	mysqlx "api/internal/infra/mysql"

	"github.com/Is999/go-utils/errors"
)

// 迁移动作常量限定命令行允许的执行模式。
const (
	actionStatus            = "status"               // 只查看迁移状态
	actionDryRun            = "dry-run"              // 预览迁移计划但不执行 SQL
	actionUp                = "up"                   // 执行未登记的幂等资产，允许非空库执行和失败后续跑
	migrationLockName       = "app:schema-migration" // 与 admin 共用迁移锁，避免同实例并发修改库结构
	migrationLockWait       = time.Minute            // 发布任务等待已有迁移结束的最大时间
	defaultMigrationTimeout = 15 * time.Minute       // 单次迁移命令默认总时限，包含连接、锁等待、SQL 和收尾
	maxMigrationTimeout     = 2 * time.Hour          // 允许配置的总时限上限，禁止发布任务无界占用数据库资源
)

// buildVersion 由构建阶段通过 -ldflags 注入，用于发布排查。
var buildVersion = "dev"

// main 解析命令行参数并执行数据库迁移命令。
func main() {
	configFile := flag.String("f", "./etc/config.yaml", "配置文件路径")
	action := flag.String("action", actionStatus, "迁移动作：status/dry-run/up")
	allowBootstrap := flag.Bool("allow-bootstrap", false, "允许执行 bootstrap-only 基线迁移")
	allowDestructive := flag.Bool("allow-destructive", false, "允许执行 destructive 迁移")
	executionTimeout := flag.Duration("timeout", defaultMigrationTimeout, "迁移命令总时限，范围 (0,2h]")
	showVersion := flag.Bool("version", false, "输出构建版本并退出")
	flag.Parse()
	if *showVersion {
		fmt.Println(buildVersion)
		return
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, *configFile, *action, *allowBootstrap, *allowDestructive, *executionTimeout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

// run 在进程信号和总时限共同约束下加载配置、连接主库并执行迁移。
func run(parent context.Context, configFile string, action string, allowBootstrap bool, allowDestructive bool, executionTimeout time.Duration) (runErr error) {
	// 参数和上游取消状态先校验，非法请求不加载配置或连接数据库。
	if action != actionStatus && action != actionDryRun && action != actionUp {
		return errors.Errorf("不支持的迁移动作: %s", action)
	}
	if parent == nil {
		return errors.New("迁移上下文不能为空")
	}
	if executionTimeout <= 0 || executionTimeout > maxMigrationTimeout {
		return errors.Errorf("迁移命令总时限必须在 (0,%s] 范围内", maxMigrationTimeout)
	}
	if err := parent.Err(); err != nil {
		return errors.Wrap(err, "迁移命令已取消")
	}
	// 总时限同时约束连接、锁等待和资产执行。
	ctx, cancel := context.WithTimeout(parent, executionTimeout)
	defer cancel()
	cfg, _, _, err := bootstrap.LoadConfig(configFile)
	if err != nil {
		return errors.Wrap(err, "加载配置失败")
	}
	// 迁移状态检查和执行复用同一连接池，函数退出时统一关闭。
	db, err := mysqlx.New(ctx, cfg.MySQL, cfg.Observability)
	if err != nil {
		return errors.Wrap(err, "连接 MySQL 失败")
	}
	sqlDB, err := db.DB()
	if err != nil {
		return errors.Wrap(err, "获取 MySQL 底层连接失败")
	}
	defer func() {
		runErr = mergeMigrationCloseError(runErr, mysqlx.Close(db))
	}()

	// 同一迁移锁覆盖读取登记和执行，避免多个命令重复写入同一版本。
	var results []database.MigrationRunItem
	err = database.WithMigrationLock(ctx, sqlDB, migrationLockName, migrationLockWait, func() error {
		var runErr error
		results, runErr = database.RunMigrations(ctx, database.NewGormMigrationStore(db), database.DefaultMigrations(), database.MigrationRunOptions{
			DryRun:           action != actionUp,
			AllowBootstrap:   allowBootstrap,
			AllowDestructive: allowDestructive,
		})
		return errors.Tag(runErr)
	})
	// 执行失败仍输出逐项状态，便于运维定位被拒绝或未完成的资产。
	printResults(results)
	return errors.Tag(err)
}

// mergeMigrationCloseError 让连接关闭失败影响命令退出码，同时保留更早的迁移主错误。
func mergeMigrationCloseError(runErr error, closeErr error) error {
	if closeErr == nil {
		return errors.Tag(runErr)
	}
	if runErr == nil {
		return errors.Wrap(closeErr, "关闭 MySQL 连接失败")
	}
	return errors.Wrapf(runErr, "迁移执行失败且关闭 MySQL 连接失败 close_error=%v", closeErr)
}

// printResults 以固定列宽输出迁移状态，便于发布脚本读取。
func printResults(results []database.MigrationRunItem) {
	fmt.Printf("%-10s %-14s %-36s %s\n", "STATUS", "VERSION", "NAME", "ASSET")
	for _, item := range results {
		line := fmt.Sprintf("%-10s %-14s %-36s %s", item.Status, item.Version, item.Name, item.Asset)
		if item.Reason != "" {
			line += " # " + item.Reason
		}
		fmt.Println(line)
	}
}
