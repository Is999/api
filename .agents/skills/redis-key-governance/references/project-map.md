# Redis Key 项目地图

## 发现 helper 与 registry

```bash
rg --files | rg '(^|/)(common/keys|common/rediskeys|data/keys|rediskeys|redis_keys|cachekeys|cache/keys)(/|$)'
rg -n 'RedisKey|redis key|registry|static_check|SCAN|KEYS|Scan\(|Keys\(' --glob '*.go' --glob '*.lua'
```

不要假设兄弟仓库使用相同包名；从当前调用点追到实际 Redis client、helper、Lua 和测试。

## 审查维度

- Key 模板、数据结构、hash tag、基数和归属服务。
- TTL、滑动续期、永久 Key、空值占位和过期语义。
- cache miss、回源锁、重建、失效和降级路径。
- 多 Key Lua/CAS 的同槽、owner、版本、计数与原子边界。
- 跨服务共享兼容和发布顺序。
- `SCAN`/`KEYS`、高基数索引和大结果集风险。

## 常用检查

```bash
rg -n "SCAN|Keys\\(|Scan\\(|redis\\.|Redis|fmt\\.Sprintf|:%|:\\*" --glob '*.go' --glob '*.lua'
python3 <skill-dir>/scripts/redis_key_scan.py <repo>
```

修改 helper/registry 时运行归属包和直接调用包测试；修改共享 Key 模板、hash tag、编码或 TTL 时，分别验证各消费仓库的当前契约。只有真实缓存数据需要处置时，才记录回填、精确失效或重建及其授权、验证和恢复边界；不预设双读或历史兼容窗口。
