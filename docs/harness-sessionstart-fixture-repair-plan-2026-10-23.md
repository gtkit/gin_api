# 第二阶段 SessionStart 偶发失败：调查与修复计划

日期：2026-10-23。状态：第 6 节的最小诊断采集已按先落盘、回读、实施的顺序完成，13 项定向验收通过；历史根因仍未确认，根因修复步骤 F2–F6 保持未开始。

## 1. 需求来源、目标与边界

用户要求：先查清夹具偶发失败根因，补齐修复方案和实施计划并落盘，再修复及重新验收；第三阶段不动，其他问题只汇报，不运行未授权检查。

本项属于原第二阶段 2.5 的遗留问题，来源为 `ERR-20261023-124`。原计划为 `docs/harness-phase2-workflow-plan-2026-10-23.md`，原验收为 `docs/harness-phase2-acceptance-2026-10-23.md`。不重新定义第二、三阶段，也不重新打开已归档的第二阶段 change。

目标是找到能区分真实原因的失败证据，建立可重复反例，随后只修复该原因并重新验收。重复通过不等于已修复，人工注入故障不等于历史故障的根因。

非目标：Go 业务、并行日志改造、工具升级、其他错误条目、CI、模板或第三阶段；不修改全局配置、hook 定义、信任或超时；不执行 lint、脚本 lint、vet、modern、全量 race、覆盖率或聚合门禁。

## 2. 已核实事实与缺失证据

历史原件：`.cache/harness-phase2-ufkun4cw/runtime-wiring.log`。23 项测试中 `test_client_configs_and_start_prompt_stop_contracts` 的 client=0 失败，另有 1 项 PowerShell 跳过。失败行只断言上下文中存在 `no active changes`，没有输出当次 `contexts`、CLI 返回内容或子进程耗时。

源码链路为：两个客户端配置中的 SessionStart 命令 → `openspec_context.py` → `build_session_context()` → `openspec_list()` → `run_command()`/`openspec_prefix()`。探测预算 2 秒、解析总预算 6 秒、CLI 命令预算 8 秒。遇到 `OpenSpecRuntimeError` 时输出错误上下文，hook 自身仍可以正常退出；所以单凭 hook exit 0 无法证明 CLI 成功。

当前夹具配置空 changes、假 CLI 返回固定 JSON；当前断言已附 `contexts`（上一轮已改），本轮没有修改它。缺少历史上下文，不能判断当次是否为超时、非零退出、无效输出或其他读取错误。

本轮使用现有 Python 3.13.11，只在隔离临时夹具和项目 `.cache` 保存证据；没有新增依赖或修改 Python/OpenSpec 版本。证据目录：`.cache/harness-sessionstart-fix-20261023-stjw7er0/`。

| 证据 | 本轮实际结果 | 证明范围 |
|---|---|---|
| `baseline-repetitions.json` | 原用例顺序 12 次均通过；24 次 OpenSpec SessionStart，耗时 0.233–1.111 秒 | 当前普通调度下未复现，不证明历史根因 |
| `bounded-concurrency.json` | 4 个并发上限，共 8 次原用例均通过 | 当前有界并发下未复现，不是 Go race 或容量验收 |
| `fault-matrix.json` | 注入探测超时、list 非零退出、list 非 JSON，三者均使原断言失败 | 同一历史断言不能区分这三类原因；不能据此认定历史发生了任一类 |
| `original-schedule-wiring.log/json` | 两套 Python 测试并行时，接线套件 22 通过、1 跳过，exit 0，15.264 秒 | 对照原执行方式仍未复现 |
| `original-schedule-workflow.log` | 会话契约 21 项通过，exit 0，2.648 秒 | 当前功能回归结果；不是修复后的验收 |
| `before-hashes.json`、`before/` | 保存本轮前相关脚本、配置、计划、报告、错误记录 | 支持差异核对，不能替代行为证据 |

注入细节：只修改本次临时夹具的假 CLI。`--version` 延迟 2.2 秒触发真实 2 秒探测超时；`list` 返回 7 或输出非 JSON 分别触发对应真实错误路径。未执行任何危险 payload、真实 Go 检查或外部服务调用。

结论：错误上下文被历史断言遗漏是已证实的诊断缺口；历史失败的具体原因仍未确认。当前证据不支持把它归因于 Python 冷启动、调度抖动、环境串扰或生产 resolver 缺陷，也不支持为此延长超时、增加重试或替换夹具。

## 3. 修复准入与兼容约束

