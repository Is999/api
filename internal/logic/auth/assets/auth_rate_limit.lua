-- 原子维护认证入口窗口计数和超限锁，避免 INCR 后未设置 TTL 的永久计数。
-- 调用方：AuthLogic.checkAuthRateLimit；go:embed 加载后由 embedasset 剥离文件头。
-- KEYS[1..2]：AuthRateLimitCount/Lock 同槽模板，按动作与主体 HMAC 隔离窗口计数和超限锁。
-- ARGV: window_seconds, max_attempts, lock_seconds。
local window_seconds = tonumber(ARGV[1])
local max_attempts = tonumber(ARGV[2])
local lock_seconds = tonumber(ARGV[3])
-- 非法边界直接报错，避免创建无法自动过期的限流 key。
if not window_seconds or window_seconds < 1 or not max_attempts or max_attempts < 1 or not lock_seconds or lock_seconds < 1 then
    return redis.error_reply('invalid auth rate limit arguments')
end
-- 已锁定的入口不再增加窗口计数。
if redis.call('EXISTS', KEYS[2]) == 1 then
    return -1
end

-- 首次计数同步设置 TTL，超限后切换为独立锁 key。
local count = redis.call('INCR', KEYS[1])
if count == 1 then
    redis.call('EXPIRE', KEYS[1], window_seconds)
end
if count > max_attempts then
    redis.call('SET', KEYS[2], '1', 'EX', lock_seconds)
    redis.call('DEL', KEYS[1])
    return -1
end
return count
