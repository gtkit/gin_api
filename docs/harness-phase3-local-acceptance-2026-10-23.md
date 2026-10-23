# 第三阶段本机模板回流验收

日期：2026-10-23。结论：本次授权的本机源同步、新建和更新验收完成；不代表所有客户端/平台已验收。

## 1. 交付范围

已先保存并完整回读 [实施计划](harness-phase3-local-sync-plan-2026-10-23.md)，再改源。用户已批准仅本次跨仓库工作跳过单项目会话门禁；没有修改 hook 定义/信任来实现例外，也没有伪造原生回调或工作流通过回执。

- H：/Users/xiaozhaofu/Ai/harness-engineering，实际差异 15 文件。同步 Go 规则、两种完整 reader、四份运行/诊断测试资产、两份 profile skill；补齐首装/安全更新映射，新增本机验收入口和 README 说明。
- O：/Users/xiaozhaofu/Ai/openspec-auto-bootstrap，实际差异 20 文件。同步 CLI 解析与会话工作流、两端配置/skill/managed block、可移植契约和示例；补齐安装清单与更新器精确归属，保留独立安装。
- 合计 35 个源文件实际变化（12 个新增）。另核对并写入的 openspec_guard.py 与原文件字节一致，不算修改。精确原/后 SHA-256 和 mode 见证据目录 audit.json、before-manifest.json。
- gin_api 本轮只改总计划、实施计划、本文及错误记录；543 个受保护的原有 tracked 文件哈希未变。没有修改业务 Go、Makefile 或现有 OpenSpec change。
- H 两个原生 hook 未修改；O 原有 VERSION 与 openspec-auto 命令的未提交 diff 保持一致，README 等已有内容保留后再作限定增量。未提交、推送、打版本或发布。

本机 go-harness 包装命令已经直接指向 H/scripts/harness-init.sh，源改动立即供下次调用使用，不需要重新安装命令。两源版本号未提升；实际本地内容以哈希清单为准。

## 2. 环境和命令证据

证据根：

`/Users/xiaozhaofu/go/src/Project/gin_api/.cache/harness-phase3-local-IXDyTl/`

复用本机 Python 3.13.11、Node 24.22.0、OpenSpec CLI 1.3.0。默认解析额外作了只读实测：不设置 OPENSPEC_BIN/NODE_BIN 时，源 run_openspec.py --resolve exit 0，得到现有 Node v24.22.0 与 NVM v24.6.0 内的 openspec.js。没有下载或升级。

生成在独立临时项目与子进程用户目录内运行，首装传 --skip-codex-user-config；HOME/CODEX_HOME 的真实用户配置未改。命令详细 argv、cwd、实际退出码、耗时和完整 stdout/stderr 分别保存在根 commands.json 与 integration-3/commands.json 及同目录日志，不以本表替代原始输出。

| 验收 | 本轮结果 | 实际耗时（秒） |
|---|---|---:|
| 修改前 go-harness 真实生成旧版夹具 | exit 0，留下真实 update-base | 6.945 |
| 更新后 go-harness 新建 | exit 0 | 8.031 |
| 更新旧版夹具副本 | exit 0 | 0.546 |
| 新建产物接线/reader/解析/诊断回归 | 27 项，26 通过、PowerShell 1 跳过，exit 0 | 21.911 |
| 新建产物会话契约回归 | 21 通过，exit 0 | 2.961 |
| 更新产物接线/reader/解析/诊断回归 | 27 项，26 通过、PowerShell 1 跳过，exit 0 | 21.621 |
| 更新产物会话契约回归 | 21 通过，exit 0 | 3.369 |
| 重复 update | exit 0，所有托管内容摘要不变 | 0.407 |
| 源分发契约 | 最终 4 项通过，包含两端块一致性 | 见集成日志 |
| 安全更新定向回归 | 5 项通过，exit 0 | 1.126 |
| O 独立安装 | exit 0，4 条 hook 与工作流文件完整 | 5.877 |
| go-harness --no-openspec | exit 0，2 条 harness hook，无 O 目录 | 1.692 |
| 无 O 安装的规则比较 | exit 0 | 0.065 |

