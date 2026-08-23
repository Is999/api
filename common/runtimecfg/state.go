package runtimecfg

import "sync/atomic"

// Snapshot 保存当前进程可全局读取的轻量运行配置。
type Snapshot struct {
	AppID string // 当前应用唯一标识，用于 Redis key、签名和缓存隔离场景
}

// current 保存当前进程运行配置快照。
var current atomic.Value

// Set 原子替换运行期公共快照；AppID 的规范校验由 bootstrap 启动边界负责。
func Set(snapshot Snapshot) {
	current.Store(snapshot)
}

// Get 返回值副本；启动前尚未 Set 时返回空快照，不生成默认 AppID。
func Get() Snapshot {
	cfg, _ := current.Load().(Snapshot)
	return cfg
}

// Restore 原子恢复已保存的运行配置快照。
func Restore(snapshot Snapshot) {
	current.Store(snapshot)
}

// AppID 返回当前应用唯一标识。
func AppID() string {
	return Get().AppID
}
