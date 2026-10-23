# Harness 第二阶段：会话契约与可复核执行方案

日期：2026-10-23。状态：方案已落盘并完整回读；第二阶段实现、功能回归和最终原生验收完成。

## 1. 需求、授权与原计划对应

来源：用户要求“开始第二阶段，先核对原计划，补齐完整方案和实施计划并落盘，再按计划实施；第三阶段不动，其他问题只汇报，不运行未授权检查”。本次授权包含本阶段方案落盘、实现与对应功能验收；不包含受控检查或全局配置变更。

原计划为 `docs/harness-openspec-skills-improvement-plan-2026-10-23.md` 第 4 节的第二阶段五步。严格保留顺序：2.1 规则/证据 schema → 2.2 会话状态及迁移 → 2.3 改动前计划与授权门禁 → 2.4 技能/guide 与完成证据 → 2.5 错误回归及实际验收。第一阶段已按现有运行端证据收尾；不借本阶段重做第一阶段或整理旧 change。

目标：让已配置的六条 hook 在实际触发时，对当前会话的阶段、计划、文件目标、受控命令和交付证据做明确检查；任何机器结论说明所依据的文件/命令和局限。不存在会话身份、状态损坏或命令不支持时，不借用其他会话的授权。

非目标：不实现第三阶段的 CI、分发、模板回流、存量 change 对账；不改 Go 业务、依赖、目录架构、安全 hook 的破坏性操作规则、hook JSON 定义或用户信任；不安装工具，不运行 lint、脚本 lint、vet、modern、race、覆盖率或聚合门禁；不声称实现不可绕过的安全沙箱或证明模型理解。

## 2. 已读取事实

- `.openspec-auto/tools/hook_common.py`：`request-scope-<sha256(session)>.json` 已隔离请求分类；`current_change.json` 仍全局共享，解析失败还删除它。编辑正文经 `classify_prompt()` 可触发 skip；Bash 仅检测少数写法；Stop 从全 payload 抽取 completion 词并依赖全工作区 diff/schema。
- 四个 OpenSpec hook 分别接收 SessionStart、UserPromptSubmit、PreToolUse、Stop。现有 `.codex/hooks.json` 与 `.claude/settings.json` 的六条定义一致；本阶段保留命令、matcher、timeout，避免扩大旧信任授权。
- 现有 resolver 支持本机 OpenSpec 1.3.0/NVM；本轮 `run_openspec.py list --json` exit 0。Python 沿用已使用的 3.13。无新增依赖。
- 七个旧 active change 含第一阶段未归档记录，与本阶段目标不同；只新建 `enforce-session-workflow-contract`，不修改它们。
- 原生 Codex 的 session_id 及六条回调已由第一阶段实测；当前桌面聊天、Claude 原生端、Windows/PowerShell 的实际回调仍未验证。本阶段只有取得相应真实证据才更新结论。
- 工作区存在并行任务。基线和证据按本阶段明确文件核对；不把任务外变化当成本任务越界，也不擅自回滚。

## 3. 机械规则及保留的人工判断

| 项目 | 自动检查 | 局限/人工判断 |
|---|---|---|
| 用户意图 | 只解析 UserPromptSubmit 的 prompt；剔除代码围栏、引用行、引号例子，按肯定/否定子句识别支持的中文/英文请求；工具正文不授予 skip/执行/检查权限 | 不能理解任意自然语言；不确定时停在计划/只读，提示明确指令 |
| 授权时机 | 代码授权与受控检查授权分开；受控检查只接受本会话功能完成之后的新肯定请求；否定/停止清除对应授权 | 用户明确范围与需求语义仍人工核对；其他聊天授权不能继承 |
| 计划与文件 | 计划 JSON 契约有效、阶段明确、当前 session 已绑定；实际全部文件目标在精确清单内；代码编辑要求 apply-ready 或用户明确 skip | 文档/注释请求不强制 OpenSpec；机器不证明文字方案合理 |
| 命令 | 识别有限的只读 argv；实际检查通过注册的 argv runner 执行；不解析的脚本/复杂 shell 默认拒绝，不猜其无害 | 不支持任意 shell 解释或抵抗可改写 hook 的恶意宿主；运行端权限仍必要 |
| 上下文 | 声明的 rules/guide/skill 路径可读；项目双端 skill 正文相同；专用读取入口记录文件哈希及实际输出区间 | 只证明程序读取并输出，不证明模型消费/理解，也不伪称自动加载所有技能 |
| 完成 | 当前契约、代码文件哈希和必需功能检查的真实 exit 0 记录吻合；schema 验证不能冒充测试；未授权检查列未执行 | 业务正确性、容量、发布判断仍人工；不以“完成”词或手填 passed 取代证据 |
| 错误回归 | 契约声明错误 ID、复现说明及检查 ID；完成报告列对应真实结果 | 不自动关闭无证据旧错误，不因文档声明闭环就标通过 |

