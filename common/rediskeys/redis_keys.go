package keys

// Redis key 根前缀集中维护。
const (
	// ScopeRoot 表示业务 Redis key 的 app_id 命名空间根前缀。
	// Redis 类型：命名空间前缀，TTL 过期规则：不直接写入 Redis。
	ScopeRoot = "app:"
)

// table-cache Redis key 二级前缀集中维护。
const (
	// tableCacheSegment 表示 table-cache 托管缓存的二级前缀。
	// Redis 类型：Key 片段，TTL 过期规则：不直接写入 Redis，由具体 table-cache 目标控制。
	tableCacheSegment = "table"

	// EmptyValueMarker 表示空值缓存占位符，避免缓存穿透时重复回源。
	// Redis 类型：占位值，TTL 过期规则：由具体缓存目标控制。
	EmptyValueMarker = "__empty__"
)

// Redis Key 模板集中维护，业务代码只能按模板精确读写。
const (
	// SnowflakeNodeLease 表示跨 api/admin 共享的雪花 node_id 租约 key 模板。
	// Redis 类型：String(owner)，TTL 过期规则：运行期续约；正常停机刷新完整 TTL 后自然过期，期间禁止其它主机复用 node_id。
	// 参数依次为部署级 scope、业务 namespace、node_id；该 key 不追加 app_id 前缀，确保同一业务统一互斥。
	SnowflakeNodeLease = "snowflake:node:%s:%s:%d"

	// IDSegmentCounter 表示跨 api/admin 共享的业务号段高水位 key 模板。
	// Redis 类型：String(integer)，TTL 过期规则：无 TTL；每次按业务 namespace 使用 INCRBY 分配本地号段。
	// 参数依次为部署级 scope、业务 namespace；该 key 不追加 app_id 前缀，确保同一业务统一递增。
	IDSegmentCounter = "idgen:segment:%s:%s"

	// UserSessionHash 表示前台用户登录会话 Hash 模板，field 为稳定 sid，value 为当前完整 token。
	// Redis 类型：Hash，TTL 过期规则：覆盖当前用户最晚过期的有效会话；用户 ID 使用 hash tag 保证会话键同槽。
	// 参数为用户 ID；实际 Redis key 通过 WithPrefix 追加 app_id 前缀。
	UserSessionHash = "user:session:{%d}"

	// UserSessionIndex 表示前台用户登录会话 sid 索引键模板。
	// Redis 类型：ZSet，score 为毫秒级过期时间；TTL 过期规则：覆盖当前用户全部有效会话，并在创建或失效时清理过期 sid。
	// 用户 ID 使用 hash tag，与会话 Hash 和认证版本键保持 Redis Cluster 同槽。
	// 参数为用户 ID；实际 Redis key 通过 WithPrefix 追加 app_id 前缀。
	UserSessionIndex = "user:session:index:{%d}"

	// UserSessionAuthVersion 表示前台用户当前认证版本缓存键模板。
	// Redis 类型：String(uint64)，TTL 过期规则：有会话时与最晚会话同步，全量失效时覆盖 JWT 最长存活期。
	// 参数为用户 ID；实际 Redis key 通过 WithPrefix 追加 app_id 前缀。
	UserSessionAuthVersion = "user:session:auth_version:{%d}"

	// UserSessionRevoked 标记主动退出或登录失败的 sid，防止并发补偿恢复已撤销会话。
	// Redis 类型：String(1)，每个撤销 sid 一个键；TTL 过期规则：JWT 硬上限 30 天加 1 秒后自动回收，不计入八会话容量。
	// 参数为用户 ID 和随机 sid；WithPrefix 追加 app_id，用户 hash tag 与会话 Lua 保持同槽。
	UserSessionRevoked = "user:session:revoked:{%d}:%s"

	// AuthRateLimitCount 表示认证入口限流计数键模板。
	// Redis 类型：String，TTL 过期规则：按认证限流窗口 TTL 过期，登录成功后精确删除。
	// 参数依次为动作、主体哈希；二者共同组成 hash tag，保证计数和锁定键在 Redis Cluster 同槽。
	AuthRateLimitCount = "auth:rate_limit:{%s:%s}:count"

	// AuthRateLimitLock 表示认证入口超限锁定键模板。
	// Redis 类型：String，TTL 过期规则：按认证限流锁定 TTL 过期，登录成功后精确删除。
	// 参数依次为动作、主体哈希；二者共同组成 hash tag，保证计数和锁定键在 Redis Cluster 同槽。
	AuthRateLimitLock = "auth:rate_limit:{%s:%s}:lock"

	// UserProfile 表示前台用户公开资料缓存键模板。
	// Redis 类型：String(JSON 或空值占位符)，TTL 过期规则：正值使用用户资料缓存 TTL，空值使用短 TTL。
	// 参数为用户 ID；实际 Redis key 通过 WithPrefix 追加 app_id 前缀。
	UserProfile = "user:profile:%d"

	// UserProfileRebuildLock 表示用户资料缓存跨进程重建锁模板。
	// Redis 类型：String(owner)，TTL 过期规则：按重建锁租约自动过期并在持锁期间续期。
	// 参数为用户 ID；实际 Redis key 通过 WithPrefix 追加 app_id 前缀。
	UserProfileRebuildLock = "user:profile:rebuild_lock:%d"

	// SysConfigUUID 表示系统配置缓存键模板。
	// Redis 类型：Hash，TTL 过期规则：由 table-cache 目标配置控制。
	// 参数为配置 uuid；实际 Redis key 通过 TableCachePrefix 追加 app_id 与 table 前缀。
	SysConfigUUID = "config_uuid:%s"

	// SysConfigUUIDPattern 表示系统配置缓存键展示模板。
	// Redis 类型：Hash 模板，TTL 过期规则：不直接写入 Redis，仅用于展示或匹配真实 key。
	SysConfigUUIDPattern = "config_uuid:{uuid}"

	// SignatureReplayRequest 表示签名防重放缓存键模板。
	// Redis 类型：String，TTL 过期规则：按签名防重放 TTL 自动过期。
	// 参数为 trace_id；实际 Redis key 通过 WithPrefix 追加 app_id 前缀。
	SignatureReplayRequest = "signature:replay:%s"

	// OpsReplayNonce 表示内网运维请求 nonce 防重放缓存键模板。
	// Redis 类型：String，TTL 过期规则：覆盖请求时间戳剩余有效窗口后自动过期。
	// 参数为十六进制 nonce；实际 Redis key 通过 WithPrefix 追加 app_id 前缀。
	OpsReplayNonce = "ops:nonce:%s"
)
