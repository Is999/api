-- 原子校验前台用户会话 token、过期时间和认证版本。
-- 调用方：middleware.VerifyUserToken；go:embed 加载后由 embedasset 剥离文件头。
-- Key 来源：rediskeys.UserSessionKeys 按应用和用户隔离，三个 Key 使用同一 Redis Cluster 槽。
-- KEYS[1]: 用户 session Hash；KEYS[2]: 用户 session ZSET 索引；KEYS[3]: 用户认证版本 String。
-- ARGV: expected_auth_version, sid, token, now_ms。
local expected_version = ARGV[1]
local sid = ARGV[2]
local token = ARGV[3]
local now_ms = tonumber(ARGV[4])
-- 参数不完整时按未通过处理，不向客户端暴露内部原因。
if expected_version == '' or sid == '' or token == '' or not now_ms then
    return 0
end
-- JWT 认证版本必须与 Redis 围栏一致。
if redis.call('GET', KEYS[3]) ~= expected_version then
    return 0
end

-- 会话 token 与索引过期时间必须同时存在且匹配。
local saved_token = redis.call('HGET', KEYS[1], sid)
local expires_at_ms = tonumber(redis.call('ZSCORE', KEYS[2], sid))
if not saved_token or saved_token ~= token or not expires_at_ms then
    return 0
end
-- 发现过期会话时顺带清理 Hash 和索引，减少后续扫描压力。
if expires_at_ms <= now_ms then
    redis.call('HDEL', KEYS[1], sid)
    redis.call('ZREM', KEYS[2], sid)
    return 0
end
return 1
