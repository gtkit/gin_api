# Harness 运行端接线与第一阶段验收

日期：2026-10-23。对应计划：`docs/harness-openspec-skills-improvement-plan-2026-10-23.md`。只交付阶段 1 的项目侧改动；阶段 2、3 未实施。

## 结论

本次授权的两项修复已通过专项验收：标准文档补丁正常放行；采集器完整收到匹配回合的 `turn/completed`，原生回合 42.783 秒完成、采集器 exit 0。文档/guard 10 项、采集器 8 项定向测试均通过，6 条已信任 hook 定义共收到 10 次回调。方案先于代码保存并回读，见 `docs/harness-two-fixes-plan-2026-10-23.md`。

此前信任登记只新增 6 个精确定义记录；本次修复没有再改用户配置、hook 定义或信任。未运行 lint、脚本 lint、vet、modern、全量 race、覆盖率、Go 构建/测试，也未修改 Go 业务代码、模板源、CI 或其他 change。范围快照另发现 40 个任务外文件变化，来源未确认，未修改或回滚；因此不宣称全工作区保持不变或阶段 1 全部跨端验收通过。

## 第一阶段累计文件与实际行为

| 文件 | 已实现的变化 |
|---|---|
| `AGENTS.md`、`CLAUDE.md` | 公共正文一致，保留各自头部；统一检查授权边界和 OpenSpec 流程，未赋予自动运行受控检查的权限 |
| `.harness/scripts/check-agent-rules.py` | 只读比较两端公共正文，漂移或缺失时退出 1，不重写文件 |
| `.harness/scripts/read-error-journal.sh`、`.ps1` | 取消 240 行上限；保留原有参数和缺文件失败行为 |
| `.openspec-auto/tools/hook_common.py` | 共享 CLI 解析，支持已有 NVM 安装；显式路径失败不悄悄回落；探测与命令超时返回受控错误 |
| `.openspec-auto/tools/run_openspec.py`、`env.sh` | shell 与 Python 使用同一个 argv 入口；不再通过登录 shell 拼接执行 OpenSpec 命令 |
| `.openspec-auto/tools/healthcheck.sh` | 核对两端配置、公共正文、必要文件与真实 CLI；说明该结果不等于客户端激活 |
| `.codex/hooks.json`、`.claude/settings.json` | OpenSpec guard matcher 显式包含 `apply_patch`；其余命令及 Claude 权限配置保留 |
| `.harness/scripts/test_runtime_wiring.py` | 隔离测试规则比较、完整读取、CLI 解析/错误、参数传递、两端注册命令和 hook JSON 契约 |
| `.harness/error-journal.md` | 记录本轮发现与处置，不关闭旧的无关问题 |

