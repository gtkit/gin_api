# 第三阶段补充：Codex 原生客户端验收

日期：2026-10-23。初始状态：方案已保存；原生准备、信任登记与模型回合尚未执行。

## 1. 来源、目标与范围

用户要求先落盘完整方案和实施计划，再验证 go-harness 新建、更新项目的 Codex 原生 hook 实际触发；仅为临时验收项目的 6 条 hook 登记信任。124 等实际复现，其他问题只报告，不运行未授权检查。

本次使用同一个独立临时 Git 项目，先新建、原生验收，再 update、再次原生验收。这样只登记同一组 6 个精确 key/hash，不为两个路径登记 12 条。生成目录由 tempfile 在系统临时区创建，实际绝对路径写入本轮 manifest 后核对；不在 gin_api 的 Git 父目录内，避免 hook 的 git rev-parse 指向业务项目。

只新增本项目的验收脚本与文档，复用上轮已验证两套本机模板、现有字节行采集器和本机 Codex。用户随后明确允许给本次唯一临时项目增加一条项目级信任，并要求结束后撤销；这替代最初仅依靠进程配置覆盖的设计。除这条临时项目记录及 6 条精确信任外，不修改用户配置。不得使用全局信任绕过参数。

非目标：不修业务或模板，不重新运行上轮 Python 回归，不运行 Go/build/lint/脚本 lint/vet/modern/race/coverage、CI、容器、发布或其他 change 验收。不调查 124，不改生产超时、重试、hook 定义或既有信任。原桌面聊天和 Claude 客户端不在本次原生 app-server 验收结论内。

## 2. 已核实事实及前置核对

- 上轮两源已同步，新建、更新和重复更新通过；原生信任与回调仍未验证。来源：第三阶段本机方案与验收报告。
- 本机原生 API 的已有 schema 提供 initialize、hooks/list、config/batchWrite、thread/start、turn/start；HookMetadata 提供 key/currentHash/sourcePath/eventName/displayOrder/trustStatus。ConfigEdit 为 keyPath/value/mergeStrategy，replace 为有效策略。
- 已有采集器 `.cache/harness-runtime-phase1-4w0lbm8o/trust-acceptance/actual_runtime.py` 的 JsonLineReader 使用二进制管道与完整行缓冲，可复用；其 main 绑定 gin_api，不直接运行该 main。
- 官方文档说明项目层可信与精确 hook 信任不同，未信任的定义会跳过。来源：https://learn.chatgpt.com/docs/hooks （本次已读取）。不能以 hooks/list 有定义或脚本能执行替代原生回调。
- 已有信任记录使用 hooks.state 下的完整 key 和 trusted_hash；旧验收只增加目标项并比较其他配置摘要。本轮需由原生 hooks/list 取得新路径的 key/hash，禁止按记忆构造。
- 沿用已安装 Python 3.13.11、Codex 与 OpenSpec；准备步骤实际读取版本及 CLI help，确认进程级 --config 参数和协议可用。若与现有 schema 不符则停，不安装、不升级、不猜接口。

## 3. 实现与文件

- 新增 `.cache/harness-native-phase3-20261023/test_native_acceptance.py`：本轮专用验收入口，复用现有 JsonLineReader，按下面阶段保存命令和真实结果；不进入两套模板源。
- 新增本文，更新 `docs/harness-phase3-local-acceptance-2026-10-23.md` 原生补充与 `docs/harness-openspec-skills-improvement-plan-2026-10-23.md` 对应状态；必要时追加 `.harness/error-journal.md`。
- 独立 OpenSpec change `verify-generated-harness-native-hooks` 只记录本次验收脚本和流程，不修改存量 change。按当前真实会话 `01a1667d-a368-7782-8e71-c426a463358a` 读取、绑定本计划，不复用旧阶段或上轮例外。
- 证据写入 `.cache/harness-native-phase3-20261023/`，包含命令 argv/cwd/退出码/耗时、原生协议流、stderr、hook 元数据、配置脱敏摘要、canary 和结果。不保存凭据、完整用户配置、auth 文件或环境转储。

## 4. 执行步骤及验收标准