## 4. 接口、状态及兼容契约

新增 `.openspec-auto/tools/workflow_state.py`，供已有 hooks 与 `.harness/scripts/workflow.py` 共用。只用标准库；状态放现有 `.openspec-auto/state/`，不调整目录层级。

状态名 `workflow-<sha256(session_id)>.json`。schema_version=1，包含 canonical repo、session digest、revision、request mode、肯定授权及撤销、绑定计划/阶段/change、已读取上下文、编辑目标、检查运行状态与结果、功能完成及交付状态。写入同目录唯一临时文件后原子替换；同会话读改写用 POSIX flock，最多等待 2 秒，不自动抢锁；Windows 运行未验证且不以无锁模式回落。不同会话用不同锁。状态读写失败会使代码/检查门禁拒绝；只读和纯文档不因此恢复旧任务。

旧 `current_change.json`、旧 request-scope 保留原样，不迁移其授权，不作为新的 fallback。新会话只有真实 UserPromptSubmit 创建请求状态。SessionStart 只展示本会话状态与现有 active change，不替别的会话选择、删除或覆盖状态。CLI 必须显式 `--session`；原生 hook 从 payload 获取身份，命令中其他 session 被拒绝。

计划在 Markdown 中使用唯一 harness-contract:v1 注释标记后的 JSON fenced block。契约字段固定：schema_version、stage、change（或 null）、allowed_files（精确相对路径及 code/document 类型）、required_context（kind/path/name）、checks（id/kind/argv/timeout_seconds）、regressions（error_id/reproduction/checks）。拒绝重复 ID、未知字段、越界路径、符号链接逃逸、空代码清单、重复契约块、不存在的上下文、未登记的回归检查。文档中的其他 JSON 示例不当契约。契约哈希用规范 JSON；进度文字不改变批准的机器范围，修改契约必须重新绑定、读取并清除旧证据。

CLI 的固定动作：

- `--session ID read --plan PATH --path PATH [--start-line N --line-count N]`：仅输出计划或声明上下文的实际行；每份文件按哈希累计已输出区间，未完整覆盖不能作为完整读取。计划可先读取再绑定。
- `--session ID activate --plan PATH`：检查当前请求有实施授权、计划及必要上下文已完整输出、项目 skill 配对一致、change apply-ready（除本会话用户明确 skip）；保存契约与代码基线，不执行检查。
- `--session ID run --check ID`：只执行契约中该 ID 的 argv；shell=False，固定项目 cwd，真实记录开始/退出/超时、输出文件及哈希、契约及代码哈希。日志使用现有 state 目录下本 session 的唯一文件名，不覆盖历史结果。运行未结束不产生通过记录；同会话 runner 在解锁执行前标 running，收尾按 revision 校验，防止旧进程覆盖新请求。运行中轮询当前 revision，撤销/新请求时只终止本次子进程组并记录 canceled；超时同样终止并保留非通过结果。
- `--session ID ready`：所有必需功能检查在当前代码哈希下通过后才标功能完成，作为以后另行授权受控检查的时间边界；本命令不授予检查权限。
- `--session ID finish`：核对上述证据并生成交付 JSON，受控检查未授权明确列未执行；回归 ID 关联对应检查结果；不按手填 boolean 判断通过。
- `--session ID checkpoint --reason TEXT`：仅将当前工作标为未完成的安全停点，让 Stop 允许报告进度/待用户输入；不标功能或交付完成。
- `--session ID status`：只读输出当前状态，不修复损坏状态、不读取其他会话。

