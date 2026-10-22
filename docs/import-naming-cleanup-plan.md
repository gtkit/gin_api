# 全项目导入命名清理方案与实施计划

## 需求、授权与目标

来源：用户要求把本次审查发现的命名问题全部改掉，先保存并回读计划，完成后逐项验收、更新计划并汇报。本次获准完成下述全部步骤；通知通道仍停在原阶段 1，不进入阶段 2。

2026-10-22 最新收尾授权：继续完成命名清理的逐项验收、计划更新和汇报，刚才停止的检查不得重跑。本轮仅回读已有真实结果、比对当前文件内容并更新本文、相关错误记录及检查授权规则，不执行新的测试、构建或门禁。以后 lint、脚本 lint、vet、modern、全量 race 测试与覆盖率门禁，必须在本次全部功能需求完成后询问用户，只有明确允许后才能执行；旧计划不构成这些检查的再次执行授权。

目标（按用户已批准修订）：修复 1 处误导别名、2 处不必要版本别名、8 处可省略的重复原包名声明，以及项目日志门面和 HTTP 插件两组称呼不一致。最终修改 45 个 Go 文件的 48 处导入声明及 215 处必要限定名引用；另 22 处格式工具必需的原名声明保留。

非目标：不调整目录、分层、依赖方向、包声明、导出符号、函数/接口实际类型、配置、错误处理、日志内容、数据/存储、并发与资源生命周期；不引入反射、抽象、新依赖或新功能。规则更新限于用户已批准的格式工具必要声明例外，以及本次新增的检查时机与明确授权要求；不改门禁代码或配置，不启动 OpenSpec，不提交、发布或运行容器。

## 已核实事实与固定命名映射

已读取全局规则、项目规则、相关错误记录和上次审查清单。上轮审查覆盖 445 个 Go 文件、2796 处导入、195 处显式命名别名；首轮修改前已核对原 70 处清单与当前文件一致；现行 48 处清单为原清单剔除用户批准保留的 22 处，原清单及副本保留用于追溯。工作树已有大量其他修改，不能用 Git HEAD 覆盖当前工作树。

Go module 为 `github.com/gtkit/gin-api`，语言版本 `go 1.27.0`；锁定版本包括 logger/v2 v2.3.0、golang-jwt/jwt/v5 v5.3.1、gtkit/gojwt v1.1.0、golimit/v2 v2.3.0、migratex v0.4.0、smsx v1.1.0、otel v1.47.0（semconv 路径 v1.43.0）。依赖与版本保持不变。本机 workspace 使用本地 logger/v2、tools/mailx，验收须分别覆盖 workspace 和 `GOWORK=off`；使用实际核实的本机 Go 与现有 lint，不下载工具或镜像。

| 项目 | 当前事实 | 明确修改 | 等价性与验收 |
|---|---|---|---|
| 误导来源 1 处 | auth.go 用 gojwt 指代实际包名 jwt 的 golang-jwt/jwt/v5，项目另有真实 gtkit/gojwt | 改为 jwtv5，与项目该依赖其余 11 处现有写法一致；保留必要的 JWT 实现来源区分 | 导入路径和 ParserOption 实际类型不变，gtkit/gojwt 不再被另一库借名 |
| 版本别名 2 处 | 两份测试用 loggerv2，包内其他文件已用 logger | 去掉别名，限定名 loggerv2 改 logger | 第三方日志库所有 7 处导入均使用原名 logger |
| 冗余别名 8 处 | 与真实包名相同，且格式工具不要求显式声明 | 仅删除显式别名，不改使用处标识符；22 处工具必要声明保留 | jwtauth 4、ginc 2、gojwt 1、golimit 1，共 8 处清除；保留项见后文 |
| 日志门面 34 处 | 真实包名 log，已有 applog 22 处、logger 34 处 | logger 改 applog，与既有 22 处统一；避免与第三方 logger 混淆 | 项目门面 56 处统一 applog；logger 名仅用于相应第三方包或局部对象 |
| HTTP 插件 3 处 | defaulthttp 3 处，defaulthttpmiddleware 1 处 | 统一为能辨识完整插件职责的 defaulthttpmiddleware | 同一路径 4 处称呼一致 |

