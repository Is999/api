package bootstrap

import (
	"api/common/runtimecfg"
	"api/internal/config"
)

// publishRuntimeConfig 发布进程级运行配置快照，供 Redis key、签名和 MFA 等跨包能力读取。
func publishRuntimeConfig(c config.Config) runtimecfg.Snapshot {
	previous := runtimecfg.Get()
	runtimecfg.Set(runtimecfg.Snapshot{AppID: c.AppID})
	return previous
}