有限命令分类：读文件/检索、git status/diff/log/show/ls-files/rev-parse 为只读，禁止其写出选项和重定向；printf/echo 的引用文本不视为命令。已登记 `python[3] <项目 test*.py>`、`go build`、不带 race/cover 且显式 `-vet=off` 的 `go test` 可归功能检查；schema 仅归 schema。lint、脚本 lint、vet、modern、race、coverage 及 make 聚合目标归受控类别；识别 argv 而非扫描引号正文。未知命令、shell -c、解释器 -c/ heredoc、管道或重定向写入不能借功能标签通行。直接运行注册检查仍要求经过 runner，以保留真实结果；Bash 写代码改用支持的文件工具。代码目标的 Add/Update/Delete/Move 全部校验；正文不用于授权。

Stop 不再搜索整个 payload 的“完成”词，也不看其他任务的 git diff。只读/文档请求直接放行且不验证旧 change；当前代码任务有绑定计划时，完整证据或显式未完成 checkpoint 才允许收尾。已完成后代码变化使旧记录失效；新请求/撤销使迟到结果不能恢复权限。

## 5. 本阶段文件范围

仅改/新增以下项目文件（以及当前新 change artifacts、限定状态/备份/证据）：

- `.openspec-auto/tools/workflow_state.py`、`.harness/scripts/workflow.py`。
- `.openspec-auto/tools/hook_common.py` 与 `hooks/openspec_context.py`、`openspec_router.py`、`openspec_stop.py`；`openspec_guard.py` 只在错误信息/导入接线需要时改。
- `.harness/scripts/test_workflow_contract.py`、`test_runtime_wiring.py`。
- `.harness/guides/workflow-contract.md`。
- 两端 `harness-plan/SKILL.md`、`harness-implement/SKILL.md`；只同步本阶段契约与命令，保留原技能职责，不全量重写。
- 本方案、第二阶段验收报告、原总方案中第二阶段状态、`.harness/error-journal.md`。

不改 `AGENTS.md`/`CLAUDE.md` 公共规则、`.codex/hooks.json`、`.claude/settings.json`、安全 hook、模板源、全局 skills/信任、CI/Makefile 或第三阶段。当前代码实现身份/原生回调未知处不编造。

读取默认每段 200 行，可显式指定 1–1000 行；单文件/契约上限 1 MiB。检查 timeout_seconds 必须为 1–300，不能无限等待。阶段和检查 ID 使用非空短字符串；会话 ID 必须来自当前 payload 或明确 CLI 参数，不能从进程 PID、cwd 或旧全局 state 推导。

本阶段机器契约如下，未来模板示例使用同一字段；文档部分不作为功能指纹，纯报告更新不迫使重跑测试。

