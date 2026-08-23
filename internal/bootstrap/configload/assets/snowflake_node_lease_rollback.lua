-- 雪花 node_id 未激活预占回滚脚本；正常停机不得调用。
-- KEYS[1]: 尚未发布给本地生成器的 node_id 租约 key。
-- ARGV[1]: 预占该 key 的当前实例 owner。
if redis.call("GET", KEYS[1]) == ARGV[1] then
    return redis.call("DEL", KEYS[1])
end
return 0