进入代码修复前必须同时取得：失败输入及相关夹具快照；完整 hook stdout/stderr 和实际退出码；对应 CLI argv、实际路径及耗时/异常；能复现该同一错误路径的反例。没有这些证据时停在调查，不改运行行为。

修复必须保留：两端配置命令与信任、正常空 changes 文案、错误时明确报告且不改变状态、会话隔离、现有授权与文件范围约束。不能把真实 CLI 错误改成空 changes，也不能删除或放宽成功断言。

候选代码位置仅限已确认链路中的 `.harness/scripts/test_runtime_wiring.py`、`.openspec-auto/tools/hook_common.py`、`.openspec-auto/hooks/openspec_context.py`；这不是三文件批量修改授权。实际原因未确定，具体改动及机器契约尚不能如实填写，必须先将定位证据与准确文件/修改补入本计划并回读，再实施。若需要超出该链路或改变原兼容约束，停止并说明范围变化。

## 4. 顺序实施计划

| 步骤 | 前置条件 | 工作与检查方法 | 可复核证据/交付物 | 停止条件 | 当前状态 |
|---|---|---|---|---|---|
| F1 | 读取最新指令、原计划、规则和错误记录 | 读取历史失败与实际调用链；保存现状；有界重复、并发和错误路径对照 | 本文第 2 节、原始 JSON/日志、前置快照 | 未取得原始失败时不得声称根因确定 | 本轮调查完成，根因未确认 |
| F2 | F1 取得可区分的真实失败证据 | 固定失败输入、CLI 结果及最小反例；验证反例命中相同原因 | 失败前后输入/输出、命令和退出码 | 只有相同断言、没有相同错误原因时停止 | 未满足，停在此处 |
| F3 | F2 完成 | 补入准确修改、文件、兼容边界和机器契约；仅为该修复创建独立 OpenSpec change，核对 apply-ready；保存并完整回读 | 本计划修订、当前 change artifacts、读取证据 | 未决设计或计划不完整时不改代码 | 未开始 |
| F4 | F3 完成，仍在用户授权范围 | 先固定失败反例，再作最小修复；保持正常及错误路径断言 | 原反例先失败、修复后通过；限定 diff | 需要放宽生产超时/错误契约、越界改动或连续三轮自纠失败时停止 | 未开始 |
| F5 | F4 完成 | 执行原失败用例、两端接线套件、会话契约套件；若运行 hook 逻辑改变，再用原六条可信 hook 作对应原生验收；仅验证本 change schema | 每条实际命令、退出码、stdout/stderr、未验证项 | 失败或缺少运行端证据不写通过 | 未开始；本轮通过结果不能替代此步骤 |
| F6 | F5 有完整真实结果 | 核对本项文件差异，更新原验收与错误记录，按流程仅收尾本 change | 验收报告、错误处置、范围回读 | 无根因/修复证据不关闭 124 | 仅更新本轮调查状态，124 保持 open |

受控检查仍需功能完成后另行明确授权，不随 F5 自动执行。PowerShell 和 Claude 原生端没有本轮新证据，不标为通过；测试内的隔离 healthcheck 调用不代表对本机项目单独执行完整 healthcheck。

## 5. 风险、失败处理与回滚

最大风险是把一次成功或人工注入原因误认成历史根因，导致修错位置或掩盖有效失败。本轮通过结果只说明未复现。原失败日志、注入结果和新通过日志分别保存，互不覆盖。

当前没有代码修复可回滚。后续实施只备份准确目标；回滚前比较后续修改，只恢复本项 diff，保留其他聊天、历史日志和状态。不得 reset/clean、删除其他会话状态或恢复旧全局授权。

当前交付：本调查与条件化实施计划、原验收的调查补充、124 的新证据。缺口：可区分历史原因的失败现场与准确修复设计。根因未确定，尚未达到用户要求的“修复并重新验收”完成条件。

## 6. 新授权：仅补齐失败现场采集

来源：用户确认按最小范围继续，先保存方案和实施计划，再实施及定向验收；只补失败现场记录，不改生产逻辑、超时或重试。以下 D1–D5 是本次获准步骤，独立于仍待根因证据的 F2–F6；不推进第三阶段。

### 6.1 当前事实与实现范围

`HookCommandTests.hooks()` 经 `invoke()` 启动既有配置中的 hook 命令；返回内容在断言或 JSON 解析失败后随临时目录清理。假 CLI 由复制到临时目录的 `hook_common.py` 经 `subprocess.run` 启动，当前没有持久的 argv/返回码/耗时记录。仅加断言消息不足以保存嵌套 CLI 的现场。

