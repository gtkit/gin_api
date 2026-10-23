# Harness 第二阶段验收记录

日期：2026-10-23。对应方案：`docs/harness-phase2-workflow-plan-2026-10-23.md`。范围只含原总方案第二阶段。

## 当前结论

第二阶段实现、定向功能测试、受影响回归及最终原生重验已完成。最终原生回合 50.118 秒完成、exit 0；6 条定义共 14 次回调，正常写入/真实执行/Stop 通过，越界文件与未授权假检查标识未创建。第三阶段、Go 业务、依赖、CI、全局配置和六条 hook 定义未改。受控检查没有运行。

## 实现与可检查边界

| 能力 | 本阶段实际实现 | 不扩大宣称 |
|---|---|---|
| 会话状态 | session 哈希文件、版本/revision、原子替换、POSIX 有界锁；旧全局状态不作回落 | 不保证 Windows 的锁行为 |
| 授权 | 仅当前用户 prompt 的支持语法；否定/代码块/引用不能授予；明确继续才恢复实施 | 不证明任意自然语言语义 |
| 计划 | 单一 JSON 契约、精确文件和阶段、apply-ready/当前显式 skip、完整读取区间与内容哈希 | 不证明方案业务设计正确 |
| 技能/guide | 声明路径及名字、项目两端配对、安装技术 skill 白名单路径、完整读取证据 | 不证明模型理解或所有客户端自动加载 |
| 检查 | 固定 argv runner、真实 stdout/stderr 文件、退出/取消/超时、契约与代码/检查脚本指纹 | 不是任意 shell 沙箱；假程序验证不能代表真实 Go 检查 |
| 完成 | ready/finish/checkpoint、必需功能/schema 实际记录、错误 ID 映射、旧结果失效 | 不把 schema、手写 passed 或“已完成”文本当作功能测试 |

具体接线：四个已有 OpenSpec hooks 的内部逻辑改用 `workflow_state.py`；新增 `.harness/scripts/workflow.py`，新增指南并同步项目两端 harness-plan/implement。没有新增 hook 定义或改动用户级信任。

## 已执行的验证

证据目录 `.cache/harness-phase2-ufkun4cw/`；Python 3.13，现有 OpenSpec 1.3.0。本表均为实际执行，未列命令没有自动执行。

| 命令 | 结果 | 证据 |
|---|---|---|
| `PYTHONDONTWRITEBYTECODE=1 python3 .harness/scripts/test_workflow_contract.py` | 21 项通过，exit 0，2.685 秒 | `functional-final3.log` |
| `PYTHONDONTWRITEBYTECODE=1 python3 .harness/scripts/test_runtime_wiring.py` | 22 通过、1 项 PowerShell 跳过，exit 0，16.470 秒 | `runtime-wiring-final2.log` |
| `PYTHONDONTWRITEBYTECODE=1 python3 .openspec-auto/tools/run_openspec.py validate enforce-session-workflow-contract --type change --strict --json --no-interactive` | exit 0，当前 change valid，无 issues | `openspec-validate.json` |
| 原生 Codex 首轮限定会话 | exit 0；六条定义、14 次回调；正常 canary 成功，越界拒绝，未授权假检查未启动，真实 functional receipt 与 Stop 完成 | `native-run/protocol.jsonl`、`native-run/summary.json` |
| 最终版本原生重验 | exit 0，50.118 秒，14 次回调；会话 `01a16750-51ef-7321-98fd-c7e4a0f1395e`，回合 `01a16750-526b-7070-a79b-8a09fe6c5799` | `native-final/protocol.jsonl`、`native-final/summary.json` |
| `PYTHONDONTWRITEBYTECODE=1 python3 .openspec-auto/tools/run_openspec.py validate archive/2026-10-23-enforce-session-workflow-contract --type change --strict --json --no-interactive` | 归档后仅校验本 change，exit 0，valid=true、issues=[] | `openspec-archived-validate.json` |

功能测试覆盖：不同会话与双进程同会话、缺失/损坏状态、否定/引用/显式继续、计划/目标越界与 symlink、部分/失效读取、技能配对、伪造结果、非零/超时/取消、旧 revision 不回写、改代码或测试后旧结果失效、同一会话先完成功能再授权、未授权子进程未启动、两客户端命令及 Stop 接线。安全假程序只在隔离目录写固定标识，不运行真实 Go/lint/vet/modern/race/覆盖率。

