-- 原子撤销指定前台用户会话，防止并发登录失败补偿恢复已退出的 sid。
-- 调用方：AuthLogic.deleteUserSession；go:embed 加载后由 embedasset 剥离文件头。
-- Key 来源：rediskeys 会话 helper 按应用和用户隔离，三个 Key 使用同槽标签。
-- KEYS[1]: 用户 session Hash；KEYS[2]: 用户 session ZSET 索引；KEYS[3]: 当前 sid 撤销 String。
-- ARGV[1]：已鉴权请求的稳定 sid；不比较 token，使并发刷新不能保留已注销会话。
-- ARGV[2]：JWT 硬上限加一秒的撤销 TTL，不能随当前配置缩短。
local sid = ARGV[1]
local revocation_ttl = tonumber(ARGV[2])
-- 空 sid 不得触发整组会话清理。
if not sid or sid == '' or not revocation_ttl or revocation_ttl < 1 then
    return redis.error_reply('invalid user session delete arguments')
end

-- 已被容量淘汰的 sid 也必须留撤销记录，且不占用有效会话容量。
redis.call('SET', KEYS[3], '1', 'EX', revocation_ttl)

-- Hash 和 ZSET 同步删除，最后一个会话移除后清理空容器。
local deleted = redis.call('HDEL', KEYS[1], sid)
redis.call('ZREM', KEYS[2], sid)
if redis.call('ZCARD', KEYS[2]) == 0 then
    redis.call('DEL', KEYS[1], KEYS[2])
end
return deleted
