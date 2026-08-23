#!/usr/bin/env python3
"""检查项目自有 YAML 固定字段的紧邻中文注释，不代替源码语义复核。"""

import argparse
import os
import re
import sys
from pathlib import Path

HAN_RE = re.compile(r"[\u3400-\u9fff]")
SKIP_DIRS = {".git", "bin", "build", "coverage", "dist", "node_modules", "vendor"}


def mapping_field(line: str):
    """识别块式 mapping，引号内冒号不作为字段分隔符。"""
    indent = len(line) - len(line.lstrip(" "))
    content = line[indent:]
    if not content or content.startswith(("#", "---", "...", "%")):
        return None
    if content.startswith("- "):
        content = content[2:].lstrip(" ")
    elif content == "-":
        return None

    quote = None
    for index, char in enumerate(content):
        if quote:
            if char == quote and (index == 0 or content[index - 1] != "\\"):
                quote = None
            continue
        if char in ("'", '"'):
            quote = char
            continue
        if char != ":":
            continue
        if index + 1 < len(content) and not content[index + 1].isspace():
            continue
        key = content[:index].strip()
        # 流式集合和显式复杂键不在逐行检查范围，需要单独复核。
        if not key or key.startswith(("{", "[", "?")):
            return None
        if key[:1] == key[-1:] and key[:1] in ("'", '"'):
            key = key[1:-1]
        return key, indent, content[index + 1 :].strip()
    return None


def has_adjacent_chinese_comment(lines, index: int, indent: int) -> bool:
    """只接受字段上一行、同缩进的中文注释，父节点和行尾说明不能替代。"""
    if index == 0:
        return False
    previous = lines[index - 1]
    previous_indent = len(previous) - len(previous.lstrip(" "))
    return (
        previous_indent == indent
        and previous.lstrip(" ").startswith("#")
        and HAN_RE.search(previous) is not None
    )


def scan_lines(lines, dynamic_parents=()):
    """按缩进还原字段路径；动态父节点只豁免其直接数据项。"""
    dynamic = set(dynamic_parents)
    # 路径仅按源码缩进定位，不展开锚点、别名或合并后的有效配置。
    stack = []
    findings = []
    block_indent = None

    for index, line in enumerate(lines):
        stripped = line.strip()
        indent = len(line) - len(line.lstrip(" "))
        # 多行字符串中的冒号属于内容，不能当成新的配置字段。
        if block_indent is not None:
            if not stripped or indent > block_indent:
                continue
            block_indent = None
        if not stripped or stripped.startswith("#"):
            continue

        field = mapping_field(line)
        if field is None:
            continue
        key, indent, value = field
        while stack and indent <= stack[-1][0]:
            stack.pop()
        parent_path = ".".join(item[1] for item in stack)
        field_path = ".".join([*(item[1] for item in stack), key])
        if parent_path not in dynamic and not has_adjacent_chinese_comment(lines, index, indent):
            findings.append((index + 1, field_path, "missing adjacent same-indent Chinese comment"))
        if value == "":
            stack.append((indent, key))
        elif value.startswith(("|", ">")):
            block_indent = indent
    return findings


def yaml_files(paths):
    """只遍历指定路径，缺失目标报错而不是作为零缺口通过。"""
    for raw_path in paths:
        path = Path(raw_path)
        if path.is_file():
            if path.suffix.lower() in (".yaml", ".yml"):
                yield path
            continue
        if not path.is_dir():
            raise FileNotFoundError(raw_path)
        for root, dirs, files in os.walk(path):
            dirs[:] = sorted(name for name in dirs if name not in SKIP_DIRS)
            for name in sorted(files):
                candidate = Path(root, name)
                if candidate.suffix.lower() in (".yaml", ".yml"):
                    yield candidate


def main(argv=None) -> int:
    """默认以非零退出码报告缺口；建议模式只输出问题，不阻断命令。"""
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("paths", nargs="+", help="project-owned YAML file or directory")
    parser.add_argument(
        "--dynamic-parent",
        action="append",
        default=[],
        help="dot path whose direct mapping keys are dynamic data; may be repeated",
    )
    parser.add_argument("--advisory-exit-zero", action="store_true")
    args = parser.parse_args(argv)

    count = 0
    try:
        files = list(yaml_files(args.paths))
    except FileNotFoundError as error:
        print(f"path not found: {error}", file=sys.stderr)
        return 2
    for path in files:
        lines = path.read_text(encoding="utf-8").splitlines()
        for line, field_path, message in scan_lines(lines, args.dynamic_parent):
            count += 1
            print(f"{path}:{line}: {message}: {field_path}")
    if count and not args.advisory_exit_zero:
        return 1
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