| 步骤 | 前置条件 | 动作 | 证据与验收 | 停止条件 | 状态 |
|---|---|---|---|---|---|
| N1 方案与绑定 | 最新用户范围、规则和错误记录已读 | 保存并完整回读本文；准备独立 change；workflow read 所需上下文并 activate | 正确会话、计划 hash 与 apply-ready 状态 | 授权/契约不匹配，不借旧例外绕过 | 方案已落盘，余项待执行 |
| N2 准备 | N1 完成 | 读取真实版本/help；系统临时区建独立 Git 项目；用真实 go-harness 新建，带 --skip-codex-user-config；给获准的唯一临时路径登记可撤销项目级信任；原生 hooks/list 只读 | git 根为临时目录；6 条仅来自该项目，事件为 2 SessionStart/1 UserPromptSubmit/2 PreToolUse/1 Stop；无错误/警告，保存 exact key/hash 与定义 | 数量/来源/定义不符、已有信任项或 CLI 不支持 | 首次因项目信任停止，新增授权已取得，待继续 |
| N3 信任和新建原生验收 | N2 输出已实际审阅，6 个 key/hash 确认 | 原生 config/batchWrite 仅写 6 个 trusted_hash；比较其他 hooks.state 与用户配置规范化摘要；新 app-server 回读 trusted，再执行单个最小原生回合 | 6 项变化精确匹配；正常 printf 输出成功、文档 canary 内容正确、不可达危险分支被安全 hook 拒绝；6 定义有成功或预期 blocked 回调；匹配 turn/completed 且 completed | 其他配置变化、定义漂移、意外工具/授权请求、回合失败或超时 | 待执行 |
| N4 更新后原生验收 | N3 通过 | 对同一项目执行 go-harness update；不重新登记信任；新 app-server 重新 list 并执行第二个最小原生回合 | 6 key/hash 未变且仍 trusted；更新后同样满足原生回调与输出条件；既有 canary/会话状态不被更新器抹除 | 需要扩大信任、回调缺失、实际失败 | 待执行 |
| N5 范围核对和收尾 | N3/N4 均有真实证据；失败退出也须撤销本次项目信任 | 撤销该临时项目级信任；比较 gin_api/两源受保护内容、信任非目标项和配置摘要；回写报告/计划/错误，完成当前 change | 两轮独立 threadId/turnId 与协议文件；最终仅新增 6 条 hook trust；项目级记录已恢复不存在；其他业务/模板/状态不变 | 有失败不标整体通过；124 若复现只保留现场 | 待执行 |

两轮原生模型提示只允许依次执行：固定 printf；在临时项目中用 apply_patch 新增一份纯文本 canary；尝试 `false && git reset --hard` 一次以验证拒绝；输出固定终结标识。左侧 false 保证危险分支不可达；预期拒绝后不重试、不换写法执行。除 canary 外不让模型读写文件、不委托代理、不运行质量检查。

app-server 请求超时沿用 20 秒，单个模型回合期限沿用 120 秒；不以最后回复或 Stop 代替匹配 threadId/turnId 的 turn/completed。每轮新进程验证信任已持久生效。仅允许这两个模型回合，不为稳定性重复调用；任一失败先保存完整输出并报告，必要修复另核范围。

## 5. 信任边界、失败和回滚

准备时先从原生元数据确认只有精确临时根的 6 条非托管、已启用定义。keyPath 使用真实 key 的正确引用；通过原生配置 API 写入，各项值只为相应 currentHash。仅保存目标项内容、其他信任条目数量与哈希、其余用户配置规范化哈希，不打印或复制敏感配置。

前后比较任何非目标配置差异都应停止；不覆盖可能并行变化的整份用户配置。失败时仅可撤销本次新增的精确 6 项，且先核对这些项仍与本轮写入相等，不影响既有项。成功后保留这 6 个临时项目的信任记录与项目路径作为可复核证据，不授予其他目录或未来新定义信任。

临时项目级信任只允许路径 `/private/var/folders/_w/xwv4k3t12vn9054592gsk5200000gn/T/gin-api-harness-native-mbdsy5hb/project with spaces`。先确认该项目项原本不存在，再经原生配置 API 写入唯一 trust_level=trusted；不重建或改换临时路径。新进程重新读取实际配置，再继续原先的六条定义审阅。撤销时仅删除原生 API 为这个路径创建的单独表块，要求其仍只有预期 trust_level；回读 TOML 并比较其他配置，不能覆盖整个配置文件。若表块有并发新字段或无法准确识别则停止并报告，不删除。全程不保存完整用户配置或凭据副本。

只备份本轮要修改的文档与 helper；临时生成项目和 native 事件保留用于审计，不清理其他状态。回滚本项目修改时按 manifest 恢复本次内容，先确认无后来修改。模型出现范围外操作、信任失配、124 相同现象或采集失败时保留原始数据；不改产品逻辑、不延长预算、不自动重试。

## 6. 当前会话机器契约

