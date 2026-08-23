package localcache

import (
	"math"
	"sync"
	"time"

	"github.com/Is999/go-utils/errors"
	"github.com/dgraph-io/ristretto/v2"
)

// 本地缓存默认参数，调用方未配置时使用保守值。
const (
	// defaultNumCounters 表示默认追踪频次的 key 数量。
	defaultNumCounters int64 = 100_000
	// defaultMaxCost 表示默认最大成本，调用方决定成本单位。
	defaultMaxCost int64 = 1 << 20
	// defaultBufferItems 表示默认 Get 缓冲大小。
	defaultBufferItems int64 = 64
	// defaultItemCost 表示单个缓存项的默认成本。
	defaultItemCost int64 = 1
	// maxTTLTickerSeconds 是秒数换算为 Duration 时不会溢出的上限。
	maxTTLTickerSeconds = math.MaxInt64 / int64(time.Second)
)

// Options 定义本地缓存初始化参数。
type Options struct {
	NumCounters              int64         // 频次计数器数量，<=0 时使用默认值
	MaxCost                  int64         // 总成本上限，单位与 ItemCost 一致；默认成本不是对象字节数
	BufferItems              int64         // Get 缓冲大小，<=0 时使用默认值
	ItemCost                 int64         // 单项默认成本，<=0 时使用 1
	TTL                      time.Duration // 默认 TTL，<=0 表示不过期
	TTLTickerDurationSeconds int64         // TTL 清理间隔秒数，<=0 时使用 Ristretto 默认值，正值不得超过 maxTTLTickerSeconds
	Metrics                  bool          // 是否采集指标
	IgnoreInternalCost       bool          // 是否忽略 Ristretto 内部存储成本
}

// Metrics 描述本地缓存运行指标快照。
type Metrics struct {
	Hits         uint64  // 命中次数
	Misses       uint64  // 未命中次数
	KeysAdded    uint64  // 新增 key 数量
	KeysUpdated  uint64  // 更新 key 数量
	KeysEvicted  uint64  // 淘汰 key 数量
	CostAdded    uint64  // 已加入成本总量
	CostEvicted  uint64  // 已淘汰成本总量
	SetsDropped  uint64  // 写入缓冲丢弃次数
	SetsRejected uint64  // 准入策略拒绝次数
	GetsDropped  uint64  // Get 计数丢弃次数
	GetsKept     uint64  // Get 计数保留次数
	HitRatio     float64 // 命中率
}

// Cache 封装 Ristretto 进程内缓存。
type Cache[K ristretto.Key, V any] struct {
	inner     *ristretto.Cache[K, V] // Ristretto 缓存实例
	itemCost  int64                  // Set 未指定成本时使用的成本
	ttl       time.Duration          // Set 未指定 TTL 时使用的 TTL
	closeOnce sync.Once              // 保证 Close 可重复调用
}

// New 创建本地缓存实例。
func New[K ristretto.Key, V any](options Options) (*Cache[K, V], error) {
	// 先限制原始秒数，避免依赖换算溢出后在创建 ticker 时 panic。
	if options.TTLTickerDurationSeconds > maxTTLTickerSeconds {
		return nil, errors.Errorf("本地缓存 TTL 清理间隔不能超过 %d 秒", maxTTLTickerSeconds)
	}
	options = normalizeOptions(options)
	cache, err := ristretto.NewCache(&ristretto.Config[K, V]{
		NumCounters:            options.NumCounters,
		MaxCost:                options.MaxCost,
		BufferItems:            options.BufferItems,
		Metrics:                options.Metrics,
		IgnoreInternalCost:     options.IgnoreInternalCost,
		TtlTickerDurationInSec: options.TTLTickerDurationSeconds,
	})
	if err != nil {
		return nil, errors.Wrap(err, "创建本地缓存失败")
	}
	return &Cache[K, V]{
		inner:    cache,
		itemCost: options.ItemCost,
		ttl:      options.TTL,
	}, nil
}

// Set 提交缓存写入，使用默认成本和默认 TTL；返回值表示是否进入写缓冲。
func (c *Cache[K, V]) Set(key K, value V) bool {
	if c == nil {
		return false
	}
	return c.SetWithTTLAndCost(key, value, c.ttl, c.itemCost)
}

