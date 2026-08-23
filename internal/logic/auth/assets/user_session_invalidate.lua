-- 按数据库认证版本原子失效用户全部登录态，禁止旧版本覆盖新版本。
-- 调用方：AuthLogic.InvalidateUserSessions；go:embed 加载后由 embedasset 剥离文件头。
-- Key 来源：rediskeys.UserSessionKeys 按应用和用户隔离，三个 Key 使用同一 Redis Cluster 槽。
-- KEYS[1]: 用户 session Hash；KEYS[2]: 用户 session ZSET 索引；KEYS[3]: 用户认证版本 String。
-- ARGV: 已由业务用户表提交的新 auth_version、版本栅栏 TTL 秒数。
local expected_version = ARGV[1]
local version_ttl_seconds = tonumber(ARGV[2])
-- 版本围栏必须是正整数字符串，TTL 至少一秒。
if not expected_version or not string.match(expected_version, '^%d+$') or not string.match(expected_version, '[1-9]') or not version_ttl_seconds or version_ttl_seconds < 1 then
    return redis.error_reply('invalid user session invalidate arguments')
end

-- 字符串比较保留 uint64 认证版本的完整精度。
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

-- 已存在更高版本时拒绝旧请求，禁止回退认证围栏。
local current_version_text = redis.call('GET', KEYS[3])
if current_version_text then
    if not string.match(current_version_text, '^%d+$') or not string.match(current_version_text, '[1-9]') then
        return redis.error_reply('invalid cached auth version')
    end
    if compare_uint(current_version_text, expected_version) > 0 then
        return -1
    end
end

-- 会话 Hash 与索引一起清空，再写入已提交的新版本围栏。
local invalidated = redis.call('HLEN', KEYS[1])
redis.call('DEL', KEYS[1], KEYS[2])
redis.call('SET', KEYS[3], expected_version, 'EX', version_ttl_seconds)
return invalidated