本轮受影响的测试最终计 103 项通过、2 项因同一本机缺少 PowerShell 跳过；不重复累加中间成功重跑。安全更新 5 项覆盖冲突停止并保留三个版本、两组件作为一个事务、未知/损坏基线拒绝、共享规则和用户 hook 保留、写入失败恢复。

实际集成入口：

`python3 /Users/xiaozhaofu/Ai/harness-engineering/tests/test_go_harness_local.py --integration --legacy-project <本轮标记的旧夹具> --output <新的证据目录>`

该入口复用已安装 go-harness，旧夹具只复制后更新；不修改原夹具。日常可去掉 legacy 参数，仍验证新建、更新和重复更新。入口不接 CI、不运行 Go 门禁。

## 3. 关键不变量

新建两端 hook 定义一致，合计六条，OpenSpec guard 显式匹配 apply_patch。所有托管资产实际落地并逐文件比对内容；工作流 CLI/指南由 O 唯一拥有，profile assets 由 H 提供，更新器只放行三个明确的 O 工作流路径。两份更新器字节一致。

真实旧版更新保留了自定义规则前言、两端自定义 SessionStart hook、完整错误日志、独立会话 canary 和业务 canary。重复更新没有修改托管内容。跨会话隔离、没有授权的检查拒绝、缺失证据拒绝完成、文档补丁边界、完整错误尾部与失败采集均由生成产物回归实际执行覆盖。

规则和 CLI 的复制内容与 gin_api 逐字节核对；适配仅限便携例子、profile skill 的无 O 条件、独立安装的 healthcheck 和安装/更新映射。没有改变已验证的生产超时、重试或 124 涉及的生产逻辑。没有复制 gin_api 会话状态、错误历史或项目专属计划到模板。

## 4. 失败与处置

1. 首次生成：O 的 Claude managed block 多保留重复提示，一致性检查 exit 1。确认同步切片范围错误后统一两端块，添加对应分发回归。ERR-20261023-134 已经标准 close 入口关闭；原始日志保留。
2. 第二次集成：新建及其回归通过，旧版 update 成功；本次新增的用户 hook 夹具输出普通文本，引发 JSONDecodeError。仅修正本机测试夹具为标准 JSON，未改原有断言或生产文件。ERR-20261023-137 已关闭。完整 hook/CLI 现场保存在 integration-2/updated project/.cache/harness-fixture-failures/，这也实际证明诊断能保存失败现场。
3. 路径读取和证据序号读取错误分别记录于 132、138，按真实清单纠正并关闭；没有用失败输出作为通过证据。
4. 124 的历史偶发原因仍未复现/定位，本轮不关闭、不作额外探测。第二项具有明确的本次夹具原因，不能倒推为 124 根因。

## 5. 未验证和只报告事项

- PowerShell runtime 未验证；Claude 原生客户端、新建项目的 Codex 精确定义信任及回调没有本轮验收。配置存在或测试调用 hook 不等于客户端已加载。
- 跨仓库单项目工作流不受支持，ERR-20261023-133 保持 open；本次依据用户一次性例外完成，不修改这一运行逻辑。
- H 安装器原有完成提示仍概括为“打开项目即自动加载”；这不是信任/原生回调证据，本轮没有扩展修改该提示。
- CI、团队共享、发布、存量 OpenSpec 对账/归档均未执行；本机独用不需要为这些场景增加交付步骤。
- 未执行 lint、脚本 lint、vet、modern、全量 race、覆盖率、Go 构建/测试、容器操作或未授权的质量门禁。

## 6. 回滚与收尾

L1–L5 在本机范围完成。回滚依 before-manifest.json 与 before/，只还原本次差异，先核对后来修改；不得 reset/clean 或覆盖原有工作。保留本轮失败证据和日志。未把临时夹具安装到现有业务项目，也未修改真实用户级配置或其他阶段。
