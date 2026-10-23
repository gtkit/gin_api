# 接手文档补齐与启动横幅简化方案

## 最新范围调整：恢复渐变，只精简注释

2026-10-23 用户最新指令：“那就恢复原来的渐变颜色和收边线，只精简冗长的配色注释，已经补齐的文档保留，其他逻辑不要动。”

本节取代下方原阶段 2 的单色实施与收尾安排。原记录保留为历史，不再执行单色方案或其检查流程。

### 本轮方案与边界

- 原渐变实现已经用 `git show HEAD:cmd/server.go` 实际读取，与此前改动前内容一致；按该内容恢复六色常量、调色板、灰色收边线、每行复位、宽度计算和 writer 错误返回。
- `cmd/server.go` 只保留配色注释的精简：保留色号说明、渐变用途、行数取模与收边线宽度说明，删除长篇颜色理论及重复解释；不做语法重构。
- `cmd/server_test.go` 恢复到已读取的 HEAD 原文，撤销上一轮单色专属断言，恢复原有三个渐变/布局测试；不新增测试。
- README.md、SCAFFOLD.md、config/README.md 已补内容保持原样；processLogs、gracefulShutdown、JWT 和其他逻辑不变。Go 1.27.3、依赖和配置不变。
- 本轮不运行测试、构建、lint、vet、modern、race、覆盖率或 OpenSpec，不归档之前未完成且现已撤销的单色变更。
- 上一轮单色的 build/定向测试结果只是历史证据；完整 cmd 回归在 TestServerUsesDefaultConfigurationLoader 超时（exit 1，301.778s），不是通过，也不在本轮重跑或修复。

### 步骤与验收

| 步骤 | 前置条件 | 具体动作与交付物 | 检查及验收证据 | 停止条件 | 状态 |
|---|---|---|---|---|---|
| R1 | 最新指令、全局规则、原计划与错误记录已读取 | 保存并回读本节，核对原渐变与当前差异 | 原源码和原测试已从 HEAD 读取；三份文档已记录 SHA256 | 基线不明或包含他人改动 | 已保存，待回读 |
| R2 | R1 完成 | 恢复 cmd/server.go 原实现与 cmd/server_test.go 原文，只缩短配色注释 | git diff 中 server.go 只剩注释变化，server_test.go 无差异 | 出现任何非注释代码差异或需改其他逻辑 | 待实施 |
| R3 | R2 完成 | 回读源码、文档与本节，记录结果 | 三份文档 SHA256 与本轮前一致；保护逻辑无 diff；不运行测试或门禁 | 文档变化、保护逻辑变化或证据不一致 | 待实施 |

风险是恢复时遗漏色值、顺序、reset 或原测试，因此以已读取的原文逐项回读，最终要求只剩注释 diff。
若恢复不完整，停在本范围内对照原文修正；回滚也仅撤销本轮对这两个文件的修改，保留已补文档，不使用 reset/clean。

## 原始方案与历史记录

## 需求与授权

2026-10-23 用户明确要求：“按你建议的做，先补齐四类文档和 JWT 权限说明，再简化横幅；processLogs、gracefulShutdown 和 JWT 校验逻辑保持不变。”

四类文档为 GOPRIVATE 使用说明、CORS 存量请求头清理清单、gtkit 可选依赖精简指引、ProxySQL 下 PrepareStmt 的兼容与连接复用说明。用户已授权以下两个实施阶段，按原顺序完成，无需再次确认同一范围。

## 目标、范围与非目标

- 阶段 1 只改 README.md、SCAFFOLD.md、config/README.md，并回读核对；不为文档运行测试、构建或 OpenSpec。
- 阶段 2 只简化 cmd/server.go 中的横幅常量和 printBanner，并同步 cmd/server_test.go 中受影响的既有测试。
- 本文记录方案、阶段状态、证据与未验证项；横幅行为变更另外按既有 OpenSpec 流程处理对应变更文件。
- 不修改 processLogs、gracefulShutdown、JWT/CORS/数据库/通知运行逻辑、配置值、依赖版本、目录分层或已有历史计划。
- 不实际删除可选能力、运行 go mod tidy、生成密钥、修改本机 Go 配置、连接数据库/代理、创建容器、提交或发布。
- 不运行未经单独授权的 lint、脚本 lint、vet、modern、全量 race、覆盖率门禁及包含它们的聚合入口；普通 go test 显式传 -vet=off。

## 已核对事实与兼容约束

