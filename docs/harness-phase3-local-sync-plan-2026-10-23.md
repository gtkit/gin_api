# 第三阶段：本机模板回流与生成验收

日期：2026-10-23。状态：本次授权的本机实施与验收已完成；跨平台/原生客户端未验证项保留。本文取代原第三阶段的团队共享、CI 和发布方向；第一、二阶段历史证据不改写。

## 1. 需求、授权与边界

用户要求按本机使用目标调整第三阶段：先保存完整方案与实施计划，再把 gin_api 已验证改进同步到本机 `harness-engineering` 与 `openspec-auto-bootstrap`，验证 `go-harness` 新建及更新结果。当前不需要 CI、团队共享或发布。124 只等待实际复现，不继续寻找偶发原因。

当前聊天真实 ID 为 `01a1667d-a368-7782-8e71-c426a463358a`。旧绑定仍指向第二阶段诊断计划且已完成，不能据此实施第三阶段。现有会话契约只允许项目相对路径，不能覆盖两个外部源仓库；只读命令也已被该门禁拒绝。用户已明确允许“仅本次跨仓库同步任务跳过单项目会话工作流门禁”。此例外不关闭安全 hook、不更改 hook 定义或信任、不授予任何受控检查权限，也不导入或伪造其他会话状态。gin_api 本轮只写本文、总计划、验收报告及必要错误记录，不修改其生产代码或 OpenSpec artifacts。

目标：以后本机同一条 `go-harness` 能生成并安全更新携带已验证规则、完整错误 reader、可靠 CLI 入口、会话工作流与失败诊断的项目。

非目标：业务 Go/配置/依赖、其他 harness 类型的业务约定、用户全局 skills、用户级配置与信任、模板版本升级、安装新工具、Docker、CI、Git 提交/推送/发版、存量 OpenSpec change 对账或归档。已有两个源仓库的未提交修改全部保留。

## 2. 已核对事实与依赖

- harness 源：`/Users/xiaozhaofu/Ai/harness-engineering`（下称 H）；OpenSpec 源：`/Users/xiaozhaofu/Ai/openspec-auto-bootstrap`（下称 O）。`/Users/xiaozhaofu/go/bin/go-harness` 直接执行 H 的 `scripts/harness-init.sh`，源修改即可生效，不需要重装全局命令。
- H 首装由 `scripts/install-harness.sh` 和 `scripts/install-harness-commands.sh` 分发，更新由 `scripts/safe_update.py` 分发。O 首装有 `install.sh` 的显式 MANAGED_FILES 清单，更新使用镜像 `safe_update.py`。必须同时补齐首装与更新映射。
- H 的两个安全/SessionStart hook 与 gin_api 已验证版本一致，不重复修改。错误 reader 仍截断 240 行。H 缺少本轮 Go 项目规则检查、工作流回归及失败采集资产。
- O 模板尚缺 `workflow_state.py`、`run_openspec.py` 和工作流 CLI；旧首装解析依赖 PATH，healthcheck 仍有旧运行方式。O 已存在请求范围与安全更新的未提交改动，不能恢复或整体覆盖到旧版本。
- 本机 Python 3.13.11、Node 24.22.0、OpenSpec CLI 1.3.0 已有真实证据；实施时复核实际可执行入口。沿用标准库与现有依赖，不下载、不升级。Node 最低要求保留 >=20.19.0。
- H 的未跟踪 AGENT-WORKFLOW 文档，以及 O 的 README、VERSION、install.sh、openspec-auto、hooks、hook_common、安全更新文件与测试等已有差异，在快照中单独保存。后续只对本计划需要的段落作增量修改。
- PowerShell、Claude 原生回调、新建项目的 Codex 精确 hook 信任不具备本轮验收授权/环境，不写成通过。脚本测试不等于客户端已实际加载。

## 3. 文件、归属和兼容设计

### H：Go 模板与安装映射