<!-- harness-contract:v1 -->
```json
{
  "schema_version": 1,
  "stage": "3",
  "change": "verify-generated-harness-native-hooks",
  "allowed_files": [
    {"path": ".cache/harness-native-phase3-20261023/test_native_acceptance.py", "kind": "code"},
    {"path": "docs/harness-phase3-native-acceptance-plan-2026-10-23.md", "kind": "document"},
    {"path": "docs/harness-phase3-local-acceptance-2026-10-23.md", "kind": "document"},
    {"path": "docs/harness-openspec-skills-improvement-plan-2026-10-23.md", "kind": "document"},
    {"path": ".harness/error-journal.md", "kind": "document"},
    {"path": "openspec/changes/verify-generated-harness-native-hooks/proposal.md", "kind": "document"},
    {"path": "openspec/changes/verify-generated-harness-native-hooks/design.md", "kind": "document"},
    {"path": "openspec/changes/verify-generated-harness-native-hooks/specs/generated-harness-native-hooks/spec.md", "kind": "document"},
    {"path": "openspec/changes/verify-generated-harness-native-hooks/tasks.md", "kind": "document"}
  ],
  "required_context": [
    {"kind": "rules", "path": "AGENTS.md", "name": "project-rules"},
    {"kind": "guide", "path": ".harness/guides/workflow-contract.md", "name": "workflow-contract"},
    {"kind": "skill", "path": ".agents/skills/harness-implement/SKILL.md", "name": "harness-implement"},
    {"kind": "skill", "path": "/Users/xiaozhaofu/.agents/skills/senior-python-engineer/SKILL.md", "name": "senior-python-engineer"}
  ],
  "checks": [
    {"id": "native-project-trust", "kind": "functional", "argv": ["python3", ".cache/harness-native-phase3-20261023/test_native_acceptance.py", "project-trust"], "timeout_seconds": 30},
    {"id": "native-prepare", "kind": "functional", "argv": ["python3", ".cache/harness-native-phase3-20261023/test_native_acceptance.py", "prepare"], "timeout_seconds": 120},
    {"id": "native-new", "kind": "functional", "argv": ["python3", ".cache/harness-native-phase3-20261023/test_native_acceptance.py", "new"], "timeout_seconds": 180},
    {"id": "native-updated", "kind": "functional", "argv": ["python3", ".cache/harness-native-phase3-20261023/test_native_acceptance.py", "updated"], "timeout_seconds": 180},
    {"id": "native-scope", "kind": "functional", "argv": ["python3", ".cache/harness-native-phase3-20261023/test_native_acceptance.py", "scope"], "timeout_seconds": 30},
    {"id": "native-change-schema", "kind": "schema", "argv": ["python3", ".openspec-auto/tools/run_openspec.py", "validate", "verify-generated-harness-native-hooks", "--type", "change", "--strict", "--json", "--no-interactive"], "timeout_seconds": 30}
  ],
  "regressions": [
    {"error_id": "ERR-20261023-107", "reproduction": "两轮均必须捕获匹配的 turn/completed；保留完整协议，不以最后回复或 Stop 代替", "checks": ["native-new", "native-updated"]},
    {"error_id": "ERR-20261023-124", "reproduction": "不主动复现历史偶发；本轮原生事件失败时保留完整输出并如实报告", "checks": ["native-new", "native-updated"]}
  ]
}
```

## 7. 实际执行记录

初始写入时 N2–N5 未执行；本文不是验收成功证明。后续仅写真实命令结果与证据。

### N1 与 N2 的实际结果

当前会话已通过 workflow read 完整读取本计划及四项 required_context，activate 成功，绑定阶段 3 / verify-generated-harness-native-hooks。验收 helper 已写入，未修改业务或模板。

首次 native-prepare 的 run_id 为 `857c2665-2c61-413e-b8cc-ae4c57e115eb`，exit 1（约 10.04 秒）。真实 go-harness 新建成功，Git 根已核对；临时项目为 `/private/var/folders/_w/xwv4k3t12vn9054592gsk5200000gn/T/gin-api-harness-native-mbdsy5hb/project with spaces`。本机 Codex 为 `0.163.0-alpha.11.1`。

app-server 的 configWarning 明确指出该项目未信任，项目配置和 hooks 被禁用；hooks/list 返回空列表。使用进程 --config 覆盖没有令本次 hooks/list 加载该项目。只确认这一实际结果，不推断所有配置覆盖均不支持，也不当作模板的 hook 缺失。

在本轮 `.cache/harness-native-phase3-20261023/prepare-native/` 保留完整协议和 stderr；原 workflow stdout/stderr 已由 runner 保存。当前没有登记 hook 信任、没有修改用户级项目白名单、没有启动原生模型回合。N3/N4 尚未执行。按原计划停止，已请求用户批准仅此临时项目级信任并在验收后撤销；未获答复前不执行该扩展。

用户已答复“允许临时项目信任，结束后撤销”，上述单一路径的授权已明确。随后当前会话 status 却将该确认标为 mode=read、implementation=false（revision 4），导致脚本修改/重新激活无法按当前工作流继续。此为运行端意图分类问题，不重新索要同一权限、不修改分类器或手写状态；等待明确的继续执行指令刷新当前请求。授权本身保留，项目级及 hook 信任均尚未写入。

用户通过按钮再次明确“继续实施第三阶段原生客户端验收”，授权无需重复确认；实际状态仍为 mode=read、implementation=false（revision 5）。只读查看 request_intent / instruction_clauses 发现其会剥除引号内文字，而按钮答复以带引号的结构化消息进入聊天，符合此次未识别的现象；没有修改分类器。再次使用同一按钮不能解决当前阻断，需普通聊天文字刷新执行意图。N2 后续、信任登记与两个模型回合均未执行。
