-- 原子创建前台用户会话，并按认证版本隔离旧登录态、清理过期会话和执行每用户硬上限。
-- 调用方：AuthLogic.createSession；go:embed 加载后由 embedasset 剥离文件头。
-- Key 来源：rediskeys.UserSessionKeys 按应用和用户隔离，三个 Key 使用同一 Redis Cluster 槽。
-- KEYS[1]: 用户 session Hash；KEYS[2]: 用户 session ZSET 索引；KEYS[3]: 用户认证版本 String。
-- ARGV: now_ms, expected_auth_version, sid, token, expires_at_ms, max_sessions。
local now_ms = tonumber(ARGV[1])
local expected_version = ARGV[2]
local sid = ARGV[3]
local token = ARGV[4]
local expires_at_ms = tonumber(ARGV[5])
local max_sessions = tonumber(ARGV[6])

-- 参数必须完整且新会话过期时间晚于当前时间。
if not now_ms or not expected_version or not string.match(expected_version, '^%d+$') or not string.match(expected_version, '[1-9]') or not expires_at_ms or expires_at_ms <= now_ms or not max_sessions or max_sessions < 1 or sid == '' or token == '' then
    return redis.error_reply('invalid user session create arguments')
end

-- 十进制字符串比较避免 Lua number 丢失 uint64 认证版本精度。
local function compare_uint(left, right)
    left = string.gsub(left, '^0+', '')
    right = string.gsub(right, '^0+', '')
    if #left ~= #right then
        return #left < #right and -1 or 1
    end
    if left == right then
        return 0
    end
    return left < right and -1 or 1
end

-- 新版本可清空旧会话，旧版本请求不得覆盖已推进的围栏。
local current_version_text = redis.call('GET', KEYS[3])
if current_version_text then
    if not string.match(current_version_text, '^%d+$') or not string.match(current_version_text, '[1-9]') then
        return redis.error_reply('invalid cached auth version')
    end
    local compared = compare_uint(current_version_text, expected_version)
    if compared > 0 then
        return {-1}
    end
    if compared < 0 then
        redis.call('DEL', KEYS[1], KEYS[2])
    end
end
redis.call('SET', KEYS[3], expected_version)

-- 写入新会话前先原子清理过期 Hash 字段和索引成员。
local expired = redis.call('ZRANGEBYSCORE', KEYS[2], '-inf', now_ms)
if #expired > 0 then
    redis.call('HDEL', KEYS[1], unpack(expired))
    redis.call('ZREM', KEYS[2], unpack(expired))
end

redis.call('HSET', KEYS[1], sid, token)
redis.call('ZADD', KEYS[2], expires_at_ms, sid)

-- 超限时只淘汰创建前的最旧会话，本次新 sid 必须保留。
local overflow = redis.call('ZCARD', KEYS[2]) - max_sessions
local evicted = {}
if overflow > 0 then
    local candidates = redis.call('ZRANGE', KEYS[2], 0, overflow)
    local oldest = {}
    for _, old_sid in ipairs(candidates) do
        if old_sid ~= sid then
            table.insert(oldest, old_sid)
            if #oldest == overflow then
                break
            end
        end
    end
    if #oldest > 0 then
        for _, old_sid in ipairs(oldest) do
            local old_token = redis.call('HGET', KEYS[1], old_sid)
            local old_expires_at_ms = redis.call('ZSCORE', KEYS[2], old_sid)
            if old_token and old_expires_at_ms then
                table.insert(evicted, old_sid)
                table.insert(evicted, old_token)
                table.insert(evicted, old_expires_at_ms)
            end
        end
        redis.call('HDEL', KEYS[1], unpack(oldest))
        redis.call('ZREM', KEYS[2], unpack(oldest))
    end
end

-- 三类 key 统一保留到最晚会话到期，避免版本围栏提前消失。
local latest = redis.call('ZRANGE', KEYS[2], -1, -1, 'WITHSCORES')
local ttl_seconds = math.max(1, math.ceil((tonumber(latest[2]) - now_ms) / 1000))
redis.call('EXPIRE', KEYS[1], ttl_seconds)
redis.call('EXPIRE', KEYS[2], ttl_seconds)
redis.call('EXPIRE', KEYS[3], ttl_seconds)
-- 返回被淘汰会话快照，供数据库失败后的精确补偿使用。
local result = {#evicted / 3}
for _, value in ipairs(evicted) do
    table.insert(result, value)
end
return result