1. `go-harness/AGENTS.md`、`CLAUDE.md`：同步 gin_api 从“行为纪律”开始的公共规则，保留各自入口头部，移除由 O 单独管理的 OpenSpec 块。不得复制个人全局规则或绝对业务路径。
2. `scripts/error-journal/read-error-journal.sh`、`.ps1`：同步完整读取版本，保留参数、缺文件失败、日志格式及状态协议。
3. `go-harness/scripts/check-agent-rules.py`、`test_runtime_wiring.py`、`test_workflow_contract.py`、`fixture_diagnostics.py`：同步 gin_api 当前已验证文件。诊断仍只作用于测试副本，不改变生产超时与重试。
4. `go-harness/skills/harness-plan/SKILL.md`、`harness-implement/SKILL.md`：同步会话契约指引；无 O 安装时明确仅在对应文件存在时使用其工具，不要求 `--no-openspec` 项目执行不存在的 CLI。
5. `scripts/install-harness.sh`：在现有脚本安装位置增加当前 profile 的脚本映射；既有 preserve/force 语义不变。`scripts/install-harness-commands.sh`：支持当前 profile 同名 skill 覆盖共享模板，未提供覆盖的 profile 保持旧映射。
6. `scripts/safe_update.py`：补 profile 脚本、skill 映射；同名 profile 内容确定性优先。不得改三方合并、冲突停止、锁、备份、回滚和用户定制保护语义。
7. `tests/test_go_harness_local.py`：增加仅本机、临时项目的新建/更新验收入口；不接 CI，不运行 Go 或受控质量检查。`README.md` 仅补本机验收入口与边界。

### O：可移植 OpenSpec 运行内容

1. `templates/repo/.openspec-auto/hooks/openspec_{context,router,guard,stop}.py` 和 `tools/hook_common.py`、`workflow_state.py`、`run_openspec.py`、`env.sh`：同步 gin_api 已验证实现；生产超时、重试、请求授权/隔离、安全边界保持原值。
2. `templates/repo/.openspec-auto/tools/healthcheck.sh`：同步 argv/统一解析与分层证据说明；只在 Go harness 安装时要求 Go 专属规则比较文件；存在 harness 安装时检查其 pre_tool_use hook，保留 O 独立安装能力。
3. `templates/repo/.harness/scripts/workflow.py` 与 `.harness/guides/workflow-contract.md`、`workflow-plan-example.md` 由 O 唯一管理，以使 O 独立安装也具备会话工具。示例从项目专属计划引用改成同目录便携示例；不复制业务计划、会话 ID、状态、错误历史或测试结果。
4. 两端 `openspec-auto/SKILL.md` 与 `AGENTS.md` / `CLAUDE.md` 的 managed block 同步当前授权边界及统一 CLI；保留 O 既有新项目布局说明，不扩展其适用范围。
5. 两端 hook JSON 仅给 O guard 补显式 `apply_patch` matcher；保留其他设置，不把 gin_api 的六条定义整体复制进去造成重复。
6. `install.sh` 补新增资产清单，复用 `run_openspec.py` 检出已有 CLI；保留 CLI flags、默认安装行为、版本与安全更新入口。不引入下载或配置新开关。
7. O 的 `safe_update.py` 与 H 镜像一致；只新增上述三条 O 拥有的 `.harness/` 精确路径权限，不放开任意项目路径。`README.md` 仅补本机使用、解析环境变量与证据边界说明。

环境参数保持 `OPENSPEC_BIN`（显式 CLI，失效即报错）、`NODE_BIN`（已有 Node 候选）、`OPENSPEC_PYTHON`（Python 入口）、`OPENSPEC_TELEMETRY`（未设时为 0）；无新依赖版本或配置语义。新建和更新均不复制 `.openspec-auto/state/`、错误日志、gin_api 文档或 `.cache`。不变更任何现有模块层级，只在现有 profile/模板目录补分发资产。

## 4. 验收设计

源修改前保存限定文件 bytes/mode/hash、两仓库 git status/diff 和当前 gin_api 受保护文件哈希。用修改前真实命令生成旧版临时项目并留下其 update-base，再修改源；不用凭猜测重建旧安装。

所有生成运行在本轮 `.cache` 下的隔离临时项目；子进程使用隔离用户目录，保证安装器旧全局 skill 清理、更新备份、CLI 探测不触及真实用户配置。仅子进程隔离，不改父进程环境、真实 HOME 或 CODEX_HOME。显式复用已核实的 Node/OpenSpec 入口，不靠联网安装；首装带 `--skip-codex-user-config`。

验收包括：

- 新建真实项目：六条 hook 分别存在且两端一致；规则正文一致；新增文件实际落地；O init 有真实输出；状态/错误历史未被从 gin_api 导入。
- 更新修改前的旧项目：新增文件补齐、旧模板改进合入；自定义规则附录、用户 hook、错误记录、会话自定义状态、业务 canary 保留。第二次更新无额外模板差异。未知基线和冲突失败仍保持目标原文件不变（利用现有安全更新定向测试或隔离反例）。
- 新建和更新后的产物运行既有功能回归：`test_runtime_wiring.py`（规则/reader、解析、hook 和诊断）与 `test_workflow_contract.py`（会话/授权/完成证据），不运行任何 Go、lint、vet、modern、race、coverage 或聚合检查。仅按实际可用平台报告 PowerShell 跳过。
- O 独立安装和 `go-harness --no-openspec` 的安装内容分别核对，避免新增硬依赖破坏原用法；不额外启动原生模型或登记信任。
- 新测试验证分发覆盖、O 特定路径归属、profile 覆盖和保护条件；仅覆盖本次增量，不顺手重跑其他语言模板套件。