## 文件与逐项导入清单

以下为全部 Go 文件范围；每行同时记录全部需要改的导入。限定名只改对应导入的使用，不替换局部变量、注释、日志/错误字符串或其他库。源码和测试同步；若目标名称被局部声明遮蔽，仅允许为消除本次导入冲突进行语义明确的局部改名，并在实施记录逐一列出，不能扩大为其他接口或包改造。

| 文件 | 导入名变更 |
|---|---|
| [cmd/plugins_test.go](/Users/xiaozhaofu/go/src/Project/gin_api/cmd/plugins_test.go) | `defaulthttp` → `defaulthttpmiddleware`（`github.com/gtkit/gin-api/internal/plugin/builtin/httpmiddleware`） |
| [cmd/signals.go](/Users/xiaozhaofu/go/src/Project/gin_api/cmd/signals.go) | `logger` → `applog`（`github.com/gtkit/gin-api/internal/pkg/log`） |
| [internal/bootstrap/alerting_test.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/bootstrap/alerting_test.go) | `loggerv2` → `logger`（`github.com/gtkit/logger/v2`） |
| [internal/bootstrap/app.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/bootstrap/app.go) | `logger` → `applog`（`github.com/gtkit/gin-api/internal/pkg/log`） |
| [internal/bootstrap/assembly_contract_test.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/bootstrap/assembly_contract_test.go) | `defaulthttp` → `defaulthttpmiddleware`（`github.com/gtkit/gin-api/internal/plugin/builtin/httpmiddleware`） |
| [internal/bootstrap/http.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/bootstrap/http.go) | `logger` → `applog`（`github.com/gtkit/gin-api/internal/pkg/log`） |
| [internal/bootstrap/listener.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/bootstrap/listener.go) | `logger` → `applog`（`github.com/gtkit/gin-api/internal/pkg/log`） |
| [internal/bootstrap/migrateruntime.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/bootstrap/migrateruntime.go) | `logger` → `applog`（`github.com/gtkit/gin-api/internal/pkg/log`） |
| [internal/bootstrap/pprof.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/bootstrap/pprof.go) | `logger` → `applog`（`github.com/gtkit/gin-api/internal/pkg/log`） |
| [internal/bootstrap/processruntime.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/bootstrap/processruntime.go) | `jwtauth` → 省略（使用真实包名）（`github.com/gtkit/gin-api/internal/pkg/jwtauth`） |
| [internal/bootstrap/providers.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/bootstrap/providers.go) | `logger` → `applog`（`github.com/gtkit/gin-api/internal/pkg/log`）；`jwtauth` → 省略（使用真实包名）（`github.com/gtkit/gin-api/internal/pkg/jwtauth`） |
| [internal/bootstrap/providers_token_test.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/bootstrap/providers_token_test.go) | `jwtauth` → 省略（使用真实包名）（`github.com/gtkit/gin-api/internal/pkg/jwtauth`） |
| [internal/bootstrap/ratelimit.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/bootstrap/ratelimit.go) | `logger` → `applog`（`github.com/gtkit/gin-api/internal/pkg/log`） |
| [internal/bootstrap/runtime.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/bootstrap/runtime.go) | `jwtauth` → 省略（使用真实包名）（`github.com/gtkit/gin-api/internal/pkg/jwtauth`）；`logger` → `applog`（`github.com/gtkit/gin-api/internal/pkg/log`） |
| [internal/bootstrap/singleton_worker.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/bootstrap/singleton_worker.go) | `logger` → `applog`（`github.com/gtkit/gin-api/internal/pkg/log`） |
| [internal/bootstrap/worker_scheduler.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/bootstrap/worker_scheduler.go) | `logger` → `applog`（`github.com/gtkit/gin-api/internal/pkg/log`） |
| [internal/bootstrap/workermanager.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/bootstrap/workermanager.go) | `logger` → `applog`（`github.com/gtkit/gin-api/internal/pkg/log`） |
| [internal/middleware/auth.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/middleware/auth.go) | `gojwt` → `jwtv5`（`github.com/golang-jwt/jwt/v5`） |
| [internal/middleware/error.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/middleware/error.go) | `logger` → `applog`（`github.com/gtkit/gin-api/internal/pkg/log`） |
| [internal/middleware/jwtkeys_test.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/middleware/jwtkeys_test.go) | `gojwt` → 省略（使用真实包名）（`github.com/gtkit/gojwt`） |
| [internal/middleware/ratelimit.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/middleware/ratelimit.go) | `ginc` → 省略（使用真实包名）（`github.com/gtkit/gin-api/internal/pkg/ginc`）；`logger` → `applog`（`github.com/gtkit/gin-api/internal/pkg/log`） |
| [internal/middleware/ratelimit_middleware_test.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/middleware/ratelimit_middleware_test.go) | `ginc` → 省略（使用真实包名）（`github.com/gtkit/gin-api/internal/pkg/ginc`） |
| [internal/middleware/recovery.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/middleware/recovery.go) | `logger` → `applog`（`github.com/gtkit/gin-api/internal/pkg/log`） |
| [internal/middleware/requestid.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/middleware/requestid.go) | `logger` → `applog`（`github.com/gtkit/gin-api/internal/pkg/log`） |
| [internal/middleware/requestid_test.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/middleware/requestid_test.go) | `logger` → `applog`（`github.com/gtkit/gin-api/internal/pkg/log`） |
| [internal/middleware/routetimeout.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/middleware/routetimeout.go) | `logger` → `applog`（`github.com/gtkit/gin-api/internal/pkg/log`） |
| [internal/module/ai/transport/http/handler.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/module/ai/transport/http/handler.go) | `logger` → `applog`（`github.com/gtkit/gin-api/internal/pkg/log`） |
| [internal/pkg/auditlog/auditlog.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/pkg/auditlog/auditlog.go) | `logger` → `applog`（`github.com/gtkit/gin-api/internal/pkg/log`） |
| [internal/pkg/breaker/breaker.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/pkg/breaker/breaker.go) | `logger` → `applog`（`github.com/gtkit/gin-api/internal/pkg/log`） |
| [internal/pkg/console/console.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/pkg/console/console.go) | `logger` → `applog`（`github.com/gtkit/gin-api/internal/pkg/log`） |
| [internal/pkg/console/console_test.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/pkg/console/console_test.go) | `logger` → `applog`（`github.com/gtkit/gin-api/internal/pkg/log`） |
| [internal/pkg/httpcache/cache.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/pkg/httpcache/cache.go) | `logger` → `applog`（`github.com/gtkit/gin-api/internal/pkg/log`） |
| [internal/pkg/httpcache/envelope.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/pkg/httpcache/envelope.go) | `logger` → `applog`（`github.com/gtkit/gin-api/internal/pkg/log`） |
| [internal/pkg/httpcache/envelope_test.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/pkg/httpcache/envelope_test.go) | `logger` → `applog`（`github.com/gtkit/gin-api/internal/pkg/log`） |
| [internal/pkg/news/news.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/pkg/news/news.go) | `logger` → `applog`（`github.com/gtkit/gin-api/internal/pkg/log`） |
| [internal/pkg/response/response.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/pkg/response/response.go) | `logger` → `applog`（`github.com/gtkit/gin-api/internal/pkg/log`） |
| [internal/pkg/response/response_test.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/pkg/response/response_test.go) | `logger` → `applog`（`github.com/gtkit/gin-api/internal/pkg/log`） |
| [internal/pkg/singleton/runner.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/pkg/singleton/runner.go) | `logger` → `applog`（`github.com/gtkit/gin-api/internal/pkg/log`） |
| [internal/pkg/upload/uploader.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/pkg/upload/uploader.go) | `logger` → `applog`（`github.com/gtkit/gin-api/internal/pkg/log`） |
| [internal/plugin/builtin/logging/options_test.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/plugin/builtin/logging/options_test.go) | `loggerv2` → `logger`（`github.com/gtkit/logger/v2`） |
| [internal/router/health.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/router/health.go) | `logger` → `applog`（`github.com/gtkit/gin-api/internal/pkg/log`） |
| [internal/router/plugin_fixture_test.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/router/plugin_fixture_test.go) | `defaulthttp` → `defaulthttpmiddleware`（`github.com/gtkit/gin-api/internal/plugin/builtin/httpmiddleware`） |
| [internal/runtime/module/ai/module.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/runtime/module/ai/module.go) | `logger` → `applog`（`github.com/gtkit/gin-api/internal/pkg/log`） |
| [internal/runtime/module/auth/adapters.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/runtime/module/auth/adapters.go) | `logger` → `applog`（`github.com/gtkit/gin-api/internal/pkg/log`） |
| [pkg/ratelimit/local.go](/Users/xiaozhaofu/go/src/Project/gin_api/pkg/ratelimit/local.go) | `golimit` → 省略（使用真实包名）（`github.com/gtkit/golimit/v2`） |

