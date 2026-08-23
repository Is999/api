-- 原子轮换前台用户会话，确保同一个旧 token 最多刷新成功一次。
-- 调用方：AuthLogic.rotateSession；go:embed 加载后由 embedasset 剥离文件头。
-- Key 来源：rediskeys.UserSessionKeys 按应用和用户隔离，三个 Key 使用同一 Redis Cluster 槽。
-- KEYS[1]: 用户 session Hash；KEYS[2]: 用户 session ZSET 索引；KEYS[3]: 用户认证版本 String。
-- ARGV: now_ms, expected_auth_version, sid, previous_token, new_token, expires_at_ms。
local now_ms = tonumber(ARGV[1])
local expected_version = ARGV[2]
local sid = ARGV[3]
local previous_token = ARGV[4]
local new_token = ARGV[5]
local expires_at_ms = tonumber(ARGV[6])

-- 新 token 必须完整且过期时间晚于当前时间。
if not now_ms or expected_version == '' or sid == '' or previous_token == '' or new_token == '' or not expires_at_ms or expires_at_ms <= now_ms then
    return redis.error_reply('invalid user session rotate arguments')
end
-- 认证版本不一致时禁止轮换旧登录态。
if redis.call('GET', KEYS[3]) ~= expected_version then
    return -1
end

-- CAS 前先清理过期成员，避免失效会话继续占用容量。
local expired = redis.call('ZRANGEBYSCORE', KEYS[2], '-inf', now_ms)
if #expired > 0 then
    redis.call('HDEL', KEYS[1], unpack(expired))
    redis.call('ZREM', KEYS[2], unpack(expired))
end

-- sid、旧 token 和索引过期时间必须同时匹配才允许替换。
local saved_token = redis.call('HGET', KEYS[1], sid)
local previous_expires_at = tonumber(redis.call('ZSCORE', KEYS[2], sid))
if not saved_token or saved_token ~= previous_token or not previous_expires_at or previous_expires_at <= now_ms then
    return 0
end

-- Hash 和过期索引在同一脚本内更新。
redis.call('HSET', KEYS[1], sid, new_token)
redis.call('ZADD', KEYS[2], expires_at_ms, sid)

-- 三类 key 的 TTL 对齐到最晚会话到期时间。
local latest = redis.call('ZRANGE', KEYS[2], -1, -1, 'WITHSCORES')
local ttl_seconds = math.max(1, math.ceil((tonumber(latest[2]) - now_ms) / 1000))
redis.call('EXPIRE', KEYS[1], ttl_seconds)
redis.call('EXPIRE', KEYS[2], ttl_seconds)
redis.call('EXPIRE', KEYS[3], ttl_seconds)
return 1