// SetWithTTL 提交带 TTL 的缓存写入，使用默认成本；返回值表示是否进入写缓冲。
func (c *Cache[K, V]) SetWithTTL(key K, value V, ttl time.Duration) bool {
	if c == nil {
		return false
	}
	return c.SetWithTTLAndCost(key, value, ttl, c.itemCost)
}

// SetWithCost 提交指定成本的缓存写入，使用默认 TTL；返回值表示是否进入写缓冲。
func (c *Cache[K, V]) SetWithCost(key K, value V, cost int64) bool {
	if c == nil {
		return false
	}
	return c.SetWithTTLAndCost(key, value, c.ttl, cost)
}

// SetWithTTLAndCost 提交指定 TTL 和成本的缓存写入；cost<=0 时使用默认成本，返回值表示是否进入写缓冲。
func (c *Cache[K, V]) SetWithTTLAndCost(key K, value V, ttl time.Duration, cost int64) bool {
	if c == nil || c.inner == nil || ttl < 0 {
		return false
	}
	if cost <= 0 {
		cost = c.itemCost
	}
	// 接受入队不代表通过准入策略；读取方仍须处理未命中，不能把缓存当作权威存储。
	return c.inner.SetWithTTL(key, value, cost, ttl)
}

// Get 读取缓存值。
func (c *Cache[K, V]) Get(key K) (V, bool) {
	if c == nil || c.inner == nil {
		var zero V
		return zero, false
	}
	return c.inner.Get(key)
}

// GetTTL 返回 key 剩余 TTL；无过期时间时返回 0 和 true。
func (c *Cache[K, V]) GetTTL(key K) (time.Duration, bool) {
	if c == nil || c.inner == nil {
		return 0, false
	}
	return c.inner.GetTTL(key)
}

// Del 删除指定 key。
func (c *Cache[K, V]) Del(key K) {
	if c == nil || c.inner == nil {
		return
	}
	c.inner.Del(key)
}

// Clear 清空缓存内容。
func (c *Cache[K, V]) Clear() {
	if c == nil || c.inner == nil {
		return
	}
	c.inner.Clear()
}

// Wait 等待已入队写操作完成处理，不保证被准入策略拒绝的值可读。
func (c *Cache[K, V]) Wait() {
	if c == nil || c.inner == nil {
		return
	}
	c.inner.Wait()
}

// Close 释放缓存后台协程和缓冲资源。
func (c *Cache[K, V]) Close() {
	if c == nil || c.inner == nil {
		return
	}
	c.closeOnce.Do(func() {
		// 调用方可重复清理，Ristretto 的后台协程只关闭一次。
		c.inner.Close()
	})
}

// Metrics 返回当前缓存指标快照；未开启指标时返回零值。
func (c *Cache[K, V]) Metrics() Metrics {
	if c == nil || c.inner == nil || c.inner.Metrics == nil {
		return Metrics{}
	}
	metrics := c.inner.Metrics
	return Metrics{
		Hits:         metrics.Hits(),
		Misses:       metrics.Misses(),
		KeysAdded:    metrics.KeysAdded(),
		KeysUpdated:  metrics.KeysUpdated(),
		KeysEvicted:  metrics.KeysEvicted(),
		CostAdded:    metrics.CostAdded(),
		CostEvicted:  metrics.CostEvicted(),
		SetsDropped:  metrics.SetsDropped(),
		SetsRejected: metrics.SetsRejected(),
		GetsDropped:  metrics.GetsDropped(),
		GetsKept:     metrics.GetsKept(),
		HitRatio:     metrics.Ratio(),
	}
}

// normalizeOptions 补齐本地缓存默认参数，并将负数 TTL 配置归零。
func normalizeOptions(options Options) Options {
	if options.NumCounters <= 0 {
		options.NumCounters = defaultNumCounters
	}
	if options.MaxCost <= 0 {
		options.MaxCost = defaultMaxCost
	}
	if options.BufferItems <= 0 {
		options.BufferItems = defaultBufferItems
	}
	if options.ItemCost <= 0 {
		options.ItemCost = defaultItemCost
	}
	// 初始化的负 TTL 按未配置处理；逐次写入的负 TTL 则拒绝，避免误存永久数据。
	if options.TTL < 0 {
		options.TTL = 0
	}
	if options.TTLTickerDurationSeconds < 0 {
		options.TTLTickerDurationSeconds = 0
	}
	return options
}