## 分步实施及停止条件

| 步骤 | 前置条件 | 具体动作 | 检查方法与可复核证据 | 停止条件 | 交付物 / 状态 |
|---|---|---|---|---|---|
| P1 清单与基线 | 本文已保存并实际回读；用户授权、规则和相关错误记录一致 | 核实 70 处导入；为 59 份文件保存本轮独立副本；记录全项目 Go 文件及依赖/既有计划的散列；记录本机工具；运行改前现有受影响包测试 | 临时证据目录的 manifest、snapshot、SHA256、环境、每条命令日志与真实退出码；workspace/off 各运行 20 个受影响包的现有 race 测试 | 文件与清单不匹配、无法区分既有变更；测试基线失败先查真实原因，不能凭假定继续 | 已完成：59 份原始副本、1165 份文件散列、双环境基线各 1113 项通过 |
| P2 仅改命名 | P1 基线通过 | 按用户批准修订和上表处理 48 处 import/对应选择器，保留 22 处工具必要声明，去除 8 处冗余别名；用 Go 语法树识别导入引用，不盲改同名局部变量；gofmt 仅作用于清单文件 | 独立副本逐文件差异；去除本次 import/限定名差异后语法 token 序列与原始文件相同；48 处修改与 22 处必要声明逐项核对；旧问题与目标口径无残留 | 需要改变逻辑、实际类型/接口或其他文件范围；无法确定选择器归属 | 已完成：45 文件、48 导入、215 引用；22 处必要声明按批准保留，token 核对通过 |
| P3 回归与门禁 | P2 改名完整；原计划执行时已取得下述结果 | 历史已执行：workspace/off 同一批 20 包 race，两环境全项目 build、vet、modern、架构传感器，workspace lint 与 off 完整统一门禁；本轮只读这些结果 | 各标签 .json 的实际命令、环境、起止时间及退出码，结合对应原始 .log；详见下方历史结果表 | 用户已停止检查，本轮不得重启或重试；未来受控检查须在全部功能需求完成后另行询问并获准 | 已完成历史结果核对；最终检查 exit0，完整 make check exit0；本轮新增执行 0 次 |
| P4 逐项验收与交付 | 已有真实结果、当前文件和最新授权可核对 | 回读改名映射、差异、结果与本文；核对剩余别名/新增文件/修改范围；更新计划和本次错误记录，关闭已处置条目；汇报 | 下方 A1–A9 逐项填写真实结果，原始日志可回查；未执行/失败/未验证分别记录 | 无法用实际证据支持完成结论；不得借收尾重新运行被停止的检查 | 已完成：逐项验收、计划和相关错误记录更新并实际回读确认 |