1. go.mod 为 Go 1.27.3，Cobra v1.10.2；gtkit 依赖以当前 go.mod 为准，不升级。
2. README.md 与 SCAFFOLD.md 已有临时 GOPRIVATE 示例；SCAFFOLD.md 把 GONOSUMDB 误写为 GONOSUMCHECK。持久配置与访问权限是不同问题，不能承诺仅设置 GOPRIVATE 就能下载私库。
3. CORS 当前允许 Content-Type、AccessToken、X-CSRF-Token、Authorization、Token、X-Token、X-User-Id、App-Token；认证读取 Authorization 和 App-Token。其他存量头的仓库外客户端用途未确认，不能直接删。
4. gtkit 依赖当前由源码静态导入；运行时关闭能力不等于从依赖图删除。精简指引必须区分默认基础依赖与按能力移除的候选，不能保证删一处后 go mod 自动清空所有间接依赖。
5. internal/platform/mysql/open.go 的 buildOpenOptions 对服务和迁移使用 WithPrepareStmt(true)，没有对应配置开关。ProxySQL 具体部署版本与配置未提供，兼容性及连接容量未实测。
6. server 经 bootstrap.NewApp → newRuntime → jwtauth.Assemble 加载 JWT。prod 私钥检查 group/other 权限，Unix 公钥始终检查 group/other 写权限；dev/test 私钥放宽。存在性、普通文件、PEM/算法/配对校验仍保留。Windows 的 mode 位不是 NTFS ACL 验证。
7. 横幅当前有六色调色板、独立灰色收边线、完整 ASCII 字形；printBanner 使用注入 writer 返回写出错误。既有布局测试和 cmd/version_test.go 中的写出失败测试依赖该入口。
8. 工作树原有未跟踪 REVIEW.md、REVIEW-my.md，保留原样。初始无已跟踪改动。

## 实施设计

### 阶段 1：文档

- README.md：保留快速示例，加引导到 SCAFFOLD 的私有模块说明，避免复制多套配置解释。
- SCAFFOLD.md：补临时/持久配置、保留已有私库匹配规则、私库认证与代理分支；以真实静态导入为依据列出 gtkit 能力/入口/保留条件/移除关联；列全八种 CORS 请求头，区分认证支持与浏览器预检许可；JWT 轮换手册链接到权限说明。
- config/README.md：说明固定开启的 PrepareStmt、没有现成关闭开关、代理预处理语句及连接复用影响、部署验证边界；说明 JWT 在 server 启动路径的实际校验、环境差异、推荐权限与 Windows 限制。
- 文档里的命令是供使用方按条件执行的示例，本次不执行。资料来源使用 Go Modules Reference 与 ProxySQL 官方文档，并标记部署未验证。

### 阶段 2：横幅

- 保留现有 ASCII 字形和等宽收边线；全部使用当前已有的蓝色 ANSI 序列，末尾一次 reset；保留前后空行、writer 注入和错误向上返回。
- 移除多色调色板、灰色收边线常量及配色理论注释；不新增配置、TTY 检测、库或抽象。
- 保留 rune 宽度计算语义；使用 Go 1.27.3 可用的 strings.SplitSeq、max。不修改命令注册、bootstrap 调用、信号或关停。
- 同步既有布局测试，删除已不适用的调色板/异色断言，验证字形与收边线、统一颜色及末尾 reset；保留现有 writer 失败回归。不给文档新增测试。

## 分步执行与验收

| 步骤 | 前置条件 | 动作与交付物 | 检查与可复核证据 | 停止条件 | 当前状态 |
|---|---|---|---|---|---|
| 0 | 用户授权与规则可读 | 保存本方案并回读；完成目标源码和资料核对 | 本文、真实导入清单、对应源码与官方资料 | 规则不可读或范围不一致 | 已完成并回读 |
| 1.1 | 0 完成 | 更新私有模块配置与 CORS 清单 | 回读 README/SCAFFOLD，对照八个头与认证读取 | 未确认的客户端语义被写成事实 | 已完成 |
| 1.2 | 1.1 完成 | 更新 gtkit 依赖精简表 | 每组入口对应实际导入；说明运行时禁用与移除区别 | 找不到声明入口或删减需要猜测 | 已完成 |
| 1.3 | 1.2 完成 | 更新 ProxySQL 与 JWT 权限说明及导航 | 回读三个文档；对照 mysql/open.go、JWT 装配/读文件、doctor；检查官方来源 | 与当前源码冲突或宣称未实测兼容 | 已完成 |
| 1.4 | 1.1–1.3 完成 | 记录阶段 1 验收 | 定向 diff 与逐项回读；不运行测试/构建/OpenSpec | 缺项或出现范围外改动 | 已完成，记录如下 |
| 2.1 | 阶段 1 完成 | 核对本机 OpenSpec 入口与相关现有 spec，生成横幅变更的 apply-ready 产物；回读本计划与错误记录 | CLI 实际输出和变更文档 | CLI/必需上下文不可用，或 spec 冲突要求超出范围 | 已完成；simplify-startup-banner apply state=ready |
| 2.2 | 2.1 完成 | 简化横幅并更新对应既有测试 | 定向 diff、完整函数与调用方回读 | 触及保护逻辑、丢失 writer 错误或范围扩大 | 已完成并回读；待测试 |
| 2.3 | 2.2 完成，本机 Go 可用 | 本机 go test -vet=off -count=1 -timeout=5m ./cmd；go build ./...，均禁用自动工具链/依赖下载；必要时用同一入口实际输出横幅到终端复核 | 命令、版本、退出码与实际结果；测试内不允许间接执行受控门禁 | 缺依赖/环境，或需要容器/未授权检查；同一问题 3 轮失败 | 待实施 |
| 2.4 | 已执行检查成功或未验证项明确 | 核对保护文件未变；校验并按流程归档本次横幅 OpenSpec 变更；更新本文并汇报 | 最终 diff、OpenSpec 输出、验收记录 | 归档冲突，或出现未授权改动 | 待实施 |