<!-- harness-contract:v1 -->
```json
{
  "schema_version": 1,
  "stage": "2",
  "change": "enforce-session-workflow-contract",
  "allowed_files": [
    {"path": ".openspec-auto/tools/workflow_state.py", "kind": "code"},
    {"path": ".harness/scripts/workflow.py", "kind": "code"},
    {"path": ".openspec-auto/tools/hook_common.py", "kind": "code"},
    {"path": ".openspec-auto/hooks/openspec_context.py", "kind": "code"},
    {"path": ".openspec-auto/hooks/openspec_router.py", "kind": "code"},
    {"path": ".openspec-auto/hooks/openspec_stop.py", "kind": "code"},
    {"path": ".openspec-auto/hooks/openspec_guard.py", "kind": "code"},
    {"path": ".harness/scripts/test_workflow_contract.py", "kind": "code"},
    {"path": ".harness/scripts/test_runtime_wiring.py", "kind": "code"},
    {"path": ".harness/guides/workflow-contract.md", "kind": "document"},
    {"path": ".agents/skills/harness-plan/SKILL.md", "kind": "document"},
    {"path": ".agents/skills/harness-implement/SKILL.md", "kind": "document"},
    {"path": ".claude/skills/harness-plan/SKILL.md", "kind": "document"},
    {"path": ".claude/skills/harness-implement/SKILL.md", "kind": "document"},
    {"path": "docs/harness-phase2-workflow-plan-2026-10-23.md", "kind": "document"},
    {"path": "docs/harness-phase2-acceptance-2026-10-23.md", "kind": "document"},
    {"path": "docs/harness-openspec-skills-improvement-plan-2026-10-23.md", "kind": "document"},
    {"path": ".harness/error-journal.md", "kind": "document"}
  ],
  "required_context": [
    {"kind": "rules", "path": "AGENTS.md", "name": "project-rules"},
    {"kind": "guide", "path": ".harness/guides/workflow-contract.md", "name": "workflow-contract"},
    {"kind": "skill", "path": ".agents/skills/harness-plan/SKILL.md", "name": "harness-plan"},
    {"kind": "skill", "path": ".agents/skills/harness-implement/SKILL.md", "name": "harness-implement"},
    {"kind": "skill", "path": "/Users/xiaozhaofu/.agents/skills/senior-python-engineer/SKILL.md", "name": "senior-python-engineer"}
  ],
  "checks": [
    {"id": "phase2-functional", "kind": "functional", "argv": ["python3", ".harness/scripts/test_workflow_contract.py"], "timeout_seconds": 180},
    {"id": "phase1-regression", "kind": "functional", "argv": ["python3", ".harness/scripts/test_runtime_wiring.py"], "timeout_seconds": 180},
    {"id": "phase2-schema", "kind": "schema", "argv": ["python3", ".openspec-auto/tools/run_openspec.py", "validate", "enforce-session-workflow-contract", "--type", "change", "--strict", "--json", "--no-interactive"], "timeout_seconds": 30}
  ],
  "regressions": [
    {"error_id": "ERR-20261023-105", "reproduction": "标准 command 文档补丁不因正文或控制行误拦", "checks": ["phase1-regression", "phase2-functional"]},
    {"error_id": "ERR-20261023-109", "reproduction": "不同会话/并行任务不共享授权，不以其他文件变化拒绝当前交付", "checks": ["phase2-functional"]}
  ]
}
```

## 6. 顺序实施计划

必要技术 skill 可按当前会话已提供的安装路径声明为绝对路径；只允许当前用户 `.agents/skills` 或 `.codex/skills` 内的现有 SKILL.md，不扫描目录、不读任意项目外文件，不修改安装副本。项目内 skill 仍要求两端配对。此处把已选用的 Python 技术 skill 纳入读取证据；这不是新增安装或跨项目维护授权。

| 步骤 | 前置 | 具体改动 | 检查及证据 | 停止条件 | 状态 |
|---|---|---|---|---|---|
| 2.1.1 | 原计划/规则/错误记录已读取 | 保存本方案并回读；确定有限规则与契约示例 | 原五步逐项对应，无未决实现选项 | 范围不一致 | 完成 |
| 2.1.2 | 方案回读 | 新建唯一 `enforce-session-workflow-contract`，填写 proposal/design/spec/tasks | 当前 change apply-ready；不处理七个旧 change | CLI 失败/含混 | 完成 |
| 2.2.1 | apply-ready | 实现版本化状态、锁、原子替换和 revision；旧状态只读保留 | 隔离测试：双会话、同会话并发、损坏/缺失、撤销、迟到结果、无身份 | 串会话/数据丢失 | 完成，含双进程测试 |
| 2.2.2 | 状态模块完成 | router/context 只接本次 prompt 和本会话 selected change；否定/引用隔离 | 中文复合、英语、代码块、否定、无关新请求、明确继续/skip 的表驱动反例 | 正文授予权限 | 完成 |
| 2.3.1 | 状态可用 | 契约读取/绑定、计划范围及全部目标校验 | 未读/无计划/损坏/非 apply-ready 拒绝；纯文档放行；越界/move/混合目标反例 | 放宽用户边界 | 完成 |
| 2.3.2 | 绑定可用 | 有限 argv 分类与检查 runner，功能完成后另行授权 | 假子进程记录未授权命令从未启动；聚合/标签伪装/停止/延迟授权反例 | 未授权真实检查启动 | 完成 |
| 2.4.1 | runner 可用 | 上下文区间与哈希证据、双端 skill 配对；更新指南及两端技能 | 缺路径、配对漂移、只读一段、读取后变化；正常完整读取 | 伪称模型理解 | 完成 |
| 2.4.2 | 读取/运行证据齐 | finish/ready/checkpoint 与 Stop 接线 | 伪造 passed、运行中、非零/超时、旧哈希、错 session、仅 schema 通过不可交付 | 无证据交付 | 完成 |
| 2.5.1 | 功能实现完成 | 回归 ID 与检查映射、针对旧错误复现及反例；调整第一阶段相应测试期望 | 本阶段及受影响 Python 功能测试；不运行任何受控检查 | 同问题三轮失败/未知原因 | 完成；21 项通过，原套件 22 通过/1 跳过 |
| 2.5.2 | 功能测试通过 | 用现有六条可信 hook 进行限定原生 Codex 回合；保留完整协议 | 会话隔离、正常文档、拒绝越界/未授权检查、真实 runner 结果与 Stop；只用临时 canary | 信任定义漂移/范围外命令/未知运行端 | 完成；最终回合 50.118 秒、exit 0、14 次回调 |
| 2.5.3 | 有真实结果 | 范围回读、当前 change 严格 schema 校验、验收报告/计划/错误处置；仅当前完成 change 按既有工具流程收尾 | 命令/退出码、文件/历史证据、未验证项；第三阶段不变 | 缺证据不勾选 | 完成；当前 change 已单独归档，归档后定向 schema 校验通过 |

