# AI 模块移除方案与实施计划

日期：2026-10-23。需求来源：用户明确要求「把 internal/module/ai 模块相关代码去掉」。

## 目标与边界

- 删除 AI 业务、HTTP、运行时装配、聚合仓储、持久化模型、配置类型、启动提示及其专属测试。
- 撤下 `/api/v1/ai` 下 7 个接口；移除 `go-llm-provider/v2`、`ssex` 直接依赖及不再需要的间接依赖。
- 同步配置模板、当前能力文档及受影响的装配/生成器测试。保留用户前面补齐的 GOPRIVATE、CORS、ProxySQL、JWT 权限文档。
- 不修改其他模块、JWT 校验、processLogs、gracefulShutdown、启动横幅、目录分层或公共功能。
- 不执行数据库命令，不删除/改写历史 SQL 迁移、DDL 文件或现有数据；旧迁移仍可能在新库创建 AI 表，这一点必须在交付和使用文档说明。
- 不修改 REVIEW.md、REVIEW-my.md、旧任务计划、其他未完成 OpenSpec change、历史 CHANGELOG 或历史审查材料。
- 不运行 lint、脚本 lint、vet、modern 检查、全量 race、覆盖率或聚合门禁；这些需要功能完成后单独授权。普通 build 和针对本次影响的测试获本次实现授权。

## 已核实事实与外部耦合清单

| 耦合 | 当前事实 | 本次处理 |
|---|---|---|
| 业务与 HTTP | `internal/module/ai/` 包含 application、dto、transport/http 和专属测试 | 整体删除 |
| 装配与外部调用 | `internal/runtime/module/ai/` 创建 LLM 客户端、独立连接池/熔断重试、仓储适配器和 cleanup | 整体删除；其他出站 HTTP 能力保留 |
| HTTP 契约 | POST chat/chat/stream；GET providers/conversations/conversations/:id；PATCH、DELETE conversations/:id | 在生产路由装配测试中逐项断言 404 |
| 身份 | AI 读取用户身份，经 user_id 校验会话归属 | 删除 AI 消费端；auth、账号/角色仓储、JWT、中间件不变 |
| 持久化 | `internal/repository/aiconversation/` 管理 ai_conversations、ai_messages；模型为 AIConversation、AIMessage | 删除仓储与模型；从 cmd/migrate.go 的 DDL 模型注册中移除 |
| SQL 历史 | `database/migrations/20260901000001_create_ai_tables.sql` 及两个 AI DDL 文件已存在；是否在某部署应用未确认 | 保留原文件，不执行 Up/Down，不改数据库/schema 快照 |
| 配置 | ModuleConfigs.AI、AIModuleConfig、AIWarnings；模板有 modules.ai 与 APP_MODULES__AI__… 示例 | 删除 AI 字段、校验、告警和模板段；保留 ModuleConfigs 与 scaffold 标记、既有 modules.user 迁移提示 |
| 本机配置 | 只检查键名与权限元数据：config/env/env.yml 的 modules 子键为空；未输出配置值 | 无 AI 子树可删除，不改本机配置及其权限 |
| 依赖 | go.mod 锁定 Go 1.27.3、go-llm-provider/v2 v2.18.0、ssex v0.2.2 | 仅清理失去用途的依赖，保留其他版本 |
| 共享能力 | ginx、goerr、ormx、httpc、middleware.Timeout/StreamTimeout、模块配置容器/生成器仍有其他消费者 | 保留，更新指向已移除依赖的说明 |
| 生成器 | providers.go 中 rateLimitStager 为生成模块接线使用；fixture 内嵌 AI 模块 | 保留该接线变量和标记，删除 fixture 中 AI 示例；scaffold 标签回归验证 |
| 现有工作区 | README.md、SCAFFOLD.md、config/README.md 已有用户授权的文档修改；REVIEW*.md 和旧计划为既存未跟踪文件 | 修改前备份既有文档，仅修改 AI 相关段 |
| 测试限制 | ERR-20261023-091 记录 cmd 全包测试的默认配置回落会启动本机服务并等待关停 | 不跑该用例，不更改其逻辑；cmd 仅运行 DDL 注册等相关定向测试 |

## 实施文件与兼容性

删除目录：`internal/module/ai/`、`internal/runtime/module/ai/`、`internal/repository/aiconversation/`。

