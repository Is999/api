package localcache

import (
	"math"
	"testing"
	"time"
)

// TestNewRejectsOverflowingTicker 确保清理间隔在创建后台 ticker 前完成整数边界校验。
func TestNewRejectsOverflowingTicker(t *testing.T) {
	for _, test := range []struct {
		name    string // 区分缺省语义、有效边界与溢出输入。
		seconds int64  // 调用方传入的清理间隔秒数。
		wantErr bool   // 超出 Duration 表示范围时必须返回错误。
	}{
		{name: "negative_default", seconds: -1},
		{name: "zero_default"},
		{name: "minimum", seconds: 1},
		{name: "maximum", seconds: math.MaxInt64 / int64(time.Second)},
		{name: "above_maximum", seconds: math.MaxInt64/int64(time.Second) + 1, wantErr: true},
		{name: "overflow", seconds: math.MaxInt64, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			cache, err := New[string, string](Options{TTLTickerDurationSeconds: test.seconds})
			if cache != nil {
				t.Cleanup(cache.Close)
			}
			if (err != nil) != test.wantErr {
				t.Fatalf("New() error = %v, wantErr = %v", err, test.wantErr)
			}
			if test.wantErr {
				if cache != nil {
					t.Fatal("非法清理间隔不应创建缓存")
				}
				return
			}
			if cache == nil {
				t.Fatal("合法清理间隔应创建缓存")
			}
		})
	}
}

// TestCacheSetGetAndMetrics 验证基础读写、删除和指标快照。
func TestCacheSetGetAndMetrics(t *testing.T) {
	cache, err := New[string, string](Options{
		NumCounters: 1_000,
		MaxCost:     1_000,
		Metrics:     true,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer cache.Close()

	// 写入完成后验证普通读取和无期限 TTL 语义。
	if ok := cache.Set("local:cache:key", "value"); !ok {
		t.Fatal("Set() = false")
	}
	// Ristretto 异步接收写入，读断言前先排空缓冲，避免把调度延迟当作 miss。
	cache.Wait()

	value, ok := cache.Get("local:cache:key")
	if !ok || value != "value" {
		t.Fatalf("Get() = %q, %v; want value, true", value, ok)
	}
	if _, ok = cache.GetTTL("local:cache:key"); !ok {
		t.Fatal("GetTTL() should find key without expiration")
	}

	// 删除后制造一次 miss，再核对命中和未命中指标均已累计。
	cache.Del("local:cache:key")
	cache.Wait()
	if _, ok = cache.Get("local:cache:key"); ok {
		t.Fatal("Get() after Del() should miss")
	}

	metrics := cache.Metrics()
	if metrics.Hits == 0 || metrics.Misses == 0 {
		t.Fatalf("Metrics() hits=%d misses=%d, want both positive", metrics.Hits, metrics.Misses)
	}
}

// TestCacheSetWithTTL 验证 TTL 到期后 Get 不返回过期值。
func TestCacheSetWithTTL(t *testing.T) {
	const ttl = 250 * time.Millisecond
	cache, err := New[string, string](Options{
		NumCounters: 1_000,
		MaxCost:     1_000,
		// 250 毫秒窗口覆盖 race/并行测试下的常见调度延迟，避免把测试进程短暂停顿误判为缓存提前过期。
		TTL: ttl,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer cache.Close()

	if ok := cache.Set("local:cache:ttl", "value"); !ok {
		t.Fatal("Set() = false")
	}
	cache.Wait()

	if _, ok := cache.Get("local:cache:ttl"); !ok {
		t.Fatal("Get() before expiration should hit")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, ok := cache.Get("local:cache:ttl"); !ok {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("Get() should miss no later than the bounded expiration window")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestCacheCostFallback 验证非法成本会回退为默认成本。
func TestCacheCostFallback(t *testing.T) {
	cache, err := New[string, string](Options{
		NumCounters:        1_000,
		MaxCost:            1_000,
		ItemCost:           3,
		Metrics:            true,
		IgnoreInternalCost: true,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer cache.Close()

	if ok := cache.SetWithCost("local:cache:cost", "value", 0); !ok {
		t.Fatal("SetWithCost() = false")
	}
	cache.Wait()

	if got := cache.Metrics().CostAdded; got != 3 {
		t.Fatalf("Metrics().CostAdded = %d, want 3", got)
	}
}

// TestCacheRejectNegativeTTL 验证负 TTL 不写入缓存。
func TestCacheRejectNegativeTTL(t *testing.T) {
	cache, err := New[string, string](Options{
		NumCounters: 1_000,
		MaxCost:     1_000,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	defer cache.Close()

	if ok := cache.SetWithTTL("local:cache:bad-ttl", "value", -time.Second); ok {
		t.Fatal("SetWithTTL() with negative TTL should return false")
	}
	cache.Wait()
	if _, ok := cache.Get("local:cache:bad-ttl"); ok {
		t.Fatal("Get() should miss negative TTL write")
	}
}

// TestCacheCloseIdempotent 验证 Close 可以重复调用且关闭后读写安全返回。
func TestCacheCloseIdempotent(t *testing.T) {
	cache, err := New[string, string](Options{
		NumCounters: 1_000,
		MaxCost:     1_000,
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	cache.Close()
	cache.Close()
	if ok := cache.Set("local:cache:closed", "value"); ok {
		t.Fatal("Set() after Close() should return false")
	}
	if _, ok := cache.Get("local:cache:closed"); ok {
		t.Fatal("Get() after Close() should miss")
	}
	cache.Wait()
	cache.Del("local:cache:closed")
	cache.Clear()
}
