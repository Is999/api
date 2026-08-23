package database

import (
	"context"
	"database/sql"
	"database/sql/driver"
	_ "embed"
	"math"
	"strings"
	"time"

	"api/common/embedasset"

	"github.com/Is999/go-utils/errors"
)

const migrationLockReleaseTimeout = 5 * time.Second // 迁移结束后释放 MySQL 会话锁的最大等待时间。

var (
	// migrationLockAcquireSQL 是获取 MySQL 迁移命名锁的嵌入 SQL。
	//go:embed sql/migration_lock_acquire.sql.tmpl
	migrationLockAcquireSQL string
	// migrationLockReleaseSQL 是释放 MySQL 迁移命名锁的嵌入 SQL。
	//go:embed sql/migration_lock_release.sql.tmpl
	migrationLockReleaseSQL string
)

// migrationLock 保存持有 MySQL 命名锁的独占连接。
type migrationLock struct {
	conn *sql.Conn // 获取命名锁的数据库连接；释放必须使用同一连接
	name string    // 当前持有的命名锁
}

// WithMigrationLock 在 MySQL 命名锁保护下执行迁移，避免多个发布任务并发修改结构。
func WithMigrationLock(ctx context.Context, db *sql.DB, name string, wait time.Duration, run func() error) (err error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if db == nil {
		return errors.Errorf("数据库迁移连接池不能为空")
	}
	if db.Stats().MaxOpenConnections == 1 {
		return errors.Errorf("数据库迁移连接池 max_open_conns 不能为 1，命名锁和迁移执行至少需要 2 条连接")
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.Errorf("数据库迁移锁名称不能为空")
	}
	if run == nil {
		return errors.Errorf("数据库迁移执行函数不能为空")
	}
	lock, err := acquireMigrationLock(ctx, db, name, wait)
	if err != nil {
		return errors.Tag(err)
	}
	defer func() {
		// 请求取消不能跳过解锁，但清理仍限定 5 秒，防止 CLI 退出无限等待。
		releaseCtx, cancel := context.WithTimeout(context.Background(), migrationLockReleaseTimeout)
		defer cancel()
		if releaseErr := lock.release(releaseCtx); releaseErr != nil {
			if err == nil {
				err = errors.Tag(releaseErr)
			} else {
				err = errors.Join(err, errors.Wrap(releaseErr, "数据库迁移执行失败后释放命名锁失败"))
			}
		}
	}()
	return errors.Tag(run())
}

// acquireMigrationLock 使用独占连接获取 MySQL 命名锁。
func acquireMigrationLock(ctx context.Context, db *sql.DB, name string, wait time.Duration) (*migrationLock, error) {
	conn, err := db.Conn(ctx)
	if err != nil {
		return nil, errors.Wrap(err, "获取数据库迁移独占连接失败")
	}
	// GET_LOCK 使用整数秒，正的小数等待向上取整，负值按不等待处理。
	waitSeconds := int64(math.Ceil(wait.Seconds()))
	if waitSeconds < 0 {
		waitSeconds = 0
	}
	query := embedasset.StripLeadingLineComments(migrationLockAcquireSQL, "--")
	var acquired sql.NullInt64
	if err = conn.QueryRowContext(ctx, query, name, waitSeconds).Scan(&acquired); err != nil {
		_ = conn.Close()
		return nil, errors.Wrap(err, "获取数据库迁移锁失败")
	}
	if !acquired.Valid || acquired.Int64 != 1 {
		_ = conn.Close()
		return nil, errors.Errorf("等待数据库迁移锁超时: %s", name)
	}
	return &migrationLock{conn: conn, name: name}, nil
}

// release 在获取锁的同一连接上释放 MySQL 命名锁。
func (l *migrationLock) release(ctx context.Context) error {
	if l == nil || l.conn == nil {
		return nil
	}
	defer l.conn.Close()
	query := embedasset.StripLeadingLineComments(migrationLockReleaseSQL, "--")
	var released sql.NullInt64
	if err := l.conn.QueryRowContext(ctx, query, l.name).Scan(&released); err != nil {
		// 解锁结果未知时废弃物理连接，不能把可能仍持锁的会话归还连接池。
		_ = l.conn.Raw(func(any) error { return driver.ErrBadConn })
		return errors.Wrap(err, "释放数据库迁移锁失败")
	}
	if !released.Valid || released.Int64 != 1 {
		return errors.Errorf("数据库迁移锁未被当前连接持有: %s", l.name)
	}
	return nil
}
