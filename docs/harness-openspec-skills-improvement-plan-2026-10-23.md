# Harness、OpenSpec 与 skills 协作改进方案和实施计划

日期：2026-10-23。当前授权：先保存完整方案，再实施阶段 1；阶段 2、3 仅规划，禁止提前实施。

## 1. 需求来源和交付边界

用户要求检查三者是否实际协作，区分文档约定与自动检查；随后明确授权：统一两端规则、修复错误记录读取截断、补齐运行端接线和验收。其他问题只报告。

目标：先让当前项目的 Claude Code / Codex 入口一致、错误记录可完整读取、已安装 OpenSpec CLI 在 hook 和手工工具中使用同一个可靠入口；用可复核证据区别“已配置”“脚本可用”“客户端已实际触发”。后续再把流程要求变成可执行门禁，并建立团队可复现的回归闭环。

非目标：不改 Go 业务逻辑、依赖、目录分层、数据库和容器；不恢复此前代码任务；不实现请求超时、断路器、OpenAPI；不处理旧测试超时和 lint 版本差异；不安装工具、不改用户级信任或配置、不提交或发布。

本轮使用 `harness-implement`、`openspec-auto`、`shell-scripting`；Codex 契约按 OpenAI Docs 与已安装 CLI 核实。验收按 `backend-acceptance` 的证据边界执行，业务数据库、事务、API、容量项目因本轮只修改开发工具而不适用。

## 2. 已核实事实与尚未确认事项

