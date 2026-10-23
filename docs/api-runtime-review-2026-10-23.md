# API 运行审查与依赖升级（2026-10-23）

## 范围与判断

基线为 `pluggable` 分支的 `31b8c2d`。保留 Gin、GORM、go-redis、gtkit/logger 等固定技术栈，继续优化当前分支的显式装配、业务边界和运行保护。当前目标是单人借助 AI 持续开发业务 API，不添加通用插件注册器、自动 DI、路由 DSL 或微服务基础设施。

本轮包括 Go 模块及工具链升级、真实 Linux/MySQL/Redis 验收、错误及并发边界修复、内存上限和冗余收敛。全部修改在当前项目目录，无工作树、提交、推送或部署。Windows 专属测试兼容保持原状。测试使用专用依赖容器和随机测试凭据，没有访问既有业务容器或发送真实外部通知。

## 依赖与交付基础

| 组件 | 升级前 → 本轮版本 | 验证重点 |
| --- | --- | --- |
| Go | go.mod 1.27.0、本机工具链 1.27.1 → 1.27.3 | Linux/Windows 使用相同安全补丁版本，Linux 官方归档 SHA256 校验 |
| gtkit/logger/v2 | 2.3.0 → 2.5.0 | 输出全关时拒绝启动、失败不替换默认实例、排空与通知所有权 |
| gtkit/gojwt | 1.1.0 → 1.2.0 | 签发、轮换解析、吊销窗口和真实登录/刷新/退出链路 |
| gtkit/go-llm-provider/v2 | 2.15.0 → 2.18.0 | 已删除策略 API 不在本项目调用路径；三种协议与流清理回归 |
| gtkit/httpc | 1.6.0 → 1.6.1 | 具名连接池、出站上限、超时与重试错误分类 |
| gtkit/json | 直接调用 v1 → /v2 2.1.1 | 模型二进制编解码、模板 import、生产 sonic 构建；启动诊断走项目日志 |
| koanf/v2 | 2.3.8 → 2.4.1 | YAML、环境覆盖、未知键拒绝和配置校验 |
| OpenTelemetry | SDK/exporter 1.46.0 → 1.47.0；otelgin 0.71.0 → 0.72.0 | API/SDK 版本对齐、指标、追踪与关停 |
| miniredis/v2、govulncheck | 2.39.0 → 2.40.0；x/vuln 1.6.0 → 1.9.0 | Redis 单测语义及漏洞扫描 |
| golangci-lint | 2.14.0 → 2.15.0 | 保留原规则；补齐 error channel 和类型断言结果检查 |
| Gitleaks / Trivy | 更新至 8.30.1 / 0.76.1，并锁定官方镜像 digest | 实际 pull 与版本输出；当前候选文件密钥扫描及原有判定器自测 |
| 构建及运行镜像 | Go 1.27.3/Alpine 3.24，固定官方多平台 digest | Go 版本与 go.mod 一致；实际执行 APK 安装及非 root 用户创建 |
| Alpine OpenSSL | 3.5.8-r0 → 3.5.9-r0 | 旧精确补丁已不在新分支仓库，核对官方 APKINDEX 并实际运行安装 |

共更新 38 个已有模块版本；另外将直接 JSON 调用迁移到 /v2。Gin 1.12.0、GORM 1.31.2、go-redis 9.23.0 等已是查询到的最新稳定版，保留现有版本。`go get -u -t` 覆盖实际使用依赖和工具，`go mod tidy` 收敛后继续锁定精确版本。未使用的上游工具/测试模块元数据不为“全图升级”强行引入应用依赖。

这里按升级前后的模块选择结果统计；go.mod 文本表现为 37 条已有版本变化，以及原本由上游选中的 renameio 从 0.1.0 升至 1.0.1 后新增为显式间接要求。

