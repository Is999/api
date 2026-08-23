package cache_test

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	keys "api/common/rediskeys"
	"api/common/runtimecfg"
	"api/internal/config"
	cachelogic "api/internal/logic/cache"
	configlogic "api/internal/logic/config"
	"api/internal/model"
	"api/internal/svc"

	"github.com/alicebob/miniredis/v2"
	miniredisserver "github.com/alicebob/miniredis/v2/server"
	"github.com/redis/go-redis/v9"
	gormmysql "gorm.io/driver/mysql"
	"gorm.io/gorm"
)

// TestSysConfigCachePreservesUUIDSuffix 从业务读穿和刷新入口验证 UUID 分隔符不会改变查询对象。
func TestSysConfigCachePreservesUUIDSuffix(t *testing.T) {
	previous := runtimecfg.Get()
	runtimecfg.Set(runtimecfg.Snapshot{AppID: "site-a"})
	t.Cleanup(func() { runtimecfg.Restore(previous) })
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
	db, err := gorm.Open(gormmysql.New(gormmysql.Config{
		DSN: "test:test@tcp(127.0.0.1:1)/test", SkipInitializeWithVersion: true,
	}), &gorm.Config{DisableAutomaticPing: true, DryRun: true})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })

	rows := make(map[string]model.SysConfig)
	var queried []string
	// 按实际 SQL 参数选择记录，同前缀配置用于识别串读；不能直接回填用例期望值。
	if err := db.Callback().Query().After("gorm:query").Register("test:config_uuid_lookup", func(tx *gorm.DB) {
		// UUID 用于精确核对，缓存回源不应读取标题、备注等管理字段。
		if !slices.Equal(tx.Statement.Selects, []string{"uuid", "type", "value"}) {
			tx.AddError(fmt.Errorf("unexpected config projection: %v", tx.Statement.Selects))
			return
		}
		if !strings.Contains(tx.Statement.SQL.String(), "uuid = ?") || len(tx.Statement.Vars) == 0 {
			tx.AddError(fmt.Errorf("unexpected config query: %s %v", tx.Statement.SQL.String(), tx.Statement.Vars))
			return
		}
		uuid, ok := tx.Statement.Vars[0].(string)
		if !ok {
			tx.AddError(fmt.Errorf("unexpected UUID argument: %T", tx.Statement.Vars[0]))
			return
		}
		queried = append(queried, uuid)
		row, ok := rows[uuid]
		if !ok {
			tx.AddError(gorm.ErrRecordNotFound)
			return
		}
		*tx.Statement.Dest.(*model.SysConfig) = row
		tx.RowsAffected = 1
	}); err != nil {
		t.Fatal(err)
	}
	logic := configlogic.NewSysConfigLogic(t.Context(), svc.NewServiceContext(
		config.Config{AppID: "site-a"}, "test", svc.Dependencies{Rds: client, SiteDBs: svc.SiteDatabases{MainDB: db}},
	))
	for index, uuid := range []string{"billing", "billing:limit", "billing::limit", "billing: limit", "billing :limit", ":billing", "billing:", ":"} {
		t.Run(fmt.Sprintf("uuid_%d", index), func(t *testing.T) {
			want := "value:" + uuid
			rows[uuid] = model.SysConfig{ID: index + 1, UUID: uuid, Type: 3, Value: fmt.Sprintf("%q", want)}
			got, err := logic.GetCachedValue(uuid)
			if err != nil || got != want {
				t.Errorf("GetCachedValue(%q) = %v, %v; want %q", uuid, got, err, want)
			}
			if len(queried) == 0 || queried[len(queried)-1] != uuid {
				t.Errorf("SQL queried UUIDs = %q, want last %q", queried, uuid)
			}
			// 预置本地曾缓存的管理字段，刷新后必须只保留业务读取投影。
			key := cachelogic.TableCachePhysicalKey(logic.BaseLogic, fmt.Sprintf(keys.SysConfigUUID, uuid))
			if err := client.HSet(t.Context(), key, map[string]any{
				"id": 1, "uuid": uuid, "title": "配置", "example": "示例", "remark": "备注",
				"page": 1, "pid": 0, "pids": "0", "version": 1, "updatedAt": "2026-09-09 00:00:00",
			}).Err(); err != nil {
				t.Fatal(err)
			}
			// 刷新应替换完整 UUID 对应的 Hash，后续读取不得再次查库。
			want = "updated:" + uuid
			rows[uuid] = model.SysConfig{ID: index + 1, UUID: uuid, Type: 3, Value: fmt.Sprintf("%q", want)}
			if err := logic.RenewByUUID(uuid); err != nil {
				t.Errorf("RenewByUUID(%q): %v", uuid, err)
			}
			cached, err := client.HGetAll(t.Context(), key).Result()
			if err != nil || len(cached) != 2 || cached["type"] != "3" || cached["value"] != fmt.Sprintf("%q", want) {
				t.Errorf("refreshed hash %q = %v, %v", key, cached, err)
			}
			before := len(queried)
			got, err = logic.GetCachedValue(uuid)
			if err != nil || got != want || len(queried) != before {
				t.Errorf("cached read = %v, %v, new queries=%d; want %q", got, err, len(queried)-before, want)
			}
		})
	}
	// API 保留精确标识约束，空值和首尾空白不能被修剪后查询成另一条配置。
	for _, uuid := range []string{"", " billing", "billing ", "billing:limit "} {
		before := len(queried)
		if err := logic.RenewByUUID(uuid); err == nil || len(queried) != before {
			t.Errorf("invalid UUID %q must fail before SQL: err=%v, new queries=%d", uuid, err, len(queried)-before)
		}
	}
}
