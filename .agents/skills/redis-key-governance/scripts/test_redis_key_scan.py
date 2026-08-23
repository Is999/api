import tempfile
import unittest
from contextlib import redirect_stderr
from io import StringIO
from pathlib import Path

from redis_key_scan import (
    is_allowed,
    looks_like_key_literal,
    main,
    scan_file,
    should_scan_line,
    string_literals,
    walk_files,
)


class RedisKeyScanTest(unittest.TestCase):
    """使用临时源码验证识别边界，不连接 Redis 或执行 Lua。"""
    def test_relative_helper_path_is_allowed(self):
        # 相对路径同样能识别项目集中 Key 目录。
        self.assertTrue(is_allowed("common/rediskeys/user.go"))

    def test_lua_comment_is_ignored(self):
        # 注释里的示例不能被报告成运行期 Key。
        with tempfile.TemporaryDirectory() as root:
            path = Path(root, "script.lua")
            path.write_text('-- redis key "danger:*"\nreturn 1\n', encoding="utf-8")
            self.assertEqual(scan_file(str(path)), [])

    def test_inline_key_outside_helper_is_reported(self):
        # 没有 Redis 调用时，Key 常量赋值仍须进入审阅。
        with tempfile.TemporaryDirectory() as root:
            path = Path(root, "logic.go")
            path.write_text('package logic\nconst cacheKey = "user:profile"\n', encoding="utf-8")
            findings = scan_file(str(path))
            self.assertEqual(len(findings), 1)
            self.assertIn("possible inline Redis key literal", findings[0][2])

    def test_route_and_log_strings_are_not_key_context(self):
        # 路由占位符、日志格式和脱敏星号不能构成 Key 证据。
        self.assertFalse(should_scan_line('t.Fatalf("route missing: %s", key)'))
        self.assertFalse(should_scan_line('Path: "/api/items/:id",'))
        self.assertFalse(looks_like_key_literal("***"))

    def test_redis_operation_literal_is_reported(self):
        # 直接传给 Redis 的字面量不能只靠变量赋值规则发现。
        with tempfile.TemporaryDirectory() as root:
            path = Path(root, "logic.go")
            path.write_text(
                'package logic\nfunc save() { rds.Set(ctx, "user:session", value, ttl) }\n',
                encoding="utf-8",
            )
            findings = scan_file(str(path))
            self.assertEqual(len(findings), 1)

    def test_extended_redis_operations_are_scanned(self):
        # 计数、集合、流和发布操作使用同一审阅入口。
        for operation in ("Incr", "Exists", "HGetAll", "XAdd", "Publish", "TTL"):
            with self.subTest(operation=operation):
                line = f'rds.{operation}(ctx, "rate:limit")'
                self.assertTrue(should_scan_line(line))

    def test_lua_keys_call_is_reported(self):
        # 一次 Lua 调用应分别报告全库扫描和内联 Key 两种线索。
        with tempfile.TemporaryDirectory() as root:
            path = Path(root, "script.lua")
            path.write_text("return redis.call('KEYS', 'user:*')\n", encoding="utf-8")
            findings = scan_file(str(path))
            messages = [finding[2] for finding in findings]
            self.assertTrue(any("Lua Redis scan/keys" in message for message in messages))
            self.assertTrue(any("inline Redis key" in message for message in messages))

    def test_lua_single_quoted_key_is_extracted(self):
        # Lua 单引号不能绕过仅识别 Go 字符串的扫描器。
        self.assertIn("user:profile", string_literals("redis.call('GET', 'user:profile')"))

    def test_walk_skips_go_tests_by_default(self):
        # 生产扫描与显式夹具扫描的文件集合必须不同。
        with tempfile.TemporaryDirectory() as root:
            Path(root, "logic.go").write_text("package logic\n", encoding="utf-8")
            Path(root, "logic_test.go").write_text("package logic\n", encoding="utf-8")
            self.assertEqual(len(list(walk_files(root))), 1)
            self.assertEqual(len(list(walk_files(root, include_tests=True))), 2)

    def test_main_rejects_missing_root_and_regular_file(self):
        # 建议模式只放宽命中项，不允许无效扫描范围伪装通过。
        with tempfile.TemporaryDirectory() as root:
            source = Path(root, "logic.go")
            source.write_text("package logic\n", encoding="utf-8")
            for path in (Path(root, "missing"), source):
                for flags in ([], ["--advisory-exit-zero"]):
                    with self.subTest(path=path, flags=flags):
                        output = StringIO()
                        with redirect_stderr(output):
                            self.assertEqual(2, main([str(path), *flags]))
                        self.assertIn("not a directory:", output.getvalue())


if __name__ == "__main__":
    unittest.main()
