# 文档补丁误拦与采集器超时修复方案、实施计划

日期：2026-10-23。授权来源：用户要求先查清两个根因、补齐方案和实施计划，再修复并重新验收；仅处理这两项，其他阶段不动，不运行未授权检查。

## 目标与非目标

目标：有效的纯文档 `apply_patch` 按目标文件分类，不因补丁正文或控制行而要求 OpenSpec；JSON 行采集器不遗漏已读入的尾部消息，只有收到匹配会话/回合的成功终结通知且验收结果符合预期时才成功。

非目标：不改其他 prompt 分类、OpenSpec 状态选择、安全策略、hook 定义或信任记录；不修其他存量问题；不改 Go、依赖、CI、目录层级和其他阶段；不安装工具，不运行 lint、脚本 lint、vet、modern、全量 race、覆盖率、Go build/test 或聚合门禁。

## 当前事实与根因

### 文档补丁（ERR-20261023-105）

1. `.openspec-auto/tools/hook_common.py` 的 `documentation_tool_targets()` 接收 `Edit/Write/MultiEdit/apply_patch` 等，但只从 `patch/input` 提取补丁，漏掉实际 Codex `tool_input.command`。
2. 因此纯文档补丁无法进入“全部目标是文档”的已有豁免，落到 `should_allow_edit()` 对整个字符串的意图分类。
3. 已用实际 canary.txt 补丁字符串复现：`docTargets=false`、`needs_openspec=true`、`codeHints=[]`。`*** Begin Patch` / `*** End Patch` 中的 Patch 被通用动作词规则命中；所以即使正文没有 Go 路径也会被要求选择 change。
4. 前轮真实运行中已记录 guard 拒绝同类临时补丁；没有创建 canary。此前记录的“正文提到 .go”是另一复现输入，并不是触发误拦的必要条件。

修复：仅补齐 `command` 的已有标准补丁解析入口，继续验证全部 Add/Update/Delete/Move 目标。继续使用现有文档扩展名、路径归属、requirements/constraints/CMakeLists 例外；不扩大豁免、不过滤通用动作词。正文里的路径不视为实际目标；混合代码或移动到代码文件仍走原门禁。

### 采集器（ERR-20261023-107）

1. 旧文件 `.cache/harness-runtime-phase1-4w0lbm8o/trust-acceptance/actual_runtime.py` 以 `text=True` 打开 stdout，并在每次 `readline()` 前等待 `selector.select()`。
2. 在本机 Python 3.13.11，用同样的 TextIOWrapper/BufferedReader 管道一次写入两条 JSON 行：首次 select=true；读第一行后 select=false；第二行仍能立即从 Python 缓冲读取。旧循环因此不再读第二行，直到总期限耗尽。
3. 这是已证实的采集器缺陷。前次事件确有最终回复及 Stop completed，但没有原始字节记录，**不能倒推当时 turn/completed 一定已写入管道**。模型流重连不能直接认定为该超时根因；新验收须保留完整协议事件以消除此证据缺口。

修复：Popen stdout 改为无文本包装的二进制管道；使用单一字节缓冲按换行拆分，先排空已有完整行，再等待底层可读并 `os.read()`。保留未成行和 UTF-8 分段；EOF、无数据超时、非法 JSON 分开失败。使用 monotonic deadline，不通过增加超时或看到最终回复/Stop 就假装回合完成。

## 范围、版本与兼容约束

仅修改：

- `.openspec-auto/tools/hook_common.py`：文档补丁来源字段一处修复。
- `.harness/scripts/test_runtime_wiring.py`：文档补丁对应成功/反例测试；保留原有测试。
- `.cache/harness-runtime-phase1-4w0lbm8o/trust-acceptance/actual_runtime.py`：修复原采集器，封装可导入的字节行读取器，保留原生客户端接线。
- 同目录新增 `test_actual_runtime.py`：仅采集器定向回归，不接 CI、不提升为通用框架。
- 本方案、原阶段计划、验收报告、当前 `repair-project-harness-runtime-wiring` 的相关 artifacts，以及错误日志中的这两项处置。
- `.cache` 下本次备份、复现与新验收证据；不覆盖前轮原始事件。

