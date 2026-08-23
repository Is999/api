#!/bin/sh

# 任一未处理错误或未定义变量都终止扫描。
set -eu

if [ "$#" -ne 0 ]; then
	echo "用法: $0（固定扫描全部 Git 跟踪文件，不接受路径缩小范围）" >&2
	exit 2
fi

# 所有命中一次汇总，便于开发者集中修复。
failures=""

# 本地真实配置不得进入 Git 索引。
if git ls-files --error-unmatch etc/config.yaml >/dev/null 2>&1; then
	failures="${failures}etc/config.yaml 被纳入版本控制；该文件只能保留在本机。\n"
fi

# 本地配置存在时必须限制为当前用户可读写。
if [ -f etc/config.yaml ]; then
	if mode="$(stat -f '%Lp' etc/config.yaml 2>/dev/null)"; then
		:
	else
		mode="$(stat -c '%a' etc/config.yaml)"
	fi
	case "$mode" in
		600) ;;
		*) failures="${failures}etc/config.yaml 权限为 ${mode}，必须限制为 600。\n" ;;
	esac
fi

# 扫描全部跟踪文本中的 PEM 私钥头。
private_key_files="$(git grep -l -I -E 'BEGIN (RSA |EC |OPENSSH )?PRIVATE KEY' -- . || true)"
if [ -n "$private_key_files" ]; then
	failures="${failures}发现被版本控制的私钥 PEM：\n${private_key_files}\n"
fi

# 字符串分段避免扫描脚本把自身规则误判为真实 YAML 密钥。
inline_secret_pattern='(^|[[:space:]])rsa_private_key_''server:[[:space:]]*[^[:space:]#]+|(^|[[:space:]])aes_''key:[[:space:]]*[^[:space:]#]+|(^|[[:space:]])aes_''iv:[[:space:]]*[^[:space:]#]+'
inline_secret_files="$(git grep -l -I -E "$inline_secret_pattern" -- . || true)"
if [ -n "$inline_secret_files" ]; then
	failures="${failures}发现不允许提交的内联安全密钥字段：\n${inline_secret_files}\n"
fi

# 汇总错误后统一非零退出，成功路径保持静默。
if [ -n "$failures" ]; then
	printf '%b' "$failures" >&2
	exit 1
fi