## 风险、失败处理与回滚

- 依赖精简表是后续派生项目的操作指引，本次没有执行裁剪；静态导入之外的外部调用方和实际生产配置未验证。
- CORS 删除会影响浏览器预检，必须在后续变更中确认存量调用方；本次清单不授权删头。
- ProxySQL 官方说明不替代指定版本下的联调；不把预编译缓存等同于不兼容或安全保证。
- 单色横幅改变终端展示；保留字形、宽度和错误传播，避免影响启动控制流。
- 失败先保留真实输出，记录原因，再在原范围修复；不为修复扩展到数据库、JWT、通知或历史文档。
- 回滚只逆向恢复本次文档段落、横幅和其测试的改动；保留用户原有文件及后续改动，不使用 reset/clean。

## 验收记录

- 文档：已完成。README 增加私有模块导航；SCAFFOLD 补临时/持久 Go 配置、鉴权/代理语义、八项 CORS 头、八组可选能力及十组基础依赖；config/README 补 ProxySQL/PrepareStmt 与 JWT server/doctor 的权限差异。同步纠正 SCAFFOLD 同主题的邮件/短信构造入口说明。
- 文档证据：git diff 与逐段回读；CORS 对照 cors.go/auth.go，通知对照三个 Owner/dispatch，JWT 对照 assemble.go/keyset.go/diagnostics，预编译选项对照 mysql/open.go 与本地 ormx v1.6.0。Go 配置语义对照本机 go1.27.3 的 go help private 和 Go 官方文档，ProxySQL 引用其官方连接复用说明。未运行文档测试、构建、门禁或 OpenSpec。
- 未验证项：外部 CORS 客户端用途、实际 ProxySQL 部署兼容性与连接容量、依赖实际裁剪结果。文档不将这些写成已完成行为。
- 横幅：单色实现与既有布局测试已修改并回读；配色/输出块由 77 行减至 16 行，字形及 newServerCmd 保持。OpenSpec 已生成 apply-ready 产物；普通 cmd 测试运行中。
- 编译：GOWORK=off GOTOOLCHAIN=local GOPROXY=off GOFLAGS= go build ./... 已执行，exit 0。未下载依赖或新工具链。
- 保护范围回读：git diff --exit-code -- internal/bootstrap internal/pkg/jwtauth internal/middleware internal/platform go.mod go.sum 无差异；cmd/version_test.go 的既有 writer 错误回归也未修改。
- OpenSpec：validate simplify-startup-banner --type change --strict --json --no-interactive 已执行，exit 0，valid=true、issues=[]；尚未归档。
- 本机工具：go version go1.27.3 darwin/amd64；Go 为 /usr/local/bin/go，gofmt 为 /usr/local/go/bin/gofmt。OpenSpec 使用既有 /Users/xiaozhaofu/.nvm/versions/node/v24.6.0/lib/node_modules/@fission-ai/openspec/bin/openspec.js，以 Node v24.22.0 的已核实入口执行并设置 OPENSPEC_TELEMETRY=0，未安装工具。项目存在本机 go.work；验证使用 GOWORK=off、GOTOOLCHAIN=local、GOPROXY=off、空 GOFLAGS，以独立模块和已有依赖执行。
- 保持不变的实现：待最终 diff 核对。
- lint、脚本 lint、vet、modern、全量 race、覆盖率：未获本次单独授权，未执行；全部功能完成后再询问。