历史统一门禁执行前已核对真实 Makefile、脚本及可用工具；不得因门禁隐式下载/安装全局工具、读取凭据、创建/构建/拉取镜像、创建容器或触达生产。需要未授权条件时保留未验证，不推定允许。历史覆盖率生成物位于本轮临时目录；本轮收尾不再生成覆盖率或启动检查。

本次不改运行行为/实际接口和配置，不启用运行行为变更的 OpenSpec 与后端准入流程，不增加镜像实现的测试、反证测试或性能基准；使用既有测试和完整语法差异核对证明本轮覆盖范围，不据此声称验证了真实通知供应商、生产性能或全平台行为。

## 风险、失败处理与回滚

主要风险是目标别名被局部声明遮蔽、旧限定名漏改、将同名局部变量/字符串一起替换、平台或构建标签未覆盖。通过语法树引用定位、逐文件 token 差异约束、编译、现有测试与标签检查处理；平台专属执行、真实服务及生产容量保持未验证。只做命名，不改变控制流、分支、defer、锁、序列化/SQL/错误/日志字符串，故没有数据迁移或补偿步骤。

失败时保留原始命令输出并记录，不将重试通过抹去先前失败，不修改本任务外的基线问题。回滚仅从独立副本撤销本轮名称变化；如文件之后被其他工作修改，先逐项合并撤销，不能整份覆盖。禁止 reset/clean、覆盖既有修改或执行提交/发布。

