# 团队采用

## 权威源与跨 AI Agent 兼容

仓库 `.agents/skills` 是项目 AI 开发标准的唯一权威源，不属于某一个 AI 产品。能够直接识别该目录和 `SKILL.md` 的 AI Agent 必须直接使用仓库版本。

不能直接使用该格式的 AI Agent，必须先把当前任务会触发的 Skill 转换为自身原生支持的 rule、skill、instruction 或等价机制；如果运行环境没有可持久化的原生机制，则在当前任务计划中逐项展开并执行对应规则。任何情况下都不能以“不支持 Skills”为理由跳过项目规范。

转换必须满足：

- 先读取生效的 `AGENTS.md`、目标 `SKILL.md` 及其明确要求的 references/scripts，再生成适配版本。
- 完整保留 Skill 的触发条件、审批或暂停点、禁止项、执行边界、引用资源、验证命令和完成判定，不能压缩成概括性提示词。
- 按目标项目的真实技术栈、目录、命令和职责调整表达；不得机械复制来源项目名、路径、依赖、接口或发布方式。
- 仓库级适配器必须注明权威源路径并纳入版本管理；仅供个人 Agent 使用的派生文件放在其本地规则目录，不得反向覆盖仓库权威源。
- Skill 更新后必须重新生成或复核派生格式，通过结构检查和语义清单确认没有遗漏确认闸门及禁止项。

## 安装与派生形态

支持 Codex Skill 目录的个人环境可把缺失的标准 Skills 安装到 `$CODEX_HOME/skills` 或 `~/.codex/skills`。其他 AI Agent 应写入各自正式支持的项目级或个人级规则目录；具体目录和文件格式以该 Agent 的当前规范为准，不能在仓库中臆造多套无人消费的配置。

同名 skill 已由仓库提供时，个人目录不得维护另一份可独立修改的版本；双份来源会造成重复触发和规则漂移。确需兼顾仓库外使用时，只允许通过一次性同步流程从仓库生成个人镜像，并在同步后逐目录执行内容一致性校验。规则变更必须先修改仓库权威源，再整体覆盖个人镜像，禁止双向合并或分别修补。

建议同步流程遵循以下边界：

- 仅覆盖仓库清单中的同名 skill，不能对个人 skills 根目录执行全量删除。
- 同步前冻结仓库 skill 变更集；同步后用递归 diff 确认文件、权限和内容一致。
- 项目审计、交付和 CI 只以仓库 skill 为准，个人镜像是否存在不能改变项目闸门。

- `standard-development-flow`
- `requirement-intent-gate`
- `foundation-baseline-guard`
- `go-ai-implementation-guard`
- `simple-code-guard`
- `implementation-closure-review`
- `go-comment-style-audit`
- `redis-key-governance`
- `api-contract-sync`
- `frontend-vben-precommit`
- `go-db-query-performance-guard`
- `go-test-failure-debugger`
- `go-config-runtime-guard`
- `go-worker-concurrency-guard`
- `go-security-route-guard`
- `go-migration-backfill-runbook`
- `vue-vben-feature-delivery`
- `vue-api-contract-client-sync`
- `vue-e2e-smoke-browser`
- `vue-monorepo-ci-guard`
- `vue-permission-state-audit`
- `sibling-repo-sync-review`
- `release-handoff-guard`

仓库内 `.agents/skills` 按项目裁剪，避免不相关 skill 增加 token 和误触发：

- Go 后端仓库：使用通用流程、Go、Redis、API 契约、跨仓库和交付类 skill。
- Vue/vben 前端仓库：使用通用流程、vben、Vue API client、浏览器冒烟、前端 CI/hook、权限状态、跨仓库和交付类 skill；不放通用后端 `api-contract-sync`。
- 工作区入口或共享规范仓库：可放完整集合，用于跨项目维护。

## 仓库入口

在每个仓库的 `AGENTS.md` 中加入一条主规则：

```text
功能开发、问题修复、重构、生产级复核、交付前检查必须优先使用 $standard-development-flow，再按场景调用专项 skill。
```

项目特有规则放在仓库里，不放进共享 skill：

- build、test、lint 和 commit-hook 命令。
- 本地 route、permission、security、Redis、API-doc 和前端约定。
- data migration、backfill、cache invalidation 和生产审批规则。

## 项目画像

新项目使用标准流程前，先创建或更新仓库本地指导：

- `AGENTS.md`：简短硬约束和必跑检查。
- AI 开发文档：代码风格、注释、命名、日志、SQL/Lua、数据、API、前端和交付规则。
- 契约文档：API 格式、业务码、权限、安全字段、i18n 和 RouteMeta。
- 验证命令：package test、full test、typecheck、lint、build、hook 命令和 `git diff --check`。

## 标准运行规则

共享 workflow 负责流程，项目负责具体命令和领域规则。

不要让每个开发者私自 fork workflow。需要变化时更新共享 skill 和仓库入口，让所有开发者使用同一套闸门。
