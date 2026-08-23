-- 回滚数据库提交失败前创建的用户会话，并在认证版本未推进时恢复本次被淘汰的旧会话。
-- 调用方：AuthLogic.rollbackCreatedSession；go:embed 加载后由 embedasset 剥离文件头。
-- Key 来源：rediskeys 会话 helper 按应用和用户隔离，全部 Key 使用同一 Redis Cluster 槽。
-- KEYS[1]: 用户 session Hash；KEYS[2]: 用户 session ZSET 索引；KEYS[3]: 用户认证版本 String。
-- KEYS[4]: 本次 sid 撤销键；KEYS[5..]: 按淘汰快照顺序排列的旧 sid 撤销键。
-- ARGV[1..7]: now_ms、expected_auth_version、created_sid、created_token、max_sessions、delete_empty_fence、revocation_ttl。
-- ARGV[8..]: 每组三项依次为旧 sid、token、expires_at_ms。
local now_ms = tonumber(ARGV[1])
local expected_version = ARGV[2]
local created_sid = ARGV[3]
local created_token = ARGV[4]
local max_sessions = tonumber(ARGV[5])
local delete_empty_fence = ARGV[6]
local revocation_ttl = tonumber(ARGV[7])
local evicted_arg_count = #ARGV - 7

local version_valid = expected_version and string.match(expected_version, '^%d+$') and string.match(expected_version, '[1-9]')
local delete_flag_valid = delete_empty_fence == '0' or delete_empty_fence == '1'
local evicted_args_valid = evicted_arg_count >= 0 and evicted_arg_count % 3 == 0 and #KEYS == 4 + evicted_arg_count / 3
if not now_ms or not version_valid or created_sid == '' or created_token == '' or not max_sessions or max_sessions < 1 or not delete_flag_valid or not evicted_args_valid or not revocation_ttl or revocation_ttl < 1 then
    return redis.error_reply('invalid user session rollback arguments')
end
-- 认证版本已推进时禁止旧请求回滚新状态。
if redis.call('GET', KEYS[3]) ~= expected_version then
    return -1
end

local current_token = redis.call('HGET', KEYS[1], created_sid)
-- 失败 sid 可能已被另一登录淘汰，撤销记录阻止其随后被恢复；未落库注册不留额外状态。
if delete_empty_fence == '0' and (not current_token or current_token == created_token) then
    redis.call('SET', KEYS[4], '1', 'EX', revocation_ttl)
end

-- 仅删除仍指向本次 token 的会话，避免误删并发刷新。
if current_token == created_token then
    redis.call('HDEL', KEYS[1], created_sid)
    redis.call('ZREM', KEYS[2], created_sid)
end

-- 只在会话上限内恢复仍有效且未撤销的淘汰项，无关 sid 退出不影响恢复。
local restored = 0
local available = max_sessions - redis.call('ZCARD', KEYS[2])
for index = 8, #ARGV, 3 do
    if available <= 0 then
        break
    end
    local old_sid = ARGV[index]
    local old_token = ARGV[index + 1]
    local old_expires_at_ms = tonumber(ARGV[index + 2])
    local revoked_key = KEYS[5 + (index - 8) / 3]
    if old_sid ~= '' and old_token ~= '' and old_expires_at_ms and old_expires_at_ms > now_ms and redis.call('HEXISTS', KEYS[1], old_sid) == 0 and redis.call('EXISTS', revoked_key) == 0 then
        redis.call('HSET', KEYS[1], old_sid, old_token)
        redis.call('ZADD', KEYS[2], old_expires_at_ms, old_sid)
        restored = restored + 1
        available = available - 1
    end
end

-- 空索引必须对应空 Hash，注册补偿才可删除版本栅栏。
local index_count = redis.call('ZCARD', KEYS[2])
if index_count == 0 then
    if redis.call('HLEN', KEYS[1]) ~= 0 then
        return redis.error_reply('inconsistent user session rollback state')
    end
    redis.call('DEL', KEYS[1], KEYS[2])
    if delete_empty_fence == '1' then
        redis.call('DEL', KEYS[3])
    end
    return restored
end

-- 三类 key 统一保留到最晚会话到期，避免版本栅栏提前消失。
local latest = redis.call('ZRANGE', KEYS[2], -1, -1, 'WITHSCORES')
local ttl_seconds = math.max(1, math.ceil((tonumber(latest[2]) - now_ms) / 1000))
redis.call('EXPIRE', KEYS[1], ttl_seconds)
redis.call('EXPIRE', KEYS[2], ttl_seconds)
redis.call('EXPIRE', KEYS[3], ttl_seconds)
return restored