当前本机 Python 3.13.11，现有 Codex 0.163.0-alpha.11.1，OpenSpec 1.3.0；沿用既有版本，不引依赖。采集器只承诺当前 macOS/POSIX 管道语义，Windows 未验证。

既有接口保持：hook JSON 输出格式、两个客户端配置、6 个精确信任 key/hash 不变。采集器依然等待当前 threadId/turnId 的 `turn/completed`，新增证据目录参数是为了避免覆盖旧日志；不改变产品接口或数据存储。

本轮只改 gin_api 项目定制；不触碰 `/Users/xiaozhaofu/Ai/harness-engineering`、`/Users/xiaozhaofu/Ai/openspec-auto-bootstrap` 或全局技能源。

## 采集和实际验收设计

1. 将当前脚本逻辑放入 `main()`，导入时不创建客户端、不启动模型。小型读取器只负责 fd → JSON 行，不与 hook 策略耦合。
2. 新验收输出到 `.cache/harness-runtime-phase1-4w0lbm8o/trust-acceptance/repair-105-107/`，历史 evidence 保持原样。真实收取的协议消息及时落盘，包括终结消息；失败也保留事件与摘要。
3. `turn/completed` 必须同时匹配 threadId、turnId，状态必须 completed；EOF、超时、终结失败、JSON 无效都非零退出。最终回复和 Stop 只作为证据，不替代终结通知。
4. 实际回合只执行三项：固定 printf；用 apply_patch 创建本证据目录的 canary.txt；执行 `false && git reset --hard` 的不可达危险分支负例。末尾固定回复。不得运行其他测试/质量门禁，不重试或换写法绕过拒绝。
5. 预期：printf exit 0；纯文档补丁被放行、内容正确；安全负例被既有安全 hook 拒绝；6 条定义均有实际回调；匹配终结通知被完整收到，采集器 exit 0。
6. 运行前后核对全部既有项目/状态文件及信任摘要。只删除本回合新建的 request-scope 和内容匹配的 canary；其他会话新文件不动。失败最多同问题三轮，在继续前先解释已见原因。

## 风险、失败处理与回滚

- 新字段可能让混合代码补丁被误当文档：以全部源/目的路径判定，补充混合目标、rename、伪目标正文、路径越界和配置文本反例。
- 二进制读取可能丢半行或拆坏中文：测试合并行、分段 UTF-8、尾行、EOF 与空白行。只有完整协议消息能满足终结判定。
- 服务器真实断流/失败可能仍发生：完整记录并非零返回，不扩大为断路器、超时配置或客户端升级任务。
- 新完整日志可能带任务内容：仅本次临时会话，使用固定无秘密文本，不读取凭据、全局会话历史或环境变量值。
- 修改 handler 实现不改 hook 定义，仍要核对 6 条信任而非假定。若定义漂移或出现新待信任项，停止该依赖步骤，不扩大授权。
- 前置备份保存实际修改文件与 SHA-256；回滚仅恢复本轮修改，删除本轮新增测试，保留错误历史与验收证据。不得 reset/clean 或覆盖他人的并行修改。

## 逐步实施计划

