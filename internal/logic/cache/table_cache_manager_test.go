package cache

import (
	"context"
	"testing"

	"api/common/runtimecfg"
	appconfig "api/internal/config"
	corelogic "api/internal/logic"
	"api/internal/svc"

	tablecache "github.com/Is999/table-cache"
	"github.com/alicebob/miniredis/v2"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/redis/go-redis/v9"
)

// TestTableCacheManagerRecordsMetrics 验证 API 的真实 table-cache Manager 会记录组件级 Prometheus 指标。
func TestTableCacheManagerRecordsMetrics(t *testing.T) {
	// 独立注册表隔离全局指标，miniredis 模拟单实例缓存命中，不连接外部 Redis。
	previousRuntime := runtimecfg.Get()
	runtimecfg.Set(runtimecfg.Snapshot{AppID: "site-a"})
	t.Cleanup(func() { runtimecfg.Restore(previousRuntime) })

	ctx := context.Background()
	registry := prometheus.NewRegistry()
	metrics, err := tablecache.NewPrometheusMetrics(
		tablecache.WithPrometheusRegisterer(registry),
		tablecache.WithPrometheusSubsystem(TableCacheMetricsSubsystem),
	)
	if err != nil {
		t.Fatalf("NewPrometheusMetrics() error = %v", err)
	}
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	svcCtx := svc.NewServiceContext(appconfig.Config{AppID: "site-a"}, "v1", svc.Dependencies{
		Rds:               client,
		TableCacheMetrics: metrics,
	})
	base := corelogic.NewBaseLogicWithContext(ctx, svcCtx)
	manager, err := TableCacheManager(base)
	if err != nil {
		t.Fatalf("TableCacheManager() error = %v", err)
	}
	// 预置 Hash 后通过真实 Manager 读取一次命中。
	key := TableCachePhysicalKey(base, "config_uuid:featureFlag")
	if err = client.HSet(ctx, key, "value", "enabled").Err(); err != nil {
		t.Fatalf("HSet(%s) error = %v", key, err)
	}
	var value map[string]string
	result, err := manager.GetState(ctx, key, &value)
	if err != nil {
		t.Fatalf("GetState(%s) error = %v", key, err)
	}
	if result.State != tablecache.LookupStateHit {
		t.Fatalf("GetState(%s) state = %s, want hit", key, result.State)
	}
	if value["value"] != "enabled" {
		t.Fatalf("cached value = %#v, want enabled", value)
	}

	// 核对实际命中次数，不能仅凭指标名称出现判定读取已计数。
	families, err := registry.Gather()
	if err != nil {
		t.Fatalf("registry.Gather() error = %v", err)
	}
	for _, family := range families {
		if family.GetName() == "tcache_cache_hit_total" {
			metrics := family.GetMetric()
			if len(metrics) != 1 || metrics[0].GetCounter().GetValue() != 1 {
				t.Fatalf("cache hit counters = %v, want one hit", metrics)
			}
			labels := metrics[0].GetLabel()
			if len(labels) != 1 || labels[0].GetName() != "index" || labels[0].GetValue() != "config_uuid" {
				t.Fatalf("cache hit labels = %v, want index=config_uuid", labels)
			}
			return
		}
	}
	t.Fatal("tcache_cache_hit_total metric not found")
}

// TestTableCachePhysicalKeyRejectsNonLogicalInput 确保缓存入口只接受未带站点前缀的规范逻辑 key。
func TestTableCachePhysicalKeyRejectsNonLogicalInput(t *testing.T) {
	previousRuntime := runtimecfg.Get()
	runtimecfg.Set(runtimecfg.Snapshot{AppID: "site-a"})
	t.Cleanup(func() { runtimecfg.Restore(previousRuntime) })

	base := corelogic.NewBaseLogicWithContext(context.Background(), svc.NewServiceContext(appconfig.Config{AppID: "site-a"}, "v1", svc.Dependencies{}))
	if got := TableCachePhysicalKey(base, "config_uuid:featureFlag"); got != "app:site-a:table:config_uuid:featureFlag" {
		t.Fatalf("TableCachePhysicalKey() = %q", got)
	}
	for _, invalid := range []string{" config_uuid:featureFlag ", "app:site-a:table:config_uuid:featureFlag", "app:site-b:table:config_uuid:featureFlag"} {
		if got := TableCachePhysicalKey(base, invalid); got != "" {
			t.Fatalf("TableCachePhysicalKey(%q) = %q, want empty", invalid, got)
		}
	}
}