## 验收清单与执行记录

- [x] A1：1 处错误 gojwt 别名已修正，gtkit/gojwt 使用其真实包名。
- [x] A2：2 处 loggerv2 已恢复 logger，7 处第三方 logger 导入一致。
- [x] A3：8 处同名冗余声明逐项清除，22 处工具必要原名声明保留，原调用符号不变。
- [x] A4：日志门面 56 处统一 applog，本轮处理 34 处。
- [x] A5：HTTP 插件 4 处统一 defaulthttpmiddleware，本轮处理 3 处。
- [x] A6：45 份文件只有上述命名差异，模块、依赖版本、接口实际类型、数据/存储、配置和生命周期不变。
- [x] A7：改前/改后两环境现有受影响包 race 测试结果已核对；每项成功/失败/skip 如实记录。
- [x] A8：全项目 build/vet/lint/modern、架构传感器和统一门禁的历史结果已逐项记录；本轮未重新运行。
- [x] A9：本文、相关错误记录和检查授权规则已更新并实际回读确认；未进入通知阶段 2，未提交发布。

当前记录：P1–P4 已完成，A1–A9 已逐项验收。最终 45 个 Go 文件、48 处导入、215 处引用完成清理；22 处格式工具必要声明按用户批准保留。源码与已有最终证据一致；本轮只做收尾，不重跑任何测试或门禁。实际证据目录：`/var/folders/_w/xwv4k3t12vn9054592gsk5200000gn/T/gin-api-import-names-2gezkqg8/`；该目录为本机临时证据，不承诺永久保留，关键结果已记入本文。

## 门禁核对发现与已批准修订

2026-10-22 实际执行发现：改后 workspace/off 各 20 包、1113 项 race 测试通过，build/vet/modern/architecture 也通过；首次 GOWORK=off make check 在 tool 阶段因 22 条 goimports 问题 exit2，未执行完后续门禁。原始日志为本轮证据目录中的 make-check-locked.log/.json。

已用本机 golangci-lint fmt --diff 及 goimports -d 实际核对，二者均要求为路径与真实包名不一致的导入补回原名：response→resp 18 处、smsx→smsprovider 2 处、migratex→migrate 1 处、semconv/v1.43.0→semconv 1 处，合计 22 处。前次只据真实 package 名称就把这 22 处判为可删除冗余，判断不完整。它们不是 sdk 这种把具体库改叫泛化角色的错误，工具要求的真实包名声明应与无意义改名区分。