只新增 `.harness/scripts/fixture_diagnostics.py` 测试辅助文件，并修改 `.harness/scripts/test_runtime_wiring.py` 中的夹具及采集验收用例。辅助文件只由测试导入；在临时目录的 `hook_common.py` 副本末尾安装局部 subprocess 代理，观察假 CLI 调用，原始生产文件和模块不变。父进程夹具的激活调用采用同一代理并在作用域结束后恢复。两端 hook 配置、命令、实际 argv、输入、返回内容和异常保持原样。

不增加 CLI 调用、不自动重试、不改 20 秒测试调用超时或 2/6/8 秒生产预算、不替换假 CLI 实现、不改全局 subprocess、环境配置、skills、信任或任何 Go 文件。没有新增依赖，沿用已核实的本机 Python 3.13.11 与标准库。

### 6.2 记录格式与保存行为

- 每个 hook/夹具命令与嵌套假 CLI 调用独立记录：类型、实际 argv、cwd、PID、UTC 开始时间、monotonic 耗时、原 timeout、状态及实际退出码。未观察到退出（超时、启动失败或记录中断）写 null，不编造退出码。
- hook 同时保存完整 stdin、stdout、stderr；假 CLI 保存 subprocess 原本可见的完整输出。原调用已把 stderr 合并进 stdout 的情况标明 `stderr_merged`，不伪造独立 stderr。字符串用 UTF-8 保存；异常携带 bytes 时原字节保存，不截断内容。
- 调用前写 running 元数据，调用后更新 exited/timeout/os_error 等状态和异常类型/详情；JSON 原子替换，原异常继续抛出。记录写入失败明确报错，不让采集失败被当作通过。
- 当前用例的记录先放自己的临时目录。失败（含 subTest 和 JSON 解析等异常）时，先复制到项目 `.cache/harness-fixture-failures/<唯一目录>/`，再清理临时夹具；保留失败 traceback、用例 ID、夹具 state 和假 CLI 源码。标准错误输出显示绝对证据路径。目录唯一、不覆盖已有现场；成功用例不留下持久证据。
- 只保存本测试的合成输入与输出，不枚举环境变量或读取凭据。记录中的原临时路径用于说明执行现场；输出文件存于保留目录中，临时目录删除后仍可读取。

### 6.3 验收标准与实施步骤

| 步骤 | 前置条件 | 具体工作 | 检查与证据 | 停止条件 | 状态 |
|---|---|---|---|---|---|
| D1 | 最新授权、原记录与源码已读取 | 保存并回读本节；备份两份代码和相关文档，保存生产边界哈希 | 本计划、限定备份、既有改动清单 | 方案或文件范围不明确 | 完成 |
| D2 | D1 完成 | 仅创建 `capture-runtime-fixture-failures`，补齐独立 change 的 artifacts；读取/绑定本会话契约 | 当前 change apply-ready；当前会话读取证据 | CLI 出错或需操作旧 change | 完成；本地调用，不冒充原生回调 |
| D3 | D2 完成 | 实现记录器、临时副本代理与失败清理前保留；不改原超时/重试/异常 | 逐项回读 diff，代理限于夹具 | 触及生产文件或吞掉异常 | 完成；仅两份测试文件 |
| D4 | D3 完成 | 定向验收完整大输出、正常返回码、超时部分输出、启动错误、失败保留/唯一目录、成功清理和实际 hook→CLI 关联；运行受影响 HookCommandTests | 下方唯一功能命令的日志、实际失败现场样本及 metadata | 同问题连续三轮自纠失败；原断言被放宽 | 完成；13 项通过，14.460 秒，无跳过 |
| D5 | D4 通过 | 只校验当前 change，回写报告及 124 的诊断能力，不关闭历史根因；核对受保护文件并收尾本 change | 定向结果、限定 diff、当前 change 校验、文档回读 | 记录缺失或范围漂移 | 完成；仅当前 change 已归档，归档后定向校验通过 |

功能命令仅为 `python3 .harness/scripts/test_runtime_wiring.py FixtureDiagnosticsTests HookCommandTests`。超时输出契约可用构造的 TimeoutExpired 核对原异常透传；真实子进程用于完整输出/退出码及 hook→假 CLI 的实际调用验收。预期失败的内层测试由外层断言验证，不能冒充历史偶发复现。原生客户端、无关测试和所有受控检查不运行。