| 事实 | 依据 | 影响 |
|---|---|---|
| `AGENTS.md` 已限制受控检查须功能完成后单独授权，`CLAUDE.md` 仍允许直接运行 vet/lint | 两文件“自主执行边界”等正文 | 两端规则存在真实漂移 |
| 两个 reader 只返回前 240 行，错误日志超过 3200 行 | `.harness/scripts/read-error-journal.{sh,ps1}` 与实际日志 | 新错误可能读不到 |
| 两端已有 SessionStart、UserPromptSubmit、PreToolUse、Stop 注册 | `.codex/hooks.json`、`.claude/settings.json` | 缺少名称不等于没有注册 |
| Codex 的 `exec_command` 事件用 `Bash`；`apply_patch` 可匹配 `apply_patch`、`Edit`、`Write`，payload 保留 `apply_patch` | [OpenAI hooks 文档](https://learn.chatgpt.com/docs/hooks) | 不新增臆测的 `exec_command` / `functions.exec` 适配器；显式补 `apply_patch` 便于审计 |
| 项目信任与精确 hook 定义信任是不同条件；定义变更可能要求重新信任 | 同上，已安装 CLI | 不把 JSON 文件存在当作运行证明，不自动修改用户信任 |
| 当前 CLI 为 `codex-cli 0.163.0-alpha.11.1`，`codex features list` 显示 hooks stable true | 本轮真实命令 | 只证明当前 CLI 功能开关，不证明桌面当前会话调用了 hook |
| OpenSpec 1.3.0 已安装在 NVM v24.6.0 的全局目录，当前 PATH 下找不到 openspec | 已读取 package.json 与命令发现结果 | 禁止重新安装；修复本机已有安装的解析 |
| 已有 Node v24.22.0 可运行 OpenSpec；其 package.json 要求 Node >=20.19.0 | 本轮本机版本、已安装 OpenSpec 元数据 | 保持此最低版本，不升级依赖 |
| Python 3.13 可用；PowerShell 未发现 | 本轮工具发现 | shell/Python 可做实际验收；PowerShell 行为须明确未验证 |
| `env.sh`、`hook_common.py` 分别实现解析，healthcheck 用拼接字符串和登录 shell 调用 | 实际源码 | 收敛 argv 调用，避免 PATH 重置和含空格路径拆分 |
| healthcheck 要求的 OpenSpec managed block 在两份入口中缺失 | 实际源码/入口 | 补同一份简短流程块，显式受用户授权边界约束 |
| 项目治理文件被本地 `.git/info/exclude` 排除 | 本轮审查 | 阶段 1 修改可能不出现在普通 git diff；需显式文件清单和备份核验 |

尚未确认：当前桌面会话是否实际加载每个 hook；Claude 当前客户端是否实际触发；PowerShell 执行结果；更新后的精确 hook 定义是否已受信任。必须分别记录，不能以 fixture 测试替代。

## 3. 内容源、兼容约束和回滚

- harness 内容源为 `/Users/xiaozhaofu/Ai/harness-engineering`，项目 `.harness/VERSION` 标记安装来源 commit `09972a20a8ef` / v1.7.0。OpenSpec 自动化内容源为 `/Users/xiaozhaofu/Ai/openspec-auto-bootstrap`。**本轮仅交付 gin_api 项目定制，不修改这两个源仓库、全局 skill、安装器或其他项目**；不得宣称模板已修复。
- 使用 `AGENTS.md` 的公共正文作为本项目规则基准；保留两端各自标题/介绍。`CLAUDE.md` 从首个 `## 行为纪律` 起与其一致。增加只读一致性检查，默认不自动改文件。
- 错误日志格式、ID、开关状态、append/close 协议保持原样；reader 参数、缺文件失败语义保持原样，仅移除 240 行上限。SessionStart 摘要仍是摘要，不改其选取算法。
- OpenSpec 解析优先级固定为：显式 `OPENSPEC_BIN` → PATH → 已有 NVM 安装。显式配置失效时报告错误，不静默改选；NVM 多安装按 Node 版本从高到低稳定选择可运行项。Node 仍使用已有 >=20.19.0 的候选；不下载。对子进程增加有限探测超时，错误转换为 `OpenSpecRuntimeError`，不 traceback 掩盖真实原因。
- Python 与 shell 共用一个 argv 解析入口；`OPENSPEC_TELEMETRY` 默认 0，用户已显式设置时沿用。保留 `NODE_BIN`、`OPENSPEC_BIN`、`OPENSPEC_PYTHON` 的既有作用并在验收文档中说明用途、优先级、失败条件；不增加业务配置。
- 不改 OpenSpec 的 prompt 分类、授权识别、全局 current_change 选择、完成判定与安全 hook fail-open 政策；这些属于阶段 2。
- 备份本轮触碰的原文件到 `.cache/harness-runtime-phase1-<时间>/before/`，清单记录原始 SHA-256、原本不存在的文件及本轮范围。验收日志写同一目录。回滚只按清单恢复本轮文件/删除本轮新增文件；先确认无后来修改，禁止 git reset/clean，不覆盖原有未提交代码。新增错误记录保留历史，通过追加处置或 close 标识处理。

## 4. 完整改进路线

| 阶段 | 目的与方案 | 允许改动 | 当前授权 |
|---|---|---|---|
| 1 | 统一规则、完整读取错误记录、修复当前运行端接线、建立分层验收 | 见下方逐步清单 | 项目侧与 6 条信任/实际触发完成；保留误拦及采集问题，未整体通过 |
| 2 | 把关键工作流约定变为明确、有限的自动检查 | 明确请求级授权/阶段状态；计划与修改范围核验；skills/guide 路由存在性与配对检查；会话隔离；完成证据与错误回归关联 | 未授权，不修改 |
| 3 | 将已验证门禁纳入可复现的团队流程 | 治理文件与安装版本的分发决策；CI 分层集成；存量 OpenSpec 对账；错误回归长期维护 | 未授权，不修改 |

### 阶段 2 的完整实施约束与步骤（待授权）

1. 前置：阶段 1 当前运行证据可用，用户批准具体规则/误报策略。整理“可机械判定”和“须人工判断”矩阵，禁止将文字提到命令视为执行授权。交付范围与证据 schema。
2. 将计划路径、当前获准阶段、允许文件、验证授权及命令结果保存为会话独立状态；研究 current_change 的会话隔离与迁移兼容。验证并发两会话、缺失/损坏状态、取消授权和恢复；无法区分会话时停止，不回退到其他会话授权。
3. 检查改代码前是否已有可读取计划与 apply-ready change；只读/纯文档不触发实施或测试。保留用户明确跳过 OpenSpec 的路径，但不据此跳过计划。用中文复合指令、否定句、日志/代码正文建立反例集。
4. skills/guide 只做路径、配对和必要加载证据检查；不能伪称能证明模型理解。完成判定使用实际文件范围与验证记录，schema validate 不等于 tests passed。明确不可验证的人工判断项。
5. 错误记录关联复现条件、回归用例、处置证据；不自动关闭无法验证的问题。验收误拒绝/漏拒绝、串会话和伪造完成结果；失败回滚对应自动检查，不削弱用户约束。

停止条件：缺少批准的规则、误报策略或无法取得安全的运行端执行证据；不得为追求“全自动”扩大授权。所有步骤现为未执行；具体脚本落点须以阶段 1 之后源码和用户确认的 schema 更新本计划后确定。

### 阶段 3 的完整实施约束与步骤（待授权）

1. 前置：阶段 2 门禁稳定，用户决定模板回流或项目共享范围、哪些文件可入库。先核对源/安装副本/忽略规则，禁止擅改全局配置和发布。
2. 为共享规则、测试与运行依赖建立版本清单及干净环境重现方法；秘密、本地状态与错误日志不直接入库。验收新检出只安装明确依赖即可复现；不默认拉取镜像。
3. CI 拆分治理静态检查、OpenSpec delta 校验和获准业务质量门禁，输出各自证据。先验证实际工作流再接 required checks；不将当前会话受控检查自动授权。
4. 逐个核对存量 active change 与代码/用户最新决定，单独批准后补齐、废弃或归档；尤其旧 banner 方案不能覆盖后来恢复渐变的决定。不得批量勾选任务或归档。
5. 将高频真实错误关联回归测试，按实际失败减少重复错误；记录误报、漏报与失效的运行端版本。以可重复运行和故障样例验收，不承诺消除所有幻觉。

停止条件：共享边界/敏感内容/旧变更意图未确认，或 CI 基线失败尚未归因。回滚按单个门禁或 change 分开处理；无本阶段执行授权。

## 5. 阶段 1 逐步实施计划

| 步骤 | 前置条件 | 具体动作与文件 | 检查、可复核证据 | 停止条件 | 状态 |
|---|---|---|---|---|---|
| 1.0 | 已读取用户规则、当前两端入口、相关 skill/guide 和错误记录 | 保存本计划并完整回读；为本阶段建立独立 OpenSpec change `repair-project-harness-runtime-wiring`，准备 proposal/design/specs/tasks；备份限定文件与状态 | 计划回读；CLI status 的 applyRequires 全部 done；备份 manifest | 计划不完整、状态不明、备份失败 | 完成 |
| 1.1 | 1.0 完成 | 以 AGENTS 公共正文同步 CLAUDE；补受当前授权规则约束的 OpenSpec managed block；新增 `.harness/scripts/check-agent-rules.py`，仅比较正文 | 两文件回读；检查器相等通过、差异反例失败；保留独立头部与 Claude permissions | 出现丢失用户规则或不在本轮的改动 | 完成 |
| 1.2 | 1.0 完成 | `.harness/scripts/read-error-journal.sh` 使用完整 cat；`.ps1` 移除 TotalCount，不改日志协议 | >240 行、最新条目、空文件、含空格根路径、缺文件场景；本机日志末尾可读 | 输出变化超出取消上限或失败被吞 | 修改完成；shell 通过，PowerShell 未验证 |
| 1.3 | 1.0 完成，已核实本机 CLI | 改 `.openspec-auto/tools/hook_common.py` 解析与有限探测；新增 `run_openspec.py`；`env.sh` 委托共同入口；`healthcheck.sh` 使用 argv 执行并明确状态层级；两端 matcher 显式包含 apply_patch | 覆盖覆盖项、PATH、NVM、含空格路径、CLI 不可用/超时；本机实际 list；无依赖安装、无登录 shell 拼接 | 未知 API/版本、需用户级修改、解析错误未受控 | 完成 |
| 1.4 | 1.1–1.3 完成 | 新增 `.harness/scripts/test_runtime_wiring.py`，只执行本阶段功能测试；通过两个配置中的真实 hook 命令做隔离 fixture 验收 | SessionStart 注入、UserPromptSubmit 分类、PreToolUse Bash/apply_patch 拒绝与放行、Stop 输出；缺 CLI 返回明确错误；不运行危险 payload 中命令 | 本轮失败连续三轮未解决；fixture 误写真实状态 | 17 项通过、1 项 PowerShell 跳过 |
| 1.5 | 本阶段功能测试完成 | 保存 `docs/harness-runtime-acceptance-2026-10-23.md`：文件/版本/命令/退出码、配置一致性、脚本验收、客户端实际证据、未验证项；核对本轮 diff/manifest；更新计划和错误记录 | 前轮 healthcheck 通过；本次 strict validate、18 项定向测试和原生重验通过；范围比较另有未归因变化 | 真实结果不足不得写成整体通过，不绕过拒绝 | 两项修复专项通过；全工作区不变断言未通过，见第 10 节 |
| 1.6 | 交付证据完整；不依赖下阶段 | 汇报阶段 1、后续待办、已有问题；功能全部完成后才可询问是否运行受控检查 | 未执行受控检查；OpenSpec 仅在所有必要任务真正完成时归档本 change | 有真实阻断则不得勾选通过或虚报阶段完成 | 两项专项交付；其他未验证项保留，未整体归档 |

允许文件还包括本 change 的 OpenSpec artifacts、必要的 `.harness/error-journal.md` 错误处置及本轮 `.cache` 备份/日志。不修改现有其他 change、CI、Makefile、业务配置、Go 源码或测试。

## 6. 验收不变量与风险

1. 两端公共规则完全一致；新的检查器能检出真实差异，不靠日期或散列固定值判断。
2. reader 不再吞掉 240 行之后的内容；不存在文件仍失败，不把空输出当正常日志。
3. 环境中 CLI 不存在、显式路径无效、版本不兼容或探测超时时，返回清楚有限的错误；不新装、不静默绕过。
4. 路径和参数按 argv 传递；用户路径包含空格或 shell 特殊字符时不被执行。
5. hook 成功测试只表述实际覆盖的事件与层级；静态检查不宣称激活，客户端未知/未信任不标 passed。
6. 已有未提交业务修改和六个旧 active change 保持原样；不将历史 race/lint 结果视为本次证据。
7. 规则同步不会自动授权检查；测试脚本本身也不得执行受控命令、危险命令、真实写业务状态或联网模型调用。

主要风险：本地排除文件难于普通 diff 检出；更新 hook 定义触发重新信任；NVM 多安装选择错版本；共享状态被验收脚本碰到；没有 PowerShell/Claude 运行证据。措施分别为 manifest、分层验收、确定性解析与版本探测、隔离临时仓库、不虚报跨端结果。

## 7. 执行记录

- 初始状态：只读调查已完成；本文件为首个实施前交付物；代码尚未修改，测试尚未运行。
- 后续在此逐步追加真实命令、退出码、结果和未验证项；不得预填成功。
- 备份与证据：`.cache/harness-runtime-phase1-4w0lbm8o/`。首轮 16 通过、1 跳过；最终 17 通过、1 跳过，exit 0。实际 reader 在记录时读取全部 3360 行，与文件逐字节相等；无关文件 SHA-256 核对 690 项未变。
- 最终本机 `healthcheck.sh .` exit 0，2.725 秒；只调用本机已有 OpenSpec。当前 change strict validate exit 0，1.116 秒，issues 为空；这些不是客户端实际拦截的证明。
- Codex 本机 app-server 的只读 `hooks/list`：6 条项目定义，errors/warnings 为空，全部 enabled=true、trustStatus=untrusted。用户级信任属于当前授权外的动作，等待确认；不创建客户端测试会话，不绕过信任。
- 额外存量发现：文档 `apply_patch` 的 command 字段未被文档目标解析器识别，正文提及 Go 路径会误拒绝；在本轮前备份及当前源码都复现。只报告（ERR-20261023-105），未修改分类策略。
- 阶段 2、3 未开始；旧 change、旧测试失败、工具版本差异和模板源均未修改。下一步只处理已授权的第一阶段运行端信任/实际验收，不自动进入其他阶段或质量门禁。

## 8. 运行端信任与实际触发续验（用户已授权，执行结果见第 9 节）

用户本轮明确允许仅登记本项目这 6 条 hook 的信任并继续实际触发验收；不扩大阶段，不运行尚未授权的检查。此授权替代第 7 节“等待信任确认”的停点。

1. 前置核对：实际重读用户规则、错误记录、本计划与 6 条定义；用运行端 `hooks/list` 核对 sourcePath、key、currentHash，必须仍对应 `.codex/hooks.json` 的同 6 条。任何定义或数量变化先停止对应信任写入。
2. 写入边界：使用本机原生 hook 信任入口，只把这 6 个精确 key 的 trusted_hash 登记为已核对的 currentHash。保留已有其他项目 hook 状态、启用状态和其他用户配置；写前保存本项状态及其他配置摘要，写后比较。不能启用信任绕过参数或授予整个目录的任意脚本信任。
3. 实际验收：通过本机 Codex 原生运行端建立临时验收会话，记录真实 hook started/completed 通知。触发 SessionStart 两条、UserPromptSubmit、Bash/apply_patch 的 PreToolUse 两条及 Stop。允许一次最小模型回合仅驱动这些调用；不委托开发、不运行业务命令或质量检查。临时补丁仅写本轮 `.cache` 下的 canary 文件，正向 shell 只输出固定标识；若验证拒绝路径，命令使用 `false && ...` 保证危险分支不可达，不重试绕过拒绝。
4. 状态保护：开始前快照本轮可能写入的 OpenSpec 会话状态；验收不得修改存量 change。只回收本次临时会话状态、canary 文件；共享 selected change 如已有他人更新不得覆盖。模型如尝试范围外操作，拒绝并结束回合，记录真实结果。
5. 收尾：再次确认 6 条 trusted、其他信任/配置不变、项目逻辑/定义未改，回写验收报告、原任务状态及 ERR-20261023-104。真实触发或结果不足时保留未验证/失败，不补造证据，不借机修其他问题。

回滚：只恢复本轮 6 条 key 在写前的状态（原不存在则仅移除新增条目），不覆盖整份用户配置；临时文件按实际创建清单清理。若运行端行为或安全边界无法核实，停止对应动作并报告，不更改代码。

## 9. 续验实际记录（历史结果，本次修复见第 10 节）

- 本机原生 `/hooks` 已为同 6 条 key 登记精确 trusted_hash；当前全部 trusted。其他 42 条信任和其余用户配置摘要未变，没有改 hook 定义。
- 临时 Codex app-server 会话 `01a166fb-443a-7b20-b9c4-f7aac2c4f697`，实际模型回合仅一次；6 条不同 hook 定义产生 10 条完成事件，包含正常放行、补丁拒绝和安全负例拒绝。正常 shell 输出固定标识，危险命令未执行，canary 文件未创建。
- 只修改临时文本的补丁被存量 OpenSpec guard 误拒绝；按用户边界只报告，补记 ERR-20261023-105 的实际证据。未修复其他规则或阶段。
- 临时采集器未收到 turn/completed 而在 120 秒后 exit 1；原始事件已有最终回复和 Stop completed，另有模型流重连。根因未确认，记 ERR-20261023-107，未将整体验收标绿，未再运行模型或其他检查。
- 1203 项保护文件及旧会话状态保持不变，仅清理该临时会话新建的 request-scope 文件。证据保存在原证据目录的 `trust-acceptance/` 下。
- 本次用户授权的 6 条信任与实际触发工作完成；前述存量误拦、采集终结、Claude/当前桌面/PowerShell 未验证项如实保留，当前 change 不整体归档。

## 10. 两项根因修复与重新验收

用户仅授权修复文档补丁误拦和采集器超时。先完成最小复现，并保存、回读 `docs/harness-two-fixes-plan-2026-10-23.md`，再修改代码；本节不改变阶段 2、3 的顺序、范围或授权状态。

- 文档误拦：标准 `tool_input.command` 没有进入文档目标解析；补丁控制行的 Patch 动作词已足以触发通用分类，正文提到 Go 路径不是必要条件。只补这一来源字段，仍逐项判断实际目标；混合代码、移动到代码、配置文本等反例不豁免。
- 采集器超时：已复现 TextIOWrapper 预读第二条 JSON 后 fd 不再可读，旧循环因等待 select 而漏消费用户态缓冲。改用二进制管道及单一字节行缓冲，保留 120 秒期限与匹配 threadId/turnId 的终结要求；不能据此倒推历史运行一定发出了未采集的通知。
- 定向测试：文档/guard 10 项、采集器 8 项均 exit 0。真实重验会话 `01a16724-ce0f-7310-8b09-960f8b5883cd` 在 42.783 秒内成功完成，采集器 exit 0；6 条定义 10 次回调，文档补丁成功，原安全负例被拒绝，匹配 `turn/completed` 已保存。
- 范围结果：用户配置、hook 定义及原有会话状态未变；538 个任务外文件比较发现 40 个变化，范围断言 exit 1，来源未确认，记为 ERR-20261023-109。仅报告并保留这些文件，不把全工作区不变写成通过。
- 证据：`.cache/harness-runtime-phase1-4w0lbm8o/trust-acceptance/repair-105-107/`。本 change strict schema 校验 exit 0，issues 为空；本次没有重跑全量功能测试或受控检查。healthcheck 仅随 HookCommandTests 在隔离夹具中执行，未单独重跑本机项目接线检查。105/107 已按两项修复证据处置；其他阶段、原总体验收和跨端未验证项不扩大处理。