每个真实命令记录 argv、cwd、stdout、stderr、实际退出码与单调时钟耗时。失败保留原件；124 若真实再现只保留完整现场并报告，不继续查因或改超时/重试。源文件内容相同只说明同步，不冒充运行通过。

## 5. 顺序实施计划

| 步骤 | 前置条件 | 具体动作 | 检查/证据与交付物 | 停止条件 | 状态 |
|---|---|---|---|---|---|
| L1 范围与方案 | 最新授权、规则、原计划和错误已读取 | 保存本文，修订总计划第三阶段，完整回读；记录本次工作流例外 | 两份文档、准确文件归属 | 事实未确认或越出本机目标 | 完成，已完整回读 |
| L2 基线 | L1 完成 | 限定快照、未提交差异保护；旧版真实生成；确定已有 CLI | before manifest、旧项目基线、命令日志 | 生成失败先观察原因，不猜基线 | 完成 |
| L3 同步 | L2 可用 | 同步 H/O 指定内容；补首装/更新映射、便携文档及专属回归 | 精确 diff、两镜像一致、逐文件回读 | 未知行为、覆盖既有差异或扩大归属 | 完成，全部限定资产已同步 |
| L4 本机生成验收 | L3 完成 | 新建、更新、重复更新、定向失败保护、O 独立/无 O 核对；产物执行功能回归 | 每项 argv/退出码/耗时/完整输出 | 同问题连续三次失败；安全拒绝；124 实际再现只留现场 | 完成；最终 103 通过、PowerShell 2 次跳过 |
| L5 收尾 | L4 有真实结果 | 更新本计划状态和验收报告、错误处置；核对源差异与 gin_api 保护哈希 | docs/harness-phase3-local-acceptance-2026-10-23.md、manifest、日志 | 有失败/未验证不能勾选整体通过 | 完成；124/跨仓库门禁与平台限制保留 |

## 6. 风险、失败与回滚

主要风险为遗漏更新映射、覆盖 O 已有安全更新改动、复制项目专用路径、独立 O 安装被 harness 文件依赖破坏、测试误触用户配置。分别用唯一资产归属、修改前快照和增量 diff、便携示例、独立安装场景、子进程用户目录隔离控制。

实际失败先保留命令与现场，区分本次引入和存量问题；只修本次增量。未确认的跨平台或原生客户端行为如实列为未验证，不扩大检查。回滚根据 before manifest 逐文件恢复本次差异；先核对是否有后来修改，不使用 reset/clean，不覆盖用户已有变更。新增文件仅在确认本次创建且未有后续修改后删除；保留历史日志和错误记录。没有提交或版本变化，无发布回滚步骤。

## 7. 实施记录

初始保存时：没有同步任何模板，没有运行本阶段生成或功能测试。后续仅填写真实结果。

本轮证据目录：`.cache/harness-phase3-local-IXDyTl/`。修改前真实 `go-harness` 生成 exit 0（6.945 秒）；源分发契约 3 项 exit 0。尚未代表更新后验收通过。

L4 首次新建失败（exit 1）：O 的 CLAUDE managed block 中原有 Claude Code Notes 被同步脚本当作布局尾部保留，AGENTS 无此段。两条提示已在公共块前文等价表达；本次统一两块并加入正文一致性反例。完整输出保留在 `012-local-generation-integration.*`，没有放宽规则检查。

L4 第二次运行：新建和新项目 27 项接线/21 项契约回归通过；真实旧版 update exit 0。更新后的接线测试因本次新增测试 hook 返回普通文本而 JSONDecodeError，失败采集器已保留完整现场。修正本机集成夹具为标准 JSON hook 输出；生产文件、既有测试及时间预算不变，不视为 124 历史原因。

最终记录：35 个源文件实际变化（12 新增），543 个受保护 tracked 文件哈希未变。新建、真实旧版更新、重复更新、独立 O 与无 O 安装均通过。首两次集成失败分别为本次块同步与本次测试 hook 输出问题，已修复并保留日志；未按此推断 124。详见 `docs/harness-phase3-local-acceptance-2026-10-23.md`。