风险是诊断 I/O 增加夹具耗时；记录只涉及当前用例，不改变时间预算或失败判定。中途被终止可能只留下 running 记录，必须如实标注未观察到退出。回滚仅恢复本次两份测试代码的 diff 和新增辅助文件，先核对后续改动；保留历史失败证据，不回滚生产或其他聊天文件。

<!-- harness-contract:v1 -->
```json
{
  "schema_version": 1,
  "stage": "2",
  "change": "capture-runtime-fixture-failures",
  "allowed_files": [
    {"path": ".harness/scripts/fixture_diagnostics.py", "kind": "code"},
    {"path": ".harness/scripts/test_runtime_wiring.py", "kind": "code"},
    {"path": "docs/harness-sessionstart-fixture-repair-plan-2026-10-23.md", "kind": "document"},
    {"path": "docs/harness-phase2-acceptance-2026-10-23.md", "kind": "document"},
    {"path": ".harness/error-journal.md", "kind": "document"}
  ],
  "required_context": [
    {"kind": "rules", "path": "AGENTS.md", "name": "project-rules"},
    {"kind": "guide", "path": ".harness/guides/workflow-contract.md", "name": "workflow-contract"},
    {"kind": "skill", "path": ".agents/skills/harness-implement/SKILL.md", "name": "harness-implement"},
    {"kind": "skill", "path": "/Users/xiaozhaofu/.agents/skills/senior-python-engineer/SKILL.md", "name": "senior-python-engineer"}
  ],
  "checks": [
    {"id": "fixture-diagnostics", "kind": "functional", "argv": ["python3", ".harness/scripts/test_runtime_wiring.py", "FixtureDiagnosticsTests", "HookCommandTests"], "timeout_seconds": 120},
    {"id": "diagnostics-schema", "kind": "schema", "argv": ["python3", ".openspec-auto/tools/run_openspec.py", "validate", "capture-runtime-fixture-failures", "--type", "change", "--strict", "--json", "--no-interactive"], "timeout_seconds": 30}
  ],
  "regressions": [
    {"error_id": "ERR-20261023-124", "reproduction": "失败时完整保留 hook 与嵌套假 CLI 现场；不宣称已复现或修复历史根因", "checks": ["fixture-diagnostics"]}
  ]
}
```

### 6.4 实际验收记录

证据目录 `.cache/harness-fixture-diagnostics-20261023-9kovpt3c/`。记录器位于测试脚本目录，生产 `hook_common.py`、四个 OpenSpec hook、两个 harness hook、双端 hook 配置、会话实现和第三阶段所在总计划均与本轮快照一致。

本轮只运行 `python3 .harness/scripts/test_runtime_wiring.py FixtureDiagnosticsTests HookCommandTests`：13 项通过、无跳过，测试报告 14.460 秒，runner exit 0。功能 run ID 为 `7a9c1b2a-f3f4-440e-bd65-32eccda7966f`；当前 change 的严格校验 run ID 为 `7ccdf8a2-4ebf-4ce2-b49a-56b178aa89a2`，exit 0、valid=true、issues=[]。真实 stdout/stderr 和哈希由本会话 runner 记录。

验收包含 12,000 行输入的完整往返、异常对象原样传播、部分非 UTF-8 bytes 保存、null 与实际退出码区别、两个同名失败现场互不覆盖、subTest/JSON/CLI 失败后的文件回读、成功不产生持久目录，以及仅两次原有 CLI 调用（version/list）的记录。失败现场样本在隔离验收临时目录内实际生成、回读后随外层测试清理；没有把样本称为历史偶发复现。

以后 HookCommandTests 真实失败时，stderr 会显示 `[fixture diagnostics] retained: <绝对路径>`，目录下的 `failure.json`、`fixture-state.json`、`fake-cli.py` 与 `processes/*/{record.json,stdin,stdout,stderr}` 用于定位。`observer_pid` 是观察进程 PID；原 stderr 已合并时查看 stdout 和 `stderr_merged`。本轮没有运行原生客户端、全量测试或任何受控检查。124 继续 open。

收尾：仅 `capture-runtime-fixture-failures` 使用 `--skip-specs` 归档到 `openspec/changes/archive/2026-10-23-capture-runtime-fixture-failures/`，输出确认成功；归档后仅对该路径 strict validate，exit 0、valid=true、issues=[]。11 个受保护文件哈希未变。测试内的 healthcheck 仍只作用于隔离夹具，不是本机项目独立验收。