首次方案与全局规则一概要求省略同名声明，与工具要求冲突。用户已明确回复“同意保留 22 处并修正规则（推荐）”。该修订已经获准；全局规则例外已修改并实际回读，22 处原名声明已恢复，最终验收已完成。lint 配置和门禁代码保持原样；后续执行时机以最新授权规则为准。

用户已批准的具体修订：保持现有 lint/formatters、依赖及运行行为不变，仅为下表 22 个导入恢复其原包名显式声明；其余 48 处导入、45 个实际变更文件按原计划完成清理。全局命名规则仅补充这条已验证的必要例外：“导入路径与实际包名不一致，且项目已锁定的格式化工具要求显式写出原包名时，保留该声明；它不属于泛化、误导或无理由改名。其余同名冗余声明仍须省略。”

| 文件 | 批准保留的工具必要声明 |
|---|---|
| [cmd/migrate.go](/Users/xiaozhaofu/go/src/Project/gin_api/cmd/migrate.go) | `migrate "github.com/gtkit/migratex"` |
| [internal/router/init.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/router/init.go) | `resp "github.com/gtkit/gin-api/internal/pkg/response"` |
| [internal/router/engine_baseline_test.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/router/engine_baseline_test.go) | `resp "github.com/gtkit/gin-api/internal/pkg/response"` |
| [internal/middleware/routetimeout.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/middleware/routetimeout.go) | `resp "github.com/gtkit/gin-api/internal/pkg/response"` |
| [internal/middleware/error_test.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/middleware/error_test.go) | `resp "github.com/gtkit/gin-api/internal/pkg/response"` |
| [internal/middleware/logger_bound_test.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/middleware/logger_bound_test.go) | `resp "github.com/gtkit/gin-api/internal/pkg/response"` |
| [internal/middleware/maxbody_test.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/middleware/maxbody_test.go) | `resp "github.com/gtkit/gin-api/internal/pkg/response"` |
| [internal/middleware/maxbody.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/middleware/maxbody.go) | `resp "github.com/gtkit/gin-api/internal/pkg/response"` |
| [internal/middleware/ratelimit.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/middleware/ratelimit.go) | `resp "github.com/gtkit/gin-api/internal/pkg/response"` |
| [internal/middleware/requestbody.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/middleware/requestbody.go) | `resp "github.com/gtkit/gin-api/internal/pkg/response"` |
| [internal/middleware/auth.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/middleware/auth.go) | `resp "github.com/gtkit/gin-api/internal/pkg/response"` |
| [internal/middleware/recovery.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/middleware/recovery.go) | `resp "github.com/gtkit/gin-api/internal/pkg/response"` |
| [internal/middleware/error.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/middleware/error.go) | `resp "github.com/gtkit/gin-api/internal/pkg/response"` |
| [internal/bootstrap/validation.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/bootstrap/validation.go) | `resp "github.com/gtkit/gin-api/internal/pkg/response"` |
| [internal/platform/telemetry/telemetry.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/platform/telemetry/telemetry.go) | `semconv "go.opentelemetry.io/otel/semconv/v1.43.0"` |
| [internal/module/auth/transport/http/handler.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/module/auth/transport/http/handler.go) | `resp "github.com/gtkit/gin-api/internal/pkg/response"` |
| [internal/module/ai/transport/http/errmap.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/module/ai/transport/http/errmap.go) | `resp "github.com/gtkit/gin-api/internal/pkg/response"` |
| [internal/module/ai/transport/http/handler.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/module/ai/transport/http/handler.go) | `resp "github.com/gtkit/gin-api/internal/pkg/response"` |
| [internal/pkg/sms/sms.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/pkg/sms/sms.go) | `smsprovider "github.com/gtkit/smsx"` |
| [internal/pkg/sms/sms_test.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/pkg/sms/sms_test.go) | `smsprovider "github.com/gtkit/smsx"` |
| [internal/pkg/httpcache/envelope_test.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/pkg/httpcache/envelope_test.go) | `resp "github.com/gtkit/gin-api/internal/pkg/response"` |
| [internal/pkg/httpcache/envelope.go](/Users/xiaozhaofu/go/src/Project/gin_api/internal/pkg/httpcache/envelope.go) | `resp "github.com/gtkit/gin-api/internal/pkg/response"` |

