//go:build integration

package config

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	keys "api/common/rediskeys"
	appconfig "api/internal/config"
	"api/internal/infra/redisx"
	"api/internal/model"

	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// TestSysConfigRealRedis 通过生产客户端验证单机与 Cluster 的读穿、TTL、精确失效和失败返回。
func TestSysConfigRealRedis(t *testing.T) {
	raw := os.Getenv("INTEGRATION_REDIS_CONFIG")
	if raw == "" {
		t.Skip("需要 INTEGRATION_REDIS_CONFIG 指向隔离 Redis 或 Cluster")
	}
	var redisCfg appconfig.RedisConfig
	if err := json.Unmarshal([]byte(raw), &redisCfg); err != nil {
		t.Fatalf("INTEGRATION_REDIS_CONFIG 格式无效: %v", err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	client, err := redisx.New(ctx, redisCfg, appconfig.ObservabilityConfig{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = client.Close() })
	// 数据库仅使用可控快照，Redis 客户端、路由、脚本和缓存逻辑均按项目接线执行。
	appID := fmt.Sprintf("config-integration-%d", time.Now().UnixNano())
	value, queryCount := `"old"`, 0
	logic := newSysConfigRefreshLogic(t, client, appID, func(tx *gorm.DB) {
		queryCount++
		if !slices.Equal(tx.Statement.Selects, []string{"uuid", "type", "value"}) {
			tx.AddError(fmt.Errorf("unexpected config projection: %v", tx.Statement.Selects))
			return
		}
		if len(tx.Statement.Vars) == 0 {
			tx.AddError(errors.New("config query omitted UUID"))
			return
		}
		uuid, ok := tx.Statement.Vars[0].(string)
		if !ok {
			tx.AddError(errors.New("config UUID is not a string"))
			return
		}
		if uuid == "missing" {
			tx.AddError(gorm.ErrRecordNotFound)
			return
		}
		if uuid == "alias" {
			uuid = "Alias"
		}
		*tx.Statement.Dest.(*model.SysConfig) = model.SysConfig{UUID: uuid, Type: model.SysConfigTypeString, Value: value}
		tx.RowsAffected = 1
	})
	logic.Ctx = ctx
	cleanupSysConfigRedis(t, client, keys.TableCachePrefix())

	// 第二次读取必须命中同一个 Hash，不额外查库；TTL 保持项目的一小时加单向抖动。
	for range 2 {
		if got, err := logic.GetCachedValue("billing:limit"); err != nil || got != "old" {
			t.Fatalf("read-through value=%v err=%v", got, err)
		}
	}
	key := logic.sysConfigCacheKey("billing:limit")
	if ttl, err := client.PTTL(ctx, key).Result(); err != nil || ttl < time.Hour-time.Second || ttl > 66*time.Minute {
		t.Fatalf("cache TTL=%v err=%v", ttl, err)
	}
	if queryCount != 1 {
		t.Fatalf("cache hit made %d queries, want 1", queryCount)
	}
	if cached, err := client.HGetAll(ctx, key).Result(); err != nil || len(cached) != 2 || cached["type"] != "3" || cached["value"] != value {
		t.Fatalf("projected cache=%v err=%v", cached, err)
	}
	// 模拟本地已有的完整模型 Hash，刷新必须移除不再发布的管理字段。
	if err := client.HSet(ctx, key, map[string]any{
		"id": 1, "uuid": "billing:limit", "title": "账单限制", "example": "示例", "remark": "备注",
		"page": 1, "pid": 0, "pids": "0", "version": 1, "updatedAt": "2026-09-09 00:00:00",
	}).Err(); err != nil {
		t.Fatal(err)
	}

	// 显式刷新先失效再发布新值，完整 UUID 中的冒号不能改变命中目标。
	value = `"new"`
	if err := logic.RenewByUUID("billing:limit"); err != nil {
		t.Fatal(err)
	}
	if cached, err := client.HGetAll(ctx, key).Result(); err != nil || len(cached) != 2 || cached["type"] != "3" || cached["value"] != value {
		t.Fatalf("refreshed cache retained unused fields=%v err=%v", cached, err)
	}
	if got, err := logic.GetCachedValue("billing:limit"); err != nil || got != "new" || queryCount != 2 {
		t.Fatalf("refresh value=%v err=%v queries=%d", got, err, queryCount)
	}

	// 空配置保留短期负缓存，普通重复读取不能持续触发主库查询。
	for range 2 {
		if _, err := logic.GetCachedValue("missing"); !errors.Is(err, ErrSysConfigNotFound) {
			t.Fatalf("missing config err=%v", err)
		}
	}
	if queryCount != 3 {
		t.Fatalf("empty cache made %d queries, want 3 total", queryCount)
	}

	// 排序规则别名必须返回错误，不得留下业务 Hash。
	if _, err := logic.GetCachedValue("alias"); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("collation alias err=%v", err)
	}
	if client.Exists(ctx, logic.sysConfigCacheKey("alias")).Val() != 0 {
		t.Fatal("collation alias created a cache key")
	}

	// 取消在失效前结束，既不查库，也不删除已成功发布的新值。
	canceled, stop := context.WithCancel(ctx)
	stop()
	logic.Ctx = canceled
	if err := logic.RenewByUUID("billing:limit"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled refresh err=%v", err)
	}
	if got, err := client.HGet(ctx, key, "value").Result(); err != nil || got != `"new"` || queryCount != 4 {
		t.Fatalf("canceled refresh changed value=%q err=%v queries=%d", got, err, queryCount)
	}
}

// cleanupSysConfigRedis 只清理本次唯一前缀与该配置目标的 64 个索引分片，不清空数据库。
func cleanupSysConfigRedis(t *testing.T, client redis.UniversalClient, prefix string) {
	t.Helper()
	if prefix == "" || strings.ContainsAny(prefix, "*?[]{}\\") {
		t.Fatal("测试 Redis 前缀为空或含 glob/hash tag，拒绝扩大清理范围")
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		cleanNode := func(ctx context.Context, node *redis.Client) error {
			for _, pattern := range []string{prefix + "*", "tcm:*:{" + prefix + "*", "tcm:*:" + prefix + "*"} {
				var cursor uint64
				for {
					found, next, err := node.Scan(ctx, cursor, pattern, 128).Result()
					if err != nil {
						return err
					}
					pipe := node.Pipeline()
					for _, key := range found {
						// 每次只提交一个物理 key，Cluster 同节点上的不同槽也不能合并 UNLINK。
						pipe.Unlink(ctx, key)
					}
					if _, err := pipe.Exec(ctx); err != nil {
						return err
					}
					cursor = next
					if cursor == 0 {
						break
					}
				}
			}
			return nil
		}
		var err error
		switch node := client.(type) {
		case *redis.Client:
			err = cleanNode(ctx, node)
		case *redis.ClusterClient:
			err = node.ForEachMaster(ctx, cleanNode)
		default:
			err = fmt.Errorf("unsupported Redis client %T", client)
		}
		if err != nil {
			t.Errorf("回收配置缓存: %v", err)
			return
		}
		// table-cache 未公开分片键构造器；按当前协议精确派生本目标摘要，不能按全库摘要扫描。
		indexKey := "tcm:pidx:" + prefix + strings.TrimSuffix(keys.SysConfigUUIDPattern, "{uuid}")
		sum := sha256.Sum256([]byte(indexKey))
		digest := fmt.Sprintf("%x", sum[:8])
		pipe := client.Pipeline()
		for shard := range 64 {
			pipe.Unlink(ctx, fmt.Sprintf("tcm:pidx:{%s:%02x}:%s", digest, shard, digest))
		}
		if _, err := pipe.Exec(ctx); err != nil {
			t.Errorf("回收配置缓存索引分片: %v", err)
		}
	})
}