| 步骤 | 前置条件 | 具体改动 | 检查与可复核证据 | 停止条件 | 状态 |
|---|---|---|---|---|---|
| A0 根因与方案 | 已读用户规则、105/107 原始证据及源码 | 保存本方案、回读；备份本轮目标；补当前 OpenSpec design/spec/tasks | 复现输出、方案回读、当前 change apply-ready | 证据不足或任务范围不符 | 完成 |
| A1 文档补丁 | A0 完成 | command 接入已有补丁路径提取；增加成功和反例测试 | 新反例可复现原缺陷；真实两个配置命令接收 docs/.txt 补丁放行；混合代码仍拒绝 | 非文档豁免或输出契约变化 | 完成 |
| A2 字节行采集 | A0 完成 | 二进制 stdout、读取器、完整协议保存、main 入口和匹配终结判断 | 同批多行、分片/中文、EOF/尾行、超时/坏 JSON、错会话/回合终结回归 | 吞错误、放宽完成判据或输出丢失 | 完成 |
| A3 定向回归 | A1/A2 完成 | 仅运行 Python 文档 guard 相关测试和采集器测试 | 实际命令/退出码与日志；不运行全仓 Go/脚本 lint | 本次失败未解释或连续三轮无进展 | 10+8 项通过，均 exit 0 |
| A4 真实重验 | A3 通过，6 条仍 trusted | 原生 app-server 临时会话执行固定验收；留新日志 | canary 内容、放行/拒绝、6 条回调、匹配 turn/completed、exit 0；项目/状态/信任保持 | 范围外工具调用、信任漂移、真实失败 | 两项功能通过；范围快照发现 40 个任务外文件变化，单独报告，未归因或回滚 |
| A5 交付 | A4 有实际结果 | 更新本方案、原计划/验收报告及 105/107；只在证据充分时关闭 | 源码↔测试↔实际事件交叉核对；未验证项明示 | 任一必要条件缺证据，不标通过 | 完成两项专项交付；不将全工作区范围核对标为通过 |

## 验收标准

- 合法、纯文档的 command 补丁不查询 OpenSpec 状态，正文/控制行不误导分类；混合代码与非文档目标仍受原约束。
- 一次底层写入多个 JSON 行，后续行无须新的 fd 可读事件也可逐条取出；分片和 UTF-8 正确；真 EOF、坏 JSON、真实超时不能写通过。
- 实际 canary 成功创建且内容正确，记录到本回合成功终结通知；全部 6 条 hook 有真实证据，原安全拒绝路径保持。
- 未修改其他源码、配置、hook 定义/信任或其他阶段；没有执行未授权检查。

## 执行结果

实现与定向回归已完成：文档/guard 10 项（10.789 秒），采集器 8 项（0.013 秒），均 exit 0。命令及结果保存在 `.cache/harness-runtime-phase1-4w0lbm8o/trust-acceptance/repair-105-107/targeted-tests.json`。

原生重验命令为 `PYTHONDONTWRITEBYTECODE=1 python3 .cache/harness-runtime-phase1-4w0lbm8o/trust-acceptance/actual_runtime.py --evidence-dir .cache/harness-runtime-phase1-4w0lbm8o/trust-acceptance/repair-105-107/native-run`，exit 0。本机 Codex 会话 `01a16724-ce0f-7310-8b09-960f8b5883cd`、回合 `01a16724-ce8a-7552-b013-75646fe9bb14` 在 42.783 秒完成；完整采集到匹配的 `turn/completed`，status=completed。6 条 hook 定义共 10 次回调；printf 成功、文档补丁放行且内容正确、安全负例按原规则拒绝。只清理内容核对过的 canary 及本回合新建的 request-scope，旧事件保留。

原生运行前后的用户配置、hook 定义和原有会话状态核对未变。本轮对 538 个任务外文件的快照比较发现 40 个文件变化，范围断言 exit 1（ERR-20261023-109）；来源未确认，不能宣称全工作区保持不变。本轮执行记录和原生事件仅包含上述限定操作，没有修改或回滚这些任务外文件。变化清单及实际结果见 `repair-105-107/native-result.json`。

本 change 的严格 schema 校验 exit 0，valid=true、issues=[]；不等同于功能测试或全仓门禁。两项修复分别以定向测试和原生结果验收，105/107 的处置写入错误记录；其他阶段未实施，原总任务及跨端未验证项不因本次专项通过而改为整体通过，未归档其他 change。