## 7. 验收、风险、失败与回滚

功能验收命令仅为 Python 本阶段/受影响测试与当前 change 的 schema 校验。假 go/make/解释器位于隔离临时目录，只验证分类/调用次数，不运行 Go 门禁。原生验收在项目 `.cache` 中使用精确 canary 计划/文件/轻量 Python 检查，不运行真实受控检查；一次否定路径被拒绝后不换写法重试。若六条信任发生变化，报告并停在该验收，不改全局配置。

验收属性：不同 session 永不共享计划/change/授权；只读/文档新请求不续跑旧任务；不能通过工具正文或引号授予 skip/检查；计划范围先于源码写入；每份必需上下文有完整实际读取证据；真实失败/超时/运行中/旧 revision/旧代码不得成为通过；Stop 不把 schema 或“已完成”当功能证据；回归 ID 可追到具体失败输入及结果。

风险：保守命令识别会拒绝未支持 shell 结构，须使用已有文件工具/登记 runner，不绕过；工作区共享时相同文件被其他会话改动会让证据失效，报告重叠而非覆盖；文件锁故障只报告，不删除不明锁；本地状态可被宿主改写，不能作为对恶意操作者的安全边界。真实用户语义、模型理解、所有客户端触发和发布安全不在机械证明范围。

实施前将限定原文件及哈希备份到 `.cache`，记录本阶段状态文件名。回滚仅恢复该清单中的本轮修改且先核对后续变化，删除本轮新文件/本轮 state；保留旧 current_change、其他 session、并行日志改造、错误与验收历史。禁止 reset/clean、批量删除 state、调整用户信任或重建目录。

## 8. 执行记录

备份及证据目录：`.cache/harness-phase2-ufkun4cw/`。方案先于代码完整回读，当前 change apply-ready 后才实施。最新第二阶段功能测试 21 项通过（2.685 秒）；受影响原测试 23 项中 22 通过、PowerShell 1 项按缺少运行时跳过（16.470 秒）。当前 change 严格 schema 校验 exit 0。最终代码版本原生验收 exit 0、50.118 秒、14 次回调，会话 `01a16750-51ef-7321-98fd-c7e4a0f1395e`；越界文件和未授权假检查标识均不存在；详见 `docs/harness-phase2-acceptance-2026-10-23.md`。

实际失败保留：118/121/126 为补丁上下文或顺序错误；119 为新夹具遗漏导入时捕获的执行环境；124 包括否定句与测试归属错误，另一次 SessionStart 上下文断言未保存诊断、暂未复现；125 为 macOS 规范路径夹具断言。明确问题已修正，处置以实际最终验证为准，不把失败日志替换成成功。不运行 lint、脚本 lint、vet、modern、race、覆盖率；假的 Go 程序仅验证授权分支。

收尾：本 change 已通过 `archive enforce-session-workflow-contract --skip-specs -y` 归档到 `openspec/changes/archive/2026-10-23-enforce-session-workflow-contract/`；仅对该归档路径再次执行严格 schema 校验，exit 0、issues=[]。118/119/121/125/126 已关闭，124 的已确认缺陷修复，但旧偶发断言原因仍未确认，保留 open。七个旧 active change 和第三阶段未处理。