已完成的历史执行顺序：恢复上述声明并补充全局例外 → 重建命名验收/差异证据 → workspace/off 受影响包 race、build/vet/modern/architecture 和 lint 复核 → 完整 make check → 本次按已有证据更新最终文件数、验收表和错误记录。此处为执行历史，不构成再次启动检查的授权；原失败日志保留，不能用后续通过覆盖。

用户已选择保留必要声明并修正规则，本次不调整导入格式门禁。

执行异常记录：首次格式化调用错误地写死不存在的 /usr/local/bin/gofmt（ERR-20261022-071）；已实际核实入口 /usr/local/go/bin/gofmt，-l 为空、-w exit0 且内容散列不变。首轮 token 核对涉及 70 处 import 和 215 处选择器；批准恢复 22 处声明后，最终范围为 48 处 import 和 215 处选择器。goimports 失败记为 ERR-20261022-072；用户批准的 22 处必要声明已恢复，最终完整门禁历史结果为 exit0。



补充执行记录：最终源码双环境包级检查通过；第二次完整 make check 的全项目 lint 已 0 issues，但后续单文件 lint 因本轮编排过早启动 workspace lint 而争锁失败（ERR-20261022-073，make-check-final-locked.log/.json）。独立 workspace lint exit0。结束并发检查后，make-check-serial-locked 仍因共享锁竞争 exit2；随后等待锁的完整门禁历史执行才取得 exit0，分别保留记录。

共享锁补充核对：make-check-serial-locked 在主 lint 报锁竞争而 exit2（ERR-20261022-074）；退出后 ps 未捕获存活 lint 进程，锁持有方未确认，不猜测来源。历史执行实际读取了已安装 CLI help，--allow-serial-runners 的定义是保留同一文件锁并等待；随后仅在临时 PATH 入口向真实 golangci-lint run 加入该官方等待参数，版本/配置/检查/退出码均原样保留，未删除锁或终止其他任务。make-check-waiting-locked 已于 2026-10-22 15:17:13（Asia/Taipei）结束，exit0；本轮只回读其日志，不再执行。

## 最终逐项验收

| 项目 | 实际结果与证据 |
|---|---|
| A1 依赖来源 | auth.go 的 1 处误导 gojwt 改为 jwtv5；golang-jwt/jwt/v5 的全部 12 处导入称呼一致，gtkit/gojwt 使用真实包名。 |
| A2 日志库 | 两份测试的 loggerv2 已去除；第三方 logger 的全部 7 处导入均使用真实包名 logger。 |
| A3 冗余与必要声明 | 去除 jwtauth 4、ginc 2、gojwt 1、golimit 1，共 8 处冗余声明；批准保留 resp 18、smsprovider 2、migrate 1、semconv 1，共 22 处工具必要声明。 |
| A4 日志门面 | 本轮 34 处 logger 改为 applog，项目该门面全部 56 处称呼一致。 |
| A5 HTTP 插件 | 本轮 3 处 defaulthttp 改为 defaulthttpmiddleware，项目该插件全部 4 处称呼一致。 |
| A6 变更边界 | final-scope-report.json 记录 45 文件、48 导入、215 引用，token 等价核对通过，未发生局部变量改名。本轮收尾更新规则前回读 1165 份基线散列，变化恰为这 45 份源码；当前 45 份源码逐字节等于 final-expected。规则及计划/错误记录另属本轮文档范围，不计为源码修改。 |
| A7 受影响包 | 回读原始 JSON 测试日志：改前、改后 workspace/off 每次均 20 包、1113 项测试/子测试通过，0 fail、0 skip；本轮没有执行测试。 |
| A8 项目检查 | 历史双环境 build/vet/modern/architecture 均 exit0；workspace lint exit0、0 issues；off 完整 make check exit0，覆盖率 83.1% ≥ 72.5%。workspace 完整 make check 未额外执行；不将局部检查记作该环境完整门禁。 |
| A9 交付记录 | 计划与检查授权规则已更新并回读；ERR-20261022-070、072、073、074 按历史真实结果关闭，071 既已关闭；本轮只读汇总失败另记 075 并处置。未进入通知阶段 2，未提交或发布。 |

