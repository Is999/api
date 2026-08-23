package idgen

import (
	"sync"
	"sync/atomic"

	"github.com/Is999/go-utils/errors"
)

const (
	// IDStrategySnowflake 表示默认雪花 ID 生成策略。
	IDStrategySnowflake = "snowflake"
	// IDStrategySegment 表示 Redis 号段本地缓存生成策略。
	IDStrategySegment = "segment"
)

var (
	segmentResolverSeq    atomic.Uint64          // segmentResolverSeq 生成 Segment 解析器绑定 token
	segmentResolverMu     sync.RWMutex           // segmentResolverMu 保护当前 Segment 解析器
	activeSegmentResolver segmentResolverBinding // activeSegmentResolver 保存当前 Segment 解析器
)

// SegmentResolver 按业务命名空间提供 Redis 号段 ID。
type SegmentResolver interface {
	// SegmentEnabled 判断 namespace 是否强制使用号段 ID。
	SegmentEnabled(namespace string) bool
	// SegmentID 从对应号段取 ID，失败时直接返回错误且不降级到雪花。
	SegmentID(namespace string) (int64, error)
}

// segmentResolverBinding 保存当前生效的 Segment 解析器。
type segmentResolverBinding struct {
	resolver SegmentResolver // resolver 负责按 namespace 从本地号段缓存取号
	token    uint64          // token 用于关闭旧解析器时避免误清新解析器
}

// ConfigureSegmentResolver 注册按业务命名空间生成 Segment ID 的解析器。
func ConfigureSegmentResolver(resolver SegmentResolver) uint64 {
	if resolver == nil {
		ClearSegmentResolver(0)
		return 0
	}
	token := segmentResolverSeq.Add(1)
	segmentResolverMu.Lock()
	activeSegmentResolver = segmentResolverBinding{resolver: resolver, token: token}
	segmentResolverMu.Unlock()
	return token
}

// ClearSegmentResolver 清理指定 token 对应的 Segment 解析器。
func ClearSegmentResolver(token uint64) {
	segmentResolverMu.Lock()
	if token == 0 || activeSegmentResolver.token == token {
		activeSegmentResolver = segmentResolverBinding{}
	}
	segmentResolverMu.Unlock()
}

// currentSegmentResolver 返回当前 Segment 解析器和绑定 token。
func currentSegmentResolver() (SegmentResolver, uint64) {
	segmentResolverMu.RLock()
	defer segmentResolverMu.RUnlock()
	return activeSegmentResolver.resolver, activeSegmentResolver.token
}

// segmentResolverActive 判断指定 token 的 Segment 解析器是否仍然有效。
func segmentResolverActive(token uint64) bool {
	segmentResolverMu.RLock()
	defer segmentResolverMu.RUnlock()
	return token != 0 && activeSegmentResolver.token == token && activeSegmentResolver.resolver != nil
}

// nextSegmentID 从已启用的 Segment 解析器获取业务 ID。
func nextSegmentID(namespace string, resolver SegmentResolver, token uint64) (int64, error) {
	id, err := resolver.SegmentID(namespace)
	if err != nil {
		return 0, errors.Tag(err)
	}
	// 取号期间若旧解析器已关闭，该结果不再交付给业务请求。
	if !segmentResolverActive(token) {
		return 0, errors.Errorf("ID Segment 解析器已关闭 namespace=%s", namespace)
	}
	return id, nil
}
