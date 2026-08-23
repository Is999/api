import tempfile
import unittest
from contextlib import redirect_stderr
from io import StringIO
from pathlib import Path

import yaml_comment_audit


class YAMLCommentAuditTest(unittest.TestCase):
    """只验证结构识别规则，不把含中文误当作内容正确的证明。"""
    def test_requires_adjacent_same_indent_chinese_comment(self):
        # 行尾注释不能让缺少上方说明的字段通过。
        lines = [
            "# 服务配置。",
            "service:",
            "  # 监听端口，单位为 TCP 端口号。",
            "  port: 8080",
            "  name: api # 行尾注释不能替代行级说明。",
        ]
        self.assertEqual(
            [(5, "service.name", "missing adjacent same-indent Chinese comment")],
            yaml_comment_audit.scan_lines(lines),
        )

    def test_rejects_english_comment_and_allows_dynamic_keys(self):
        # 动态子项可豁免，但其父字段仍必须有中文说明。
        lines = [
            "# Redis 配置。",
            "redis:",
            "  # Address rewrite map.",
            "  addr_map:",
            "    redis-1: 127.0.0.1",
        ]
        findings = yaml_comment_audit.scan_lines(lines, ["redis.addr_map"])
        self.assertEqual(1, len(findings))
        self.assertEqual("redis.addr_map", findings[0][1])

    def test_ignores_mapping_text_inside_block_scalar(self):
        # 脚本文本中的冒号不属于 YAML 字段。
        lines = ["# 脚本内容。", "script: |", "  missing: comment"]
        self.assertEqual([], yaml_comment_audit.scan_lines(lines))

    def test_main_reports_missing_file(self):
        # 无文件可检查必须报告调用错误，不能视为没有缺口。
        with tempfile.TemporaryDirectory() as root:
            missing = Path(root, "missing.yaml")
            error_output = StringIO()
            with redirect_stderr(error_output):
                self.assertEqual(2, yaml_comment_audit.main([str(missing)]))
            self.assertIn("path not found", error_output.getvalue())


if __name__ == "__main__":
    unittest.main()