导入审查已有最终记录为 445 个 Go 文件、2796 处导入、185 处显式命名声明（改前 195）；48 处修改均有清单，未新增 Go 文件。本轮已回读 final-acceptance-imports.json、final-manifest.json、final-scope-report.json 并比对 final-expected；历史完整差异另保存在 final-rename-only.diff，未再次运行导入扫描工具。

## 已有检查的真实执行结果（本轮未重跑）

以下全部为 2026-10-22 已结束的历史执行。工作目录为项目根目录，Go 命令设置 GOTOOLCHAIN=local；workspace 使用项目 go.work，off 设置 GOWORK=off。表中标签对应证据目录下同名 .json/.log，.json 保存完整命令、环境、起止时间及退出码。

| 标签 | 已执行命令 | UTC 完成时间 | 实际结果 |
|---|---|---|---|
| baseline-race-workspace / baseline-race-locked | /usr/local/bin/go test -json -race -count=1 -timeout=5m，追加 final-manifest.json 中固定的 20 包清单 | 06:50:04 / 06:49:59 | 各 exit0，20 包、1113 项 pass，0 fail/skip |
| final-race-workspace / final-race-locked | 同一命令与同一 20 包清单 | 07:03:39 / 07:04:19 | 各 exit0，20 包、1113 项 pass，0 fail/skip |
| final-build-workspace / final-build-locked | /usr/local/bin/go build ./... | 07:00:56 / 07:00:55 | 均 exit0 |
| final-vet-workspace / final-vet-locked | /usr/local/bin/go vet ./... | 07:00:59 / 07:01:00 | 均 exit0 |
| final-modern-workspace / final-modern-locked | /usr/local/bin/go fix -diff ./... | 07:01:03 / 07:01:03 | 均 exit0，原始日志为空 |
| final-architecture-workspace / final-architecture-locked | bash scripts/check-architecture.sh | 07:01:09 / 07:01:08 | 均 exit0 |
| final-lint-workspace | /Users/xiaozhaofu/go/bin/golangci-lint run --allow-serial-runners --timeout=5m ./... | 07:05:26 | exit0，0 issues；保留现有排除规则未匹配警告 |
| make-check-waiting-locked | make check，COVERAGE_PROFILE 指向证据目录 coverage-waiting.out；使用 waiting-tool-env.json 记录的临时 PATH 入口 | 07:17:13（开始 07:13:24） | exit0；主项目和 scripts/*.go lint、scaffold 标签 vet/modern、全量 race 与覆盖率门禁均通过，83.1% ≥ 72.5% |

历史失败同样保留：make-check-locked（06:54:41 完成）exit2，22 条 goimports 问题；make-check-final-locked（07:05:18 完成）exit2，脚本 lint 争锁；make-check-serial-locked（07:09:04 完成）exit2，主 lint 争锁。最终通过不抹去这些失败，也不宣称锁持有方已查明。

本轮收尾的两次只读工具失败已记 ERR-20261022-075：未核对 manifest 字段导致 KeyError；读取未核实存在的备用错误模板导致 cat exit1。核实真实 alias/target 字段并仅使用已有日志后，证据汇总 exit0；两次失败均未修改源码或执行测试/门禁。

合规与结论边界：命名清理完成，源码/测试引用与批准映射一致；接口实际类型、错误处理、并发、超时和资源生命周期保持原状，依据为既有 token 核对和本轮源码字节比对。真实通知投递、生产容量及其他平台专属行为未验证。本轮只修改规则、计划和错误记录；lint/脚本 lint/vet/modern/全量 race/覆盖率门禁均未再次启动，后续须按最新规则取得明确授权。