删除文件：`internal/models/ai_conversation.go`、`internal/models/ai_message.go`、`internal/platform/config/module_ai.go`、`internal/platform/config/module_ai_test.go`。

修改文件：`internal/bootstrap/providers.go`、`processruntime.go`、`assembly_contract_test.go`、`lifecycle_contract_test.go`、`notification_test.go`；`internal/platform/config/modules.go`、`load_test.go`；`cmd/migrate.go`；`internal/scaffold/module_test.go`；`internal/middleware/routetimeout.go`（仅相关说明）；`go.mod`、`go.sum`；`config/env.yml.example`、`config/README.md`、`README.md`、`SCAFFOLD.md`、`database/README.md`；本计划和本次 OpenSpec 文件。后续若发现遗漏引用，先核实是否属于 AI，再更新此清单。

兼容性：这是明确撤销 AI API 的行为变更。存量部署须移除 modules.ai 配置与 APP_MODULES__AI__ 前缀环境变量，否则严格解码将拒绝未知键；不保留无效配置或静默兼容分支。公共模块配置容器与其他配置语义不变。历史数据保留但本服务不再提供 AI 数据访问接口。

## 风险、失败与回滚

- 主要风险：漏删引用导致构建失败；误删公共代码使认证/生成器回归；旧配置启动失败；误动已应用迁移。
- 对策：先按引用清单实施，再离线构建、执行受影响包普通测试、对生产装配路由发 httptest 请求、执行 scaffold 标签测试。不得以假数据库测试声称真实数据库或生产验收通过。
- 本机 Go 为 go1.27.3 darwin/amd64；构建/测试使用 `GOWORK=off GOTOOLCHAIN=local GOPROXY=off GOFLAGS=`，避免本机 go.work 替换遮蔽缺依赖；测试显式 `-vet=off`。
- 依赖清理使用本地缓存，禁止自动升级其他版本；离线缺依赖时记录缺口并停止相关步骤，不安装工具或启动容器。
- 失败先读取报错、定位到本次影响，记录实际证据；同一问题 3 轮修正仍失败则停止并说明。发现非 AI 问题不顺手修复。
- 回滚只逆转本次修改：被删源码从当前基线恢复，已有文档从修改前备份恢复后重放用户原有改动；不得 reset/clean 整个工作区。无数据库变更，因此无数据库回滚。

## 分阶段实施与验收

| 步骤 | 前置条件 | 具体操作 | 检查方法与证据 | 停止条件 | 交付物 | 状态 |
|---|---|---|---|---|---|---|
| 1 范围确认 | 已读用户最新指令、全局规则、项目规则、错误记录和引用 | 保存并回读本方案；按 CLI 指引建立本次 OpenSpec proposal/design/specs/tasks 至 apply-ready；保留既有文档备份 | 计划回读与 OpenSpec apply 状态 | 计划不一致或未知耦合影响范围 | 本计划、本次 change、备份 | 已完成：CLI state=ready，47 文件已备份 |
| 2 删除实现 | 步骤 1 完成 | 删除 AI 专属文件、装配/配置/DDL 注册/fixture 引用，更新路由与配置回归用例 | diff 与精确引用搜索；保留公共标记和其他模块 | 非 AI 文件需改行为、发现数据/历史迁移需更改 | AI 移除源码与相关测试 | 已完成：30 个专属文件已删，公共标记保留，运行代码残留扫描无命中 |
| 3 配置文档依赖 | 步骤 2 完成 | 删除模板 AI 段和无效环境示例；更新现行文档；离线 tidy 清理依赖 | 回读配置及文档，检查其他版本未变；保留历史迁移说明 | 依赖下载/升级或变动超出已确认范围 | 模板、文档、go.mod/go.sum | 已完成：模板/文档回读；tidy exit 0，移除 3 个模块，其余版本不变 |
| 4 普通验证 | 步骤 2、3 完成 | 离线 go build ./...；config/bootstrap/middleware/router 普通测试；cmd DDL 注册定向测试；scaffold 标签回归 | 记录实际命令、退出码、结果；AI 路由全 404、health/认证配置/生成器现有契约通过 | 失败不能定位至本次、触发外部数据库/容器或受控检查 | 真实验证记录 | 已完成：以下命令均 exit 0 |
| 5 收尾 | 步骤 4 已执行并记录结果 | 回读完整 diff；更新任务状态；严格校验/归档本次 OpenSpec；报告未执行检查和历史迁移边界 | 当前文件、依赖、路由与文档一致；不声称生产准入 | 未处理的本次缺陷或 OpenSpec 不一致 | 完成说明 | 已完成：本次 change 已归档，两项最终 spec 严格校验通过 |

