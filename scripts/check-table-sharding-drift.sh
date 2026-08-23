#!/bin/sh

# 未处理错误和未定义变量均终止分支差异检查。
set -eu

# 参数优先，CI 环境变量和当前分支只提供默认值。
base_ref="${1:-main}"
requested_variant="${2:-}"
current_branch="${CI_COMMIT_BRANCH:-${CI_MERGE_REQUEST_TARGET_BRANCH_NAME:-$(git branch --show-current)}}"

# 基线必须可解析为提交，避免空 diff 被误判为通过。
if ! git rev-parse --verify --quiet "${base_ref}^{commit}" >/dev/null; then
	echo "分支差异检查失败：找不到基线 ${base_ref}" >&2
	exit 1
fi

# 未显式指定方案时仅识别受控长期分支。
if [ -z "${requested_variant}" ]; then
	if [ -n "${CI_MERGE_REQUEST_TARGET_BRANCH_NAME:-}" ] && [ "${CI_MERGE_REQUEST_TARGET_BRANCH_NAME}" = "${CI_DEFAULT_BRANCH:-main}" ]; then
		case "${CI_MERGE_REQUEST_SOURCE_BRANCH_NAME:-}" in
			table-sharding/shardingsphere-proxy-alternative|table-sharding/app-table-sharding)
				echo "分支差异检查失败：长期表拆分分支不能直接合入公共基线 ${CI_DEFAULT_BRANCH:-main}" >&2
				exit 1
				;;
		esac
		echo "分支差异检查跳过：合入公共基线的功能变更由合并后的 main 流水线校验"
		exit 0
	fi
	case "${current_branch}" in
		main) requested_variant="main" ;;
		table-sharding/shardingsphere-proxy-alternative) requested_variant="proxy" ;;
		table-sharding/app-table-sharding) requested_variant="app" ;;
		*)
			if [ -n "${CI:-}" ]; then
				echo "分支差异检查跳过：${current_branch} 不是受控长期分支"
				exit 0
			fi
			echo "分支差异检查失败：功能分支必须显式设置 BRANCH_VARIANT=main、proxy 或 app" >&2
			exit 1
			;;
	esac
fi

# 方案值只允许公共基线、代理分表和应用分表三种。
case "${requested_variant}" in
	main|proxy|app) ;;
	*)
		echo "分支差异检查失败：未知方案 ${requested_variant}" >&2
		exit 1
		;;
esac

# 长期方案分支必须包含最新公共基线。
if [ "${requested_variant}" != "main" ] && ! git merge-base --is-ancestor "${base_ref}" HEAD; then
	echo "分支差异检查失败：请先把 ${base_ref} 合入当前分支" >&2
	exit 1
fi

# 应用分表候选当前只允许说明文档差异，不放行运行代码。
is_allowed_app_path() {
	case "$1" in
		README.md|docs/*) return 0 ;;
		*) return 1 ;;
	esac
}

# Proxy 候选只在 README 说明外部部署方案，源码继续跟随基线。
is_allowed_proxy_path() {
	case "$1" in
		README.md) return 0 ;;
		*) return 1 ;;
	esac
}

# 逐文件核对允许差异，最终一次性输出全部越界路径。
unexpected=""
changed_paths="$(git -c core.quotepath=false diff --name-only "${base_ref}" HEAD)"
while IFS= read -r path; do
	[ -z "${path}" ] && continue
	case "${requested_variant}" in
		main) allowed=false ;;
		proxy)
			if is_allowed_proxy_path "${path}"; then allowed=true; else allowed=false; fi
			;;
		app)
			if is_allowed_app_path "${path}"; then allowed=true; else allowed=false; fi
			;;
	esac
	if [ "${allowed}" = false ]; then
		unexpected="${unexpected}${path}
"
	fi
done <<EOF
${changed_paths}
EOF

if [ -n "${unexpected}" ]; then
	echo "分支差异检查失败：以下公共路径偏离 ${base_ref}：" >&2
	printf '%s' "${unexpected}" >&2
	exit 1
fi

echo "分支差异检查通过：${requested_variant} 相对 ${base_ref} 仅包含允许差异"