`apply_patch` 原来已能通过 Codex 的 `Edit`/`Write` 别名匹配，新增显式名称用于接线核验，不能把它描述为此前完全没有匹配。`exec_command` 仍按官方 `Bash` 事件处理。[OpenAI hooks 文档](https://learn.chatgpt.com/docs/hooks)

## 环境与解析约定

- 本机 Python：`/Library/Frameworks/Python.framework/Versions/3.13/bin/python3`。
- 本机 Codex：`codex-cli 0.163.0-alpha.11.1`；功能查询为 `hooks stable true`。
- 本机 Claude：`2.1.280 (Claude Code)`，只证明版本可查询，未证明真实 hook 触发。
- 本机 Node：`/Users/xiaozhaofu/.nvm/versions/node/v24.22.0/bin/node`。
- 现有 OpenSpec：1.3.0，入口 `/Users/xiaozhaofu/.nvm/versions/node/v24.6.0/lib/node_modules/@fission-ai/openspec/bin/openspec.js`。未重新安装。

既有环境变量的用途与回落规则：

| 变量 | 用途、默认/回落与约束 |
|---|---|
| `OPENSPEC_BIN` | 现有 OpenSpec 文件路径，可用绝对路径或相对当前目录的路径；未设置/空值时先查 PATH，失败后查 `~/.nvm/versions/node/*/lib/node_modules/@fission-ai/openspec/bin/openspec.js`；显式非空路径无效则报错，不跳过它 |
| `NODE_BIN` | Node 候选路径，与 PATH 中 Node 和现有 NVM Node 一起探测，沿用原先选择最高可用版本的规则；执行 JS CLI 需要 >=20.19.0；不下载 Node |
| `OPENSPEC_PYTHON` | shell 包装入口使用的 Python；未设置/空值时查 python3，再查 python；必须可运行当前 Python 脚本，找不到则失败 |
| `OPENSPEC_TELEMETRY` | 控制 OpenSpec telemetry，未设置时为 0；沿用用户显式值，不由脚本启用 |

NVM 中多个 CLI 按安装目录的 Node 版本降序尝试；所有尝试都只使用已有文件。每个版本探测上限 2 秒，CLI 解析总预算 6 秒；共享 OpenSpec 命令上限 8 秒，超时返回错误，不冒充成功。shell 调用示例：

```bash
python3 .openspec-auto/tools/run_openspec.py --resolve
python3 .openspec-auto/tools/run_openspec.py list --json
bash .openspec-auto/tools/healthcheck.sh .
```

这些命令不会运行 Go 质量门禁。healthcheck 的 `passed` 只表示项目接线与 CLI 校验范围。

## 首轮实际验收证据（历史）

证据目录：`.cache/harness-runtime-phase1-4w0lbm8o/`。所有时间均为本机本轮命令耗时，不是性能基准。

| 命令/检查 | 实际结果 | 证据 |
|---|---|---|
| `PYTHONDONTWRITEBYTECODE=1 python3 .harness/scripts/test_runtime_wiring.py` | exit 0；18 项中 17 通过、1 跳过；unittest 12.609 秒，外层进程 12.791 秒 | `functional-tests-final.log`、`verification-commands.json` |
| `/bin/bash .openspec-auto/tools/healthcheck.sh .` | exit 0，2.725 秒；正文、两端注册和实际 CLI 均通过 | `healthcheck-final.log` |
| `python3 .openspec-auto/tools/run_openspec.py validate repair-project-harness-runtime-wiring --type change --strict --json --no-interactive` | exit 0，1.116 秒；1 个 change valid、0 issues；只证明本 change schema | `openspec-validate.json` |
| 实际 reader 输出逐字节比较 | exit 0；当时 3360 行、159577 字节，与文件完整一致，包含 ERR-20261023-105；之后追加处置使行数变化不代表截断 | `scope-and-reader.json` |
| 对原有修改及旧 OpenSpec 文件的 SHA-256 比较 | 排除本轮新建计划后，690 个任务外文件无变化 | `unrelated-before.json`、`final-scope.json` |
| Codex 本机 app-server `hooks/list`，参数 `cwds: [项目绝对路径]` | 请求成功；6 个定义，0 errors、0 warnings；全部 untrusted | `codex-hooks-list.json` |

功能测试覆盖：正文相等/漂移/缺正文、reader 长文件/空文件/缺文件/带空格路径、显式 CLI/PATH/NVM、PATH 不可用后 NVM 回落、Node 不兼容、CLI 非零/超时/缺失、shell 特殊字符原样传递；两份配置中的真实 hook 命令分别接收 SessionStart、UserPromptSubmit、PreToolUse、Stop JSON，验证已有的预备状态拒绝/放行与安全拒绝输出。危险字符串仅作为 JSON 输入，未执行其中的命令。

上述 hook 测试在临时目录与假 CLI 中执行，不证明真实客户端已调用。PowerShell 测试因没有 `pwsh` 跳过；实际源码已回读，但不声称 Windows/PowerShell 已验收。

## 运行端信任与实际触发续验（历史失败，保留原始结果）

| 事件/用途 | 本轮真实 Codex 状态 |
|---|---|
| SessionStart / OpenSpec context | trusted；实际完成，1348 ms，包含现有 active change 上下文 |
| SessionStart / 错误摘要 | trusted；实际完成，125 ms，包含未关闭错误摘要 |
| UserPromptSubmit / OpenSpec router | trusted；实际完成，832 ms，输出路由结果 |
| PreToolUse / OpenSpec guard | trusted；对 3 次调用均有回调，2 次完成、1 次拒绝临时补丁 |
| PreToolUse / harness 安全 guard | trusted；对 3 次调用均有回调，2 次完成、1 次拒绝危险分支不可达的负例 |
| Stop / OpenSpec completion guard | trusted；实际完成，123 ms |

Codex 官方要求审阅并信任精确 hook 定义，新的或改变后的定义未信任时跳过执行；CLI 的 `/hooks` 提供此入口。[官方信任说明](https://learn.chatgpt.com/docs/hooks)

用户确认后，使用本机 CLI 的原生 `/hooks` 界面登记。写前通过 `hooks/list` 确认该实例仅列出本项目的同 6 条定义，哈希与前轮一致；写后回读确认只新增对应 `hooks.state.<精确key>.trusted_hash`。没有把其他目录或今后的任意定义一起信任。

随后通过本机 `codex app-server` 的临时会话执行一次最小模型回合，记录 10 条 `hook/completed` 事件（6 条不同定义），而不是直接调用 Python hook 的夹具。正常 `printf` 实际 exit 0 并输出 `HARNESS_RUNTIME_CANARY`；临时 `canary.txt` 补丁被 OpenSpec guard 拒绝，文件没有创建；`false && git reset --hard` 被安全 hook 拒绝，且 shell 左侧恒假保证危险分支不可达。没有重试或变换被拒绝的操作。

该回合已记录 `HOOK_RUNTIME_ACCEPTANCE_FINISHED` 最终回复与 Stop 完成，但临时采集器等待 `turn/completed` 120 秒后 exit 1；事件流还出现模型流重连。未确认终结通知缺失的根因，不把它推定为 hook 故障或忽略失败。

续验证据目录：`.cache/harness-runtime-phase1-4w0lbm8o/trust-acceptance/`。

| 证据 | 说明 |
|---|---|
| `hooks-before.json`、`hooks-after.json` | 精确 key/hash 从 untrusted 到 trusted，定义保持不变 |
| `trust-before.json`、`trust-after.json` | 仅目标 6 项新增；其他 42 条信任及其余用户配置摘要不变 |
| `runtime-events.json` | 原生运行端的开始/完成/拒绝通知、实际命令输出与最终回复 |
| `runtime-evidence-summary.json` | 6 条不同定义、10 次回调，保留 collectorExitCode=1 与未收到终结通知的事实 |
| `project-before.json`、`openspec-state-before.json` | 1203 项保护文件和已有会话状态核对无变化；仅删除本验收会话新建的 request-scope 文件 |

该轮信任登记和 6 条实际触发的获准工作已完成；当时两项问题未解决，OpenSpec change 未整体归档。后续修复见下一节。Claude 的真实客户端回调、当前桌面聊天回调以及 PowerShell 执行仍未验证，不能用本机 Codex 实测替代。

## 本次两项修复及原生重验

文档误拦根因是 `documentation_tool_targets()` 仅从 `patch/input` 读取补丁，遗漏标准 `tool_input.command`。未识别文档目标后，整个补丁进入通用分类；控制行中的 Patch 本身即命中动作词，无须正文包含 `.go`。修复只增加 `command` 来源，既有文档扩展名、路径归属及配置文本例外保持；两端配置命令均验证了纯文档放行和混合代码拒绝。

采集器根因由本机管道复现：`text=True` 的 Python 缓冲预读第二条 JSON 后，底层 fd 没有新事件；旧循环每次先 select，导致缓冲行不再被消费。改用无文本包装的二进制管道，先消费单一字节缓冲中的完整行，再等待 fd；覆盖分片 UTF-8、同批多行、EOF、坏 JSON 和超时。成功仍要求当前 threadId/turnId 的完成通知，不增加 120 秒期限，不以最终回复或 Stop 代替。历史缺少原始字节，不能断言旧回合一定发出了未采集的通知。

本轮证据根目录：`.cache/harness-runtime-phase1-4w0lbm8o/trust-acceptance/repair-105-107/`。环境沿用本机 Python 3.13.11、Codex 0.163.0-alpha.11.1、OpenSpec 1.3.0；未安装依赖。

| 实际命令/操作 | 结果 | 本轮证据 |
|---|---|---|
| `PYTHONDONTWRITEBYTECODE=1 python3 .harness/scripts/test_runtime_wiring.py DocumentPatchTests HookCommandTests` | exit 0；10 项通过，10.789 秒；含隔离夹具 healthcheck 正反例 | `targeted-tests.json` |
| `PYTHONDONTWRITEBYTECODE=1 python3 .cache/harness-runtime-phase1-4w0lbm8o/trust-acceptance/test_actual_runtime.py` | exit 0；8 项通过，0.013 秒 | `targeted-tests.json` |
| `PYTHONDONTWRITEBYTECODE=1 python3 .cache/harness-runtime-phase1-4w0lbm8o/trust-acceptance/actual_runtime.py --evidence-dir .cache/harness-runtime-phase1-4w0lbm8o/trust-acceptance/repair-105-107/native-run` | exit 0；回合 42.783 秒，status=completed | `native-run/protocol.jsonl`、`native-run/runtime-summary.json` |
| `PYTHONDONTWRITEBYTECODE=1 python3 .openspec-auto/tools/run_openspec.py validate repair-project-harness-runtime-wiring --type change --strict --json --no-interactive` | exit 0；valid=true，issues=[]；只证明 schema | `openspec-validate.json` |
| 538 个任务外文件的范围比较 | 断言 exit 1；40 个文件变化，来源未确认；用户配置及 hook 定义摘要未变 | `protected-before.json`、`native-result.json`，ERR-20261023-109 |

原生会话 `01a16724-ce0f-7310-8b09-960f8b5883cd`、回合 `01a16724-ce8a-7552-b013-75646fe9bb14` 的匹配终结通知已落入完整协议日志。6 条 hook 定义共 10 次完成/拒绝回调：两条 SessionStart、UserPromptSubmit、三次调用分别触发的两条 PreToolUse，以及 Stop。固定 printf exit 0；canary 补丁成功且内容为 `HARNESS_RUNTIME_CANARY\n`；不可达危险分支负例仍被安全 hook 拒绝。实际事件没有范围外工具调用。只清理了内容核对过的 canary 和该回合新建的 request-scope，原有会话状态核对未变。

105/107 按本轮专项证据处置；40 个任务外文件的变化仅报告，未读取业务内容进行诊断、修改或回滚。本次未重跑首轮完整测试；healthcheck 仅随 HookCommandTests 在隔离夹具中执行，未单独重跑本机项目接线检查。没有推进其他阶段或将原总体验收改为全部通过。

## 本轮只报告的其他问题

1. 当前全局 selected change 状态、基于文本的绕过/完成判断、skills 实际读取证明等仍属于后续阶段，不因本轮接线修复而自动具备强制保障。
2. 模板源没有同步本轮项目定制；治理文件仍被本地 Git 排除。未改分发策略或 CI。
3. 旧的 cmd 测试超时和 lint 2.14.0/项目要求 2.15.0 差异保留，证据来自前一任务，本轮没有重新运行或修复。
4. 范围快照中的 40 个任务外文件变化来源未确认，详见本轮 `native-result.json`；不推定为本次修复造成，也不擅自回滚。ERR-20261023-109 保持 open。

## 回滚与范围保护

`before/` 与 `manifest.json` 保存本轮原文件；`project-changes.diff` 与 `project-changes.json` 用于查看被 Git 排除的改动。恢复前先核对无后续改动，再只恢复 manifest 中本轮修改的原文件、删除本轮新文件。不要对仓库执行 reset/clean；错误记录保留并追加处置。

此前信任登记仅对应 gin_api 的 6 条定义；本次两项修复仅使用既有信任。`/Users/xiaozhaofu/Ai/harness-engineering`、`/Users/xiaozhaofu/Ai/openspec-auto-bootstrap`、全局 skills 及用户配置未改。新增的两项修复备份位于 `repair-105-107/before/`、清单位于 `before-manifest.json`；恢复前须确认没有后续修改，只恢复本轮文件，不覆盖上述来源未确认的变化。

收尾 `git status` 另外显示新的 `docs/logger-unification-plan-2026-10-23.md`；它不在本轮最初清单中，也不是本轮创建，来源未确认，未读取或修改。

合规核对：两项修复功能及原生重验通过，范围快照断言未通过，分别报告；无依赖安装、容器、生产操作或受控检查。验收采用 `backend-acceptance` 的证据边界，因只改开发工具，业务 API、数据库、事务与服务容量检查不适用。PowerShell、Claude 和当前桌面聊天回调仍未验证；不将本次专项通过扩大为所有运行端或全工作区的整体准入。

此前范围证据修正（历史）：备份创建于方案首次落盘之后，原始 691 项快照含当时方案。最终断言曾因该方案后续更新而失败；未改写原始快照，按 manifest 排除本任务文件后重新核对，当时 690 个任务外文件全部保持原值（ERR-20261023-106）。