## 实际结果

实施前备份：`/var/folders/_w/xwv4k3t12vn9054592gsk5200000gn/T/gin-api-remove-ai-bplkdror`，包含 47 个本次相关文件及 files.txt 清单。

已回读方案，OpenSpec `remove-ai-module` 的 apply 指令返回 state=ready。源码、模板、文档删除已执行；离线 go mod tidy exit 0，只移除 go-llm-provider/v2、ssex 和间接 go-openai，未升级或新增其他依赖。

验证范围补充：scaffold 的 `TestGeneratedMigrationsMatchExecutor` 会调用 SQL Lint，按受控检查边界跳过；其余生成器普通测试已执行。cmd 只执行受管模型注册定向用例，避开已知真实配置回落用例。

补丁工具错误与路径/依赖示例纠正已如实记录为 `.harness/error-journal.md` 的 ERR-20261023-092；错误记录文件属于本次按项目规则追加的交付项。

### 本轮真实验证

环境：工作区路径 `/Users/xiaozhaofu/go/src/Project/gin_api`；本机 go1.27.3 darwin/amd64；以下 Go 命令统一前缀为 `GOWORK=off GOTOOLCHAIN=local GOPROXY=off GOFLAGS= /usr/local/bin/go`。

| 命令 | 实际结果 |
|---|---|
| `mod tidy` | exit 0；仅移除两个 AI 直接依赖及 go-openai 间接依赖 |
| `build ./...` | exit 0 |
| `test -vet=off -count=1 -timeout=3m ./internal/platform/config ./internal/bootstrap ./internal/middleware ./internal/router` | exit 0；包用时依次为 3.351s、7.327s、3.823s、3.098s |
| `test -vet=off -count=1 -timeout=1m -run '^TestEveryManagedModelIsRegisteredForDDL$' ./cmd` | exit 0；4.302s |
| `test -vet=off -count=1 -timeout=3m -tags=scaffold -skip '^TestGeneratedMigrationsMatchExecutor$' ./internal/scaffold` | exit 0；4.906s；明确跳过内部 SQL Lint 用例 |

已覆盖：生产 buildEngine 装配下原 7 个 AI 路由逐项为 404，health 为 200，external 身份模式无本地登录路由；缺少本地认证限流引用仍拒绝装配；AI YAML/环境变量被拒绝；配置模板与保留模型注册一致；生成器标记和已存在生成测试通过。

静态核对：运行代码、go.mod/go.sum、配置模板不存在 AI 专属类型/包导入；30 个专属文件和 3 个目录已删除；三个历史 SQL 文件与 HEAD 字节一致，cmd/server.go 未变。与任务前备份逐项比较，交接文档差异只涉及本次 AI 内容。

未执行：lint、vet、modern、全量 race、覆盖率、SQL Lint、完整 cmd 回归、真实服务/数据库/外部 LLM 联调、生产容量验收。未连接数据库或操作容器。上述本地结果不构成生产准入结论。

### 收尾与合规核对

- OpenSpec `validate remove-ai-module --type change --strict --json --no-interactive`：exit 0、valid=true、issues=[]。
- `archive remove-ai-module -y`：exit 0；确认输出为 Specs updated successfully，并归档至 `openspec/changes/archive/2026-10-23-remove-ai-module/`。归档器打印 proposal 非阻断格式提示，但实际新增 3 项移除契约并删除 1 项 AI 熔断要求，未改公共出站 HTTP 要求。
- 归档后分别严格校验 `ai-module-retirement` 与 `outbound-http-resilience`：均 exit 0、valid=true、issues=[]。
- 项目错误记录 ERR-20261023-092 已按真实处理结果关闭；未处理或改写旧错误条目。
- 原架构、公共错误/并发/超时/资源管理保持不变；processLogs、gracefulShutdown、JWT 校验与横幅源码未修改。没有新增凭据或外部操作。
- 本次受影响普通测试和逐项文档对照完成；现代化自动检查和其他受控检查保持未执行。当前交付是本地源码删除完成，不声明生产准入。