## 失败与保留项

所有失败原日志保留：补丁顺序错误 118/121/126；夹具环境问题 119；否定句和测试归属问题 124；macOS 规范路径比较问题 125。确定问题均已修正并纳入回归。124 中另有一次原 SessionStart 上下文断言失败，当次未输出具体上下文，单独复跑及后续整套已通过，原因不能倒推，保留未确认记录。

原有业务测试/工具版本/下载问题不在本阶段处理范围；不重跑它们、不借别的聊天授权执行检查。PowerShell、Claude 原生客户端以及当前桌面聊天的实际 hook 回调仍未验证。无部署/生产准入结论。

## 回滚与交付

`final-evidence.json` 记录最终原生命令：读计划/规则并激活 exit 0；未授权假检查由 runner 在启动前拒绝、exit 1；普通检查及 ready/finish exit 0。越界补丁由 PreToolUse 拒绝；全部实际命令与文件事件保留。假检查的 exit 1 是预期反例，不是漏记的验收失败。

范围回读：两端 hook JSON、安全 hook、AGENTS/CLAUDE 及原有两个 request-scope 文件哈希未变；第三阶段文本与实施前备份一致。改动清单和可审查 diff 位于 `final-evidence.json`、`scoped-changes.diff`。没有把其他聊天的检查或并行文件变化当成本任务执行证据。

原文件备份为本目录证据中的 `before/`、`manifest.json`；只回滚本阶段清单且先确认无后续修改，不覆盖并行日志改造、旧 change 或其他会话。功能状态由真实结果更新，不能恢复旧全局授权。最终交付前回读范围、保留未验证项并只收尾当前 change；第三阶段不动。

当前 change 已单独归档为 `openspec/changes/archive/2026-10-23-enforce-session-workflow-contract/`。归档输出明确成功，且归档后定向 schema 校验通过；使用 --skip-specs，未整理或验证其余旧 change。第二阶段在上述已验证边界内交付，124 中的历史偶发断言和跨端未验证项如实保留。

## 2026-10-23：124 偶发项专项调查

用户要求先查清根因再修复。本轮原用例顺序 12 次、有界并发 8 次均通过；按原两套 Python 测试并行方式对照，接线套件 22 通过、1 跳过（15.264 秒），会话契约 21 项通过（2.648 秒），均 exit 0。以上是修复前调查结果，没有代码修改，不是修复后验收。

隔离故障注入证实：探测超时、list 非零退出及非 JSON 输出都能造成历史日志里的同一断言失败；旧日志缺少当次上下文，不能唯一归因。未调整超时、增加重试或放宽断言，124 保持 open。原生客户端没有因本轮通过结果获得新的验收结论。

完整事实、证据路径、修复准入与后续步骤见 `docs/harness-sessionstart-fixture-repair-plan-2026-10-23.md`；证据目录 `.cache/harness-sessionstart-fix-20261023-stjw7er0/`。第三阶段及受控检查没有推进。

## 2026-10-23：最小失败现场采集

按用户追加授权，仅新增测试辅助 `fixture_diagnostics.py` 并接入 `HookCommandTests`。失败时在临时目录清理前保留完整 hook 输入/输出、实际假 CLI argv、退出码、耗时与异常；stderr 显示 `.cache/harness-fixture-failures/` 下的唯一绝对路径。正常返回及异常原样透传，超时/启动失败不编造退出码，成功用例不留持久记录。生产脚本、hook 定义、超时和重试不变。

定向命令 `python3 .harness/scripts/test_runtime_wiring.py FixtureDiagnosticsTests HookCommandTests` 实际 13 项通过，无跳过，14.460 秒、exit 0；只对当前 `capture-runtime-fixture-failures` 做 strict schema 校验，valid=true、issues=[]。日志、runner 记录及边界快照在 `.cache/harness-fixture-diagnostics-20261023-9kovpt3c/`。采集样本在隔离测试中已验证，不等于复现历史问题。

完整方案及实施状态见前述计划第 6 节。原生客户端、无关测试、lint、脚本 lint、vet、modern、全量 race、覆盖率未运行；第三阶段未改。历史原因仍未确认，124 保持 open。

当前诊断 change 已单独归档为 `2026-10-23-capture-runtime-fixture-failures`，归档后定向 strict validate 通过；11 个生产/配置/原总计划文件哈希未变。没有整理旧 change 或继续其他阶段。