版本、升级日志、前后锁文件和官方模块查询保存在 `.cache/api-acceptance`。Go 1.27.3 修复依据见[官方发布记录](https://go.dev/doc/devel/release)；SDK 的兼容性变化见 [logger 变更记录](https://github.com/gtkit/logger/blob/v2.5.0/CHANGELOG.md)、[LLM 迁移说明](https://github.com/gtkit/go-llm-provider/blob/v2.18.0/MIGRATION.md)。数据库和 Redis 服务端的跨版本迁移不包含在 Go 依赖升级中。

主模块中 152 条直接及间接要求已查询：151 条主代理查询成功，stringx 的例外见下文。已有 GitHub Actions checkout 7.0.1、setup-go 7.0.0 与本次查询的上游稳定版一致，继续保留提交 SHA。扫描器版本依据 [Gitleaks 发布](https://github.com/gitleaks/gitleaks/releases/tag/v8.30.1)、[Trivy 发布](https://github.com/aquasecurity/trivy/releases/tag/v0.76.1)。

应用自身的 JSON 调用统一使用 /v2；旧 v1 仅作为上游间接依赖保留。v2 的 fuzzy 注册只在 jsoniter 后端提供，因此移至同名构建标签入口；默认及 sonic 原来为空操作，语义保持不变。Docker 补齐与 Makefile 一致的 sonic 标签，构建上下文白名单纳入新入口文件，避免本地构建与镜像构建走不同 JSON 后端。

## 逻辑与资源边界修复

| 问题与触发条件 | 修改后的行为 | 回归证据 |
| --- | --- | --- |
| 授权查询返回纯 context 错误，或纯取消/超时的 Join 树 | 复用既有终态中间件；取消不回包，超时 504；混入真实故障仍保留 503 | 授权及错误树分类测试红绿验证 |
| 生成的只读 GET 路由遗漏正文契约 | 在限流与认证前挂 NoBody；有正文的写路由另组声明 JSONBody | 五种访问策略的模板契约、现有正文中间件 HTTP 测试 |
| 任意 HTTP 方法名被直接用作指标标签 | 已知方法保留，其余统一 `_OTHER`；100 个未知方法只产生一条序列 | 真实 OTel ManualReader 读取 counter/histogram 断言 |
| MaxBytesReader 外再关闭原始 body | 使用标准库直接包装，Close 透传一次并保留原错误 | 真实中间件链的关闭次数及错误测试 |
| SSE 上游 panic 时心跳尚未退出 | defer 停止并 join 心跳；正常收尾也先等待再判断流状态 | synctest 阻塞写入后触发 panic，验证恢复不得提前执行 |
| SSE 心跳写失败，上游仍在静默生成 | 取消模型调用 ctx，沿既有部分结果持久化流程收尾 | synctest 验证上游等待被即时解除 |
| worker Prepare/Cleanup panic 绕过错误与回滚路径 | 三阶段统一捕获名称和栈，聚合错误，继续清理其他已准备资源 | 启动及逆序回滚的 panic 反证；原有 Run/关停测试保留 |
| 未知 worker kind/lifecycle 可注册成功 | 注册时按枚举白名单拒绝，避免无法排序与并发清理 | 未知取值 200 与合法组合测试 |
| 异常 AI 上游无限返回 JSON、SSE 行或小帧 | SDK 读取前限制解压后的响应为 64 MiB，累计正文加推理为 8 MiB | DeepSeek/OpenAI、Anthropic、Gemini 的正常响应/流/错误响应真实 SDK 测试 |
| 超限被当 EOF，或覆盖同次 I/O 故障 | 使用明确且稳定的超限错误，保留真实读取/关闭原因，不把超限重试或保存成成功结果 | 精确边界、超限、同次 UnexpectedEOF、重复读取、Close 和部分前缀测试 |
| gojwt 1.2 的小数秒被旧解析路径截断 | 每把公钥预构造 SDK 验证器，复用严格解析；按 kid 精确选键，迁移令牌有序尝试旧公钥 | 原有 PEM 迁移测试及四种时间字段、超过 2^53 的扩展整数回归 |
| SDK 为旧令牌补齐 token_id，可能放宽应用校验 | 在 SDK 规范化前检查原始双 ID，保持必填与一致要求；未来 iat 仍拒绝 | 缺 jti、缺 token_id、不一致、未来签发时间及关闭校验选项反证 |
| JWT 缓存命中绕过无 kid 的迁移截止时刻 | 迁移令牌逐次验签并检查截止，不写入长期验证缓存；具名令牌保留有界缓存 | 旧公钥签发、截止前成功后推进至截止时刻，必须拒绝 |
| JWT 返回对象共享日期和 Custom 嵌套容器 | 四个日期及嵌套业务字段均深拷贝，调用方修改不污染缓存 | 修改写入对象和两次取出对象后的独立性测试，race 检查 |

AI 上限是进程内存保护默认值，不是容量测量结论，也不能替代按部署限制并发与设置内存预算。SDK 原始协议上限预留帧开销；回复累计上限同时覆盖 reasoning-only 帧。越界 chunk 不进入累计或回调。已有可接受前缀仍按原有 `partial=true` 流程处理；非流式超限不返回成功结果。

HTTP 方法标签的处理依据 [OpenTelemetry HTTP 语义约定](https://opentelemetry.io/docs/specs/semconv/registry/attributes/http/)。新的 HTTP 包装仅用于 SDK 读取预算，原 `http.Client` 仍负责连接池关闭；目标 URL 由部署配置及 SDK 构造，HTTP DTO 不接受目标 URL。

## 删除的冗余与保留的机制

- 撤销表的纯透传结构已删除，实际 tokenstore 对象直接满足消费方接口。
- 删除无引用的 data.Requirements、借用视图的空 Close，以及仅验证空 Close 的并发测试；真实 SQL/Redis 所有者的关闭和回滚测试保留。
- 缓存装配直接取用，删除直接别名与过期插件注释；移除请求体的重复关闭包装。
- 通知标题改用普通字符串拼接，删除仅为一次拼接引入的 stringx 直接调用；migratex 仍间接依赖 stringx。
- 保留 notification 接纳、取消和排空，保留启动回滚、逆序关闭、worker 存活屏障与日志排空。这些机制对应已覆盖的资源和并发故障。
- 保留 refreshStore 的错误翻译、应用消费方接口及 dbtx 事务传播。短小相似分支继续直写，避免只有一次用途的通用帮助层。

## 与 Hertz、go-zero、Kratos 对照

| 关注点 | 官方框架能力与当前项目事实 | 本项目取舍 |
| --- | --- | --- |
| 绑定与入口保护 | Hertz 提供绑定校验；本项目已有 DTO、正文限制、JSONBody/NoBody、JWT 与访问策略 | 继续用 Gin 常规中间件，修复声明顺序和遗漏 |
| 生命周期 | Hertz 提供优雅退出；本项目已有启动预算、回滚、readiness、两次信号和排空 | 保留具体生命周期保护，清理无实际职责的包装 |
| 稳定性 | go-zero 展示超时、限流、负载保护与分层 | 已有路由预算、bcrypt 闸门、下游池及熔断；全局接纳/负载卸载参数由实际压测决定 |
| 业务与传输 | Kratos 支持 HTTP/gRPC 中间件与 protobuf 生成 OpenAPI | 当前以 Gin HTTP 为主，不为尚不存在的 RPC 引入通用传输层 |
| API 契约 | 当前有 DTO、人工约定与测试，未见机器可读 OpenAPI | 有稳定对接接口后补一套契约及差异检查，避免维护多份手工定义 |
| 性能 | 没有同规格、同业务、同 SLO 的三个框架对比 | 不作性能排名或“最优”声明；以当前业务剖析与容量测试决定优化 |

资料：[Hertz 绑定](https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/binding-and-validate/)、[Hertz 优雅退出](https://www.cloudwego.io/docs/hertz/tutorials/basic-feature/graceful-shutdown/)、[go-zero 架构](https://go-zero.dev/concepts/architecture/)、[Kratos 中间件](https://go-kratos.dev/docs/component/middleware/overview/)、[Kratos OpenAPI](https://go-kratos.dev/docs/guide/openapi/)。

## 实际验收

| 检查 | 状态与证据 |
| --- | --- |
| 升级前真实 Linux API 链路 | 通过：注册/重复注册、登录、错误口令与不存在账号同文案、刷新轮换、访问与刷新令牌撤销、bcrypt 持久化、request_id |
| 升级前真实运行生命周期 | 通过：MySQL 故障与恢复、内存/Redis 缓存及身份绕过、依赖关闭、迁移入口、两种真实信号退出 |
| 升级后定向 race | 通过：bootstrap、middleware、metrics、AI HTTP/runtime、scaffold、logging；追加读取错误边界通过 |
| 模块完整性 | 通过：tidy -diff 无差异、go mod verify 所有模块通过 |
| 架构和镜像摘要规则 | 通过；基础镜像及扫描器真实 digest 与规则检查均已核对 |
| Alpine 运行层 APK 与用户创建 | 通过：锁定 digest 内实际运行 OpenSSL 3.5.9、CA 20261020、tzdata 2026e 安装与 app 用户创建 |
| 最终源码 Linux 全量 race/integration | 通过：56 个测试包、2534 个测试项，无失败、无测试用例跳过；另外 10 个无测试文件包正常无用例 |
| 最终静态 sonic 构建与 API | 通过：CGO_ENABLED=0、trimpath、去除符号；九项真实 API/启停链路通过，静态二进制在锁定 Alpine 内以 UID 100 非 root 执行版本入口成功 |
| build/vet/lint | 通过；lint 2.15.0 为 0 issues，jsoniter 兼容构建另行通过 |
| govulncheck | Linux/Windows 最终源码调用图均通过；0 可达、0 已导入包级漏洞，1 条模块级提示 GO-2026-5932 |
| Gitleaks | 候选文件原地只读扫描约 4 MB，只有一条已登记的测试断言误报；核对内容后同步原有指纹行号为 335，未扩大豁免范围 |
| 性能样本 | 通过：24 种已有 benchmark 变体、每种 5 次，共 120 条样本；见下表，不作为线上容量结果 |
| 独立复审 | Go 修改复审无新增高置信问题；复审发现的读取错误与 APK 版本问题已处理 |

使用当前目录只读挂载 `/workspace`，运行配置另挂 `/runtime`；未复制项目工作树。测试 MySQL 数据位于专用容器 tmpfs，Redis 另启专用实例、逻辑库 15；`PLUGIN_INTEGRATION=1` 与 `REQUIRE_DB_INTEGRATION=1` 阻止缺少真实依赖时静默跳过。允许 SCRIPT FLUSH 仅针对本任务 Redis，避免影响既有服务。

实际依赖服务版本为 MySQL 8.0.32、Redis 8.0.1，来自本机已有缓存镜像，仅用于本轮独立验收。lint 使用项目缓存中的 2.15.0 工具，不修改全局安装或持久 PATH。

验收结束后，已核对任务标签并回收三个专用容器及其匿名卷，删除八份临时配置、凭据和测试 PEM。验收证据、编译产物及工具缓存保留在当前项目的缓存目录。

第一轮全量测试发现三项 doctor 断言失败和一项 JWT 精度失败。doctor 的 Redis 分级正确，夹具相对日志路径落在只读 APP_ROOT，导致额外的日志写入探针失败；两个 helper 已将日志路径设为各自临时目录，生产诊断规则不变。JWT 精度按上表修复。保留失败日志并重新执行完整套件，不以定向测试替代全量结果。

GO-2026-5932 指向 x/crypto 中不再维护的 OpenPGP 包。所选 x/crypto 已是查询到的最新稳定版，当前项目未导入该包；保留提示，不把“无可达漏洞”表述为“所有模块绝无漏洞”。验收缓存中保留 govulncheck-release-linux.json、govulncheck-release-windows.json 及相应 summary 文件；记录依据为[官方漏洞条目](https://vuln.go.dev/ID/GO-2026-5932.json)。

依赖源例外：stringx v1.1.6 在主代理的版本及 latest 查询返回 404，镜像代理仍可下载；官方校验和数据库返回的两条哈希与 go.sum 一致。该版本来自既有 migratex 依赖，未盲目降级到仓库较低版本的现有标签，也未添加本地替代模块。验收缓存中保留查询与校验和证据 stringx-availability.json；构建环境仍需可提供该已锁定版本的代理。

## 性能样本

环境为共享开发机的 WSL2 Linux/amd64，Intel i5-1135G7，Go 1.27.3，CGO=0、sonic、GOMAXPROCS=2。每种变体计时 200 ms、重复 5 次，以下为中位数；原始 120 条及汇总保存在验收缓存的 benchmark-release.log 和 benchmark-release-summary.json。编译和静态检查不计入 benchmark 计时；vet 已独立通过，benchmark 调用关闭重复 vet。

| 路径 | ns/op | B/op | allocs/op |
| --- | ---: | ---: | ---: |
| SDK 日志，被级别过滤 | 10.44 | 0 | 0 |
| SDK 日志，普通输出 | 2556 | 264 | 2 |
| SDK 日志，6 个字段 | 3339 | 264 | 2 |
| SDK 日志，带 request_id | 3854 | 840 | 6 |
| 访问日志适配，被过滤 | 539.6 | 208 | 4 |
| 访问日志适配，启用 | 3332 | 688 | 19 |
| GORM 日志适配，被过滤 | 60.98 | 0 | 0 |
| GORM 日志适配，启用 | 2189 | 928 | 9 |

SDK 日志使用真实编码器、caller 及 /dev/null 输出；访问日志包含 Gin 和 httptest recorder，GORM 日志包含查询格式化，这两种适配测试接无操作日志 sink。它们不覆盖生产磁盘、日志采集链或真实 SQL 耗时。共享机器存在调度噪声，也没有同规格升级前样本，因此仅作为后续回归基线，不给出提速比例、端到端 QPS 或框架性能排名。

## 未验证项与上线边界

全量测试、race、静态分析和漏洞调用图只能证明已覆盖路径。目标 QPS、业务数据规模、p95/p99、资源限额、故障恢复时间、持续运行观察窗口尚未提供，不能据此宣称性能最优或不存在任何隐患。真实供应商的 LLM/短信/邮件/机器人、生产 OTLP 导出、备份恢复和灰度发布也未在本轮执行。

本轮工程回归通过，适合作为继续开发业务 API 的基础；不据此出具无条件生产上线结论。完整发布镜像的 Trivy 扫描与 SBOM 未执行，当前证据包括静态编译、真实 API 和 Alpine 运行层的分项验证。

历史文档引用门禁仍失败于旧绝对路径、已移除的历史文件和未入库配置；检查器自测通过，本轮未为历史文档扩充忽略项。复制全项目的 check-generator.sh 及 security-gate.sh 不执行，分别使用生成器现有单测/模板契约，以及当前目录的漏洞和密钥扫描；不声称完整发布门禁通过。Windows 兼容问题不修补。实际部署仍需使用正确的密钥/配置权限、生产监控和按真实负载验证的资源预算。
