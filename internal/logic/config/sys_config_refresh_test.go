package config

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"api/common/runtimecfg"
	appconfig "api/internal/config"
	"api/internal/model"
	"api/internal/svc"

	"github.com/alicebob/miniredis/v2"
	miniredisserver "github.com/alicebob/miniredis/v2/server"
	"github.com/redis/go-redis/v9"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// sysConfigRefreshHook 在第二次刷新实际遇到旧 owner 后放行旧查询，不以休眠推断竞态顺序。
type sysConfigRefreshHook struct {
	release   func()          // 释放尚未发布的旧数据库快照。
	completed <-chan struct{} // 旧刷新返回后再让新刷新继续等待判定。
	waited    atomic.Bool     // 只放行一次，后续抢锁不干涉真实 Manager。
}

// DialHook 复用真实 Redis 连接。
func (h *sysConfigRefreshHook) DialHook(next redis.DialHook) redis.DialHook { return next }

// ProcessPipelineHook 保留批量命令的实际执行结果。
func (h *sysConfigRefreshHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

// ProcessHook 只观察抢锁脚本的竞争结果，不改写 Redis 返回值。
func (h *sysConfigRefreshHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, cmd redis.Cmder) error {
		err := next(ctx, cmd)
		if err != nil || (cmd.Name() != "eval" && cmd.Name() != "evalsha") {
			return err
		}
		args := cmd.Args()
		if len(args) < 4 || !strings.Contains(fmt.Sprint(args[3]), ":rebuild:lock:") {
			return err
		}
		result, ok := cmd.(*redis.Cmd)
		if !ok {
			return err
		}
		values, sliceErr := result.Slice()
		if sliceErr != nil || len(values) != 2 || fmt.Sprint(values[0]) != "0" || fmt.Sprint(values[1]) == "" || !h.waited.CompareAndSwap(false, true) {
			return err
		}
		h.release()
		select {
		case <-h.completed:
			return err
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

// newSysConfigRefreshFixture 保留真实 Logic、Manager 和 Redis，只替换数据库查询响应。
func newSysConfigRefreshFixture(t *testing.T, query func(*gorm.DB)) (*SysConfigLogic, *redis.Client) {
	t.Helper()
	server := miniredis.RunT(t)
	server.Server().SetPreHook(func(peer *miniredisserver.Peer, command string, args ...string) bool {
		if !strings.EqualFold(command, "cluster") || len(args) != 1 || !strings.EqualFold(args[0], "info") {
			return false
		}
		peer.WriteError("ERR This instance has cluster support disabled")
		return true
	})
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	return newSysConfigRefreshLogic(t, client, "config-refresh-test", query), client
}

// newSysConfigRefreshLogic 让单元测试与真实 Redis 测试共用数据库快照夹具，不建立 MySQL 连接。
func newSysConfigRefreshLogic(t *testing.T, client redis.UniversalClient, appID string, query func(*gorm.DB)) *SysConfigLogic {
	t.Helper()
	previous := runtimecfg.Get()
	runtimecfg.Set(runtimecfg.Snapshot{AppID: appID})
	t.Cleanup(func() { runtimecfg.Restore(previous) })
	db, err := gorm.Open(mysql.New(mysql.Config{
		DSN: "test:test@tcp(127.0.0.1:1)/test", SkipInitializeWithVersion: true,
	}), &gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.Callback().Query().After("gorm:query").Register("test:config_refresh", query); err != nil {
		t.Fatal(err)
	}
	service := svc.NewServiceContext(appconfig.Config{AppID: appID}, "test", svc.Dependencies{
		Rds: client, SiteDBs: svc.SiteDatabases{MainDB: db},
	})
	return NewSysConfigLogic(t.Context(), service)
}

// TestSysConfigCacheRejectsCollationAlias 防止 MySQL 忽略大小写时创建无法按原 UUID 精确失效的别名缓存。
func TestSysConfigCacheRejectsCollationAlias(t *testing.T) {
	logic, client := newSysConfigRefreshFixture(t, func(tx *gorm.DB) {
		*tx.Statement.Dest.(*model.SysConfig) = model.SysConfig{UUID: "Billing", Type: 3, Value: `"secret"`}
		tx.RowsAffected = 1
	})
	if value, err := logic.GetCachedValue("billing"); err == nil {
		t.Fatalf("collation alias returned value=%v without error", value)
	}
	if client.Exists(t.Context(), logic.sysConfigCacheKey("billing")).Val() != 0 {
		t.Fatal("collation alias was cached outside the canonical invalidation key")
	}
}

// TestSysConfigRenewFencesOlderRefresh 验证显式刷新返回前重新读取主库，不能复用更新前已开始的回源。
func TestSysConfigRenewFencesOlderRefresh(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	started, release, completed := make(chan struct{}), make(chan struct{}), make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	defer unblock()
	var queries atomic.Int64
	logic, client := newSysConfigRefreshFixture(t, func(tx *gorm.DB) {
		value := `"new"`
		if queries.Add(1) == 1 {
			value = `"old"`
			close(started)
			select {
			case <-release:
			case <-tx.Statement.Context.Done():
				tx.AddError(tx.Statement.Context.Err())
			}
		}
		*tx.Statement.Dest.(*model.SysConfig) = model.SysConfig{UUID: "billing", Type: 3, Value: value}
		tx.RowsAffected = 1
	})
	logic.Ctx = ctx
	hook := &sysConfigRefreshHook{release: unblock, completed: completed}
	client.AddHook(hook)
	var firstErr error
	go func() {
		firstErr = logic.RenewByUUID("billing")
		close(completed)
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
	if err := logic.RenewByUUID("billing"); err != nil {
		t.Fatal(err)
	}
	<-completed
	if firstErr != nil || !hook.waited.Load() {
		t.Fatalf("older refresh err=%v, lock wait observed=%v", firstErr, hook.waited.Load())
	}
	if value, err := logic.GetCachedValue("billing"); err != nil || value != "new" || queries.Load() != 2 {
		t.Fatalf("value=%v err=%v queries=%d, want new value after two queries", value, err, queries.Load())
	}
}

// TestSysConfigRenewStopsAfterInvalidationFailure 失效失败或请求已取消时不能继续读取主库。
func TestSysConfigRenewStopsAfterInvalidationFailure(t *testing.T) {
	for _, name := range []string{"canceled", "redis_closed"} {
		t.Run(name, func(t *testing.T) {
			var queries int
			logic, client := newSysConfigRefreshFixture(t, func(tx *gorm.DB) { queries++ })
			if name == "canceled" {
				ctx, cancel := context.WithCancel(t.Context())
				cancel()
				logic.Ctx = ctx
			} else if err := client.Close(); err != nil {
				t.Fatal(err)
			}
			err := logic.RenewByUUID("billing")
			if err == nil || queries != 0 {
				t.Fatalf("err=%v queries=%d, want failure before database read", err, queries)
			}
			if name == "canceled" && !errors.Is(err, context.Canceled) {
				t.Fatalf("err=%v, want context cancellation", err)
			}
		})
	}
}

// TestSysConfigRenewLeavesMissAfterLoaderFailure 删旧成功后回源失败必须向上传递，不能恢复旧配置。
func TestSysConfigRenewLeavesMissAfterLoaderFailure(t *testing.T) {
	for _, name := range []string{"database_error", "canceled_in_loader"} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			queryErr := errors.New("query failed")
			logic, client := newSysConfigRefreshFixture(t, func(tx *gorm.DB) {
				if name == "canceled_in_loader" {
					cancel()
					tx.AddError(tx.Statement.Context.Err())
					return
				}
				tx.AddError(queryErr)
			})
			logic.Ctx = ctx
			key := logic.sysConfigCacheKey("billing")
			if err := client.HSet(t.Context(), key, "type", model.SysConfigTypeString, "value", `"old"`).Err(); err != nil {
				t.Fatal(err)
			}
			wantErr := queryErr
			if name == "canceled_in_loader" {
				wantErr = context.Canceled
			}
			if err := logic.RenewByUUID("billing"); !errors.Is(err, wantErr) {
				t.Fatalf("err=%v, want %v", err, wantErr)
			}
			if client.Exists(t.Context(), key).Val() != 0 {
				t.Fatal("failed refresh retained or rewrote the old configuration")
			}
		})
	}
}
