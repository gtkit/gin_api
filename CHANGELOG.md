# CHANGELOG

本文件只记录**已发布版本**的对外可见变更：配置契约、公开 API、以及升级时
必须动手的迁移步骤。实现细节与内部重构不进这里——它们在 git 历史里。

破坏性变更一律用 ⚠ 标注，并附具体的迁移动作。

## 未发布

### 新增

- **AI 对话模块（`internal/module/ai`）**，基于 `github.com/gtkit/go-llm-provider/v2`。
  配置驱动多厂商，`modules.ai.providers` 里**填了 `api_key` 的条目才启用**：DeepSeek、
  通义千问（百炼）、智谱 GLM、Moonshot/Kimi、火山方舟/豆包、百度千帆、硅基流动。
  一家都没填时整个模块不装配、路由不注册，启动期打一条 Warn——注册一组永远 503 的端点
  比端点不存在更难排查。

  端点（全部要求认证，会话按用户归属）：

  | 方法 | 路径 | 说明 |
  |---|---|---|
  | POST | `/api/v1/ai/chat` | 非流式，一次性返回完整回复与 token 用量 |
  | POST | `/api/v1/ai/chat/stream` | SSE 流式，事件为 `delta` / `done` / `error` |
  | GET | `/api/v1/ai/providers` | 已启用厂商与其生效默认模型 |
  | GET | `/api/v1/ai/conversations` | 会话分页列表 |
  | GET | `/api/v1/ai/conversations/:id` | 会话详情 + 消息分页 |
  | PATCH | `/api/v1/ai/conversations/:id` | 改标题 |
  | DELETE | `/api/v1/ai/conversations/:id` | 软删会话 |

  **SSE 的两条部署注意事项**：① 认证中间件禁止 SSE 请求用 query 传令牌，前端不能用原生
  `EventSource`，要用 `fetch` + `Authorization` 头读流；② 流式路由不挂路由级超时中间件
  （那会掐断正常生成中的长流），整体预算由 `modules.ai.stream_total_timeout` 给出，
  保活由 `heartbeat_interval` 控制——模型思考期间可能几十秒不吐字，而代理按空闲断连时
  不会有任何错误信息。

  **成本相关的默认值请按自己的账单复核**：`retry_attempts` 默认 2，而重试是 at-least-once
  （Chat 接口没有幂等键），厂商已处理但响应在回程丢失时会真实地再调一次；`history_messages`
  默认 20，每一条历史在每一轮都会被重新计费成输入 token。

- **新表 `ai_conversations` / `ai_messages`**（迁移 `20260901000001_create_ai_tables.sql`）。
  ⚠ **它们明文保存用户提问与模型回复**，可能包含用户输入的个人信息。保留期限、到期清理与
  库账号读取权限须由使用方按所在司法辖区确定后再上线；`DELETE /ai/conversations/:id` 只软删
  会话头，不做物理清理。聚合边界见 `internal/repository/aiconversation/README.md`。

- 新增依赖：`github.com/gtkit/go-llm-provider/v2`、`github.com/gtkit/ssex`
  （SSE 写入器，它自己管连接级写 deadline）。

### 变更

- 413 响应 `data.max_bytes_human` 改为 IEC 单位的人类可读值（如 `10 MiB`，此前为 `10.0 MB`），
  由 `github.com/dustin/go-humanize` 产出。数值字段 `data.max_bytes` 不变，机器判定请使用它。

- ⚠ **删除 `user` 示例模块**：`GET|PUT /api/v1/user` 与 `POST /api/v1/user/deactivate` 三个端点、
  `internal/module/user`、`internal/runtime/module/user`、`internal/repository/{user,user_deactivation}`、
  模型 `UserProfile` / `UserDeactivation`、注销档案清理 worker，以及 `modules.user` 配置段一并移除。
  基线迁移里的 `user_profiles` / `user_deactivations` 两张表也已删除。

  **升级动作**：仍需要这些能力的项目从 v0.3.0 的 tag 取回对应目录；`config/env/env.yml` 里的
  `modules.user` 段必须删掉——根层是严格解码，留着它会让进程启动即失败。

- 鉴权链路的账号状态源改为 `internal/repository/useraccount`（`ValidateUser` / `ValidateVIP`）。
  它此前由 `user` 模块的 application service 提供，而把鉴权依赖挂在一个业务模块上，会让
  "删掉那个模块"连带打断鉴权链路。`sharedauth.UserVerifier` / `VIPVerifier` 两个接口不变。

- `redis.dbs` 必须含库 0 的触发条件少了一项（注销清理任务已随 `user` 模块移除），
  现为「任一 ratelimit 策略用 redis store」与「`auth.mode: local`」两项。

- **依赖整体升至当前最新稳定版**。直接依赖含 `gtkit/go-llm-provider/v2` v2.15.0、`gtkit/gincache` v1.3.1、
  `gtkit/asynqx` v1.6.0、`gtkit/msgbot` v1.4.0、`gtkit/httpc` v1.6.0、`gtkit/gojwt` v1.0.7、
  `go-sql-driver/mysql` v1.10.1、`go-playground/validator/v10` v10.30.4；间接依赖含 OpenTelemetry 1.46.0、
  `mongo-driver/v2` v2.9.1、`golang.org/x/crypto` v0.57.0 等 25 项。

- **`make check` 新增子目标 `check-modern`**：`go fix -diff` 有输出即失败。此前"`go fix -diff`
  无输出"只是工程约定，没有任何目标执行它——声明了却不执行等于没声明。
  接入时一并落地了 18 个文件的现代写法（`slices.Backward`、`maps.Copy`、
  `sync.WaitGroup.Go`、`min`、`strings.SplitSeq`、`strings.Cut`、`t.Context`、提升字段字面量），
  以及 11 处 `errors.As` → `errors.AsType[T]` 的迁移。

  **派生项目注意**：`go fix` 的 `errorsastype` 改写器对**匿名接口**目标会给出不可编译的代码
  （`errors.AsType` 的类型参数约束是 `error`，而 `errors.As` 接受任意接口）。这类位置要手写成
  嵌入 `error` 的接口，不能直接套用 `go fix` 的输出。

### 修复

- **依赖漏洞 GO-2026-6348**（gRPC HTTP/2 DATA 帧分片导致堆内存耗尽）。`govulncheck` 判定本仓代码
  实际可达（`internal/pkg/tasks/scheduler.go` → `transport.NewHTTP2Client`）。`google.golang.org/grpc`
  已随依赖升级到 v1.83.2（修复版为 v1.83.1）。

- **`jwt generate` 在 Windows 上完全不可用**。`syncDir` 对目录句柄调用 `f.Sync()`，而 Windows 的
  `FlushFileBuffers` 不接受目录句柄，恒返回 `Access is denied`。现按平台分流：Unix 保留目录 fsync，
  Windows 为 no-op（NTFS 的目录项变更走文件系统日志，用户态没有更强的替代动作）。
  普通文件的 `f.Sync()` 两个平台都保留。

- **测试集在 Windows 上大面积失败**，而 CI 只跑 `ubuntu-latest`，这些失败从不暴露。根因是若干断言
  建立在 Unix 专有语义上：文件权限位（NTFS 用 ACL，Go 恒报 0666/0777）、信号投递
  （`Process.Signal` 只支持 Kill）、符号链接（需特权）、路径分隔符。现新增
  `internal/testutil` 的平台能力声明（`RequireUnixFilePerm` / `HasUnixFilePerm` /
  `RequireSignalDelivery` / `RequireSymlink`），与既有 `RequireLoopback` 同一形态；
  跳过按**断言**而非按测试划分，跨平台成立的主判据一律保留执行。

- **新增 `.gitattributes`，把文本文件的行尾固定为 LF**。此前工作区行尾由各人的 `core.autocrlf`
  决定，Windows 默认值会 checkout 出 CRLF，而库内 blob 是 LF——于是按字节工作的东西全部失效：
  `internal/scaffold` 的标记常量以 `\n` 结尾，CRLF 下 `strings.Contains` 必然失配，
  **`make module` 生成器无法定位注入锚点**；`golangci-lint` 的 gofmt 检查会报出 300+ 条
  全部源于行尾的假阳性，使 `make check` 的 lint 步骤在该平台不可用。

  **升级动作**：已有 clone 在拉取本次变更后执行一次 `git add --renormalize .` 使工作区归一
  （提交完手头改动再做，该命令会暂存全部受影响文件）。

## v0.3.0

### 变更

- ⚠ **Go 基线由 1.26 升至 1.27**：`go.mod` 声明 `go 1.27`，builder 镜像与受限验证运行器同步为
  `golang:1.27-alpine` / `golang:1.27`（均钉多平台 OCI index 摘要）。交付二进制因此获得 1.27 的运行时
  能力，包括 `runtime/pprof` 内置的 `goroutineleak` profile（`/debug/pprof/goroutineleak`）。

  声明 `go 1.27` 会翻转两个 GODEBUG 默认值：**`tracebacklabels` 0→1**——traceback 头行开始包含
  `runtime/pprof` 的 goroutine 标签；**`x509sslcertoverrideplatform` 0→1**。本仓自身不调用
  `pprof.Labels`/`pprof.Do`，但派生项目一旦使用 goroutine 标签，标签内容会随 panic 栈与崩溃日志一起输出。

  **升级动作**：本地安装 Go 1.27+ 工具链（`make check` 所钉的 golangci-lint 2.13.1 即以 go1.27 构建）。
  本地若有 `go.work`（未入库的 workspace），其 `go` 指令也要升到 1.27.x——否则 `go list` 会因
  "module requires go >= 1.27, but go.work lists go 1.26.x" 失败，表现为 `make check` 第一步就报
  依赖闭环无法枚举。若标签含敏感信息而不希望它进入 traceback，设 `GODEBUG=tracebacklabels=0`
  （该开关官方预期长期保留）。

- ⚠ **上传库 `gouploader` 升至 v1.8.1，库自身不再输出任何日志**。v1.8.0 起 gouploader 移除了内部
  日志实现，无返回路径的错误——后台会话清理失败、被隔离的钩子 panic、宽松模式降级放行、启动期
  安全建议——改为只经 `WithOnBackgroundError` 回调上报，**未装配时丢弃**。

  本仓 `internal/pkg/upload.NewLocalUploader` 已默认把该回调接进 `logger/v2`，因此经本入口装配的
  上传器无需改动即可保留这些事件（级别取 Error：上游未提供级别区分入参，而这些事件没有第二条
  上报路径）。默认出口排在附加选项之前，传入自己的 `WithOnBackgroundError` 即可取代它。

  同时 `github.com/gtkit/logger v1.4.6` 从依赖图消失。它此前是 gouploader 的传递依赖，本仓代码
  从未 import 它，因此任何搜索 import 的检查都看不见——但 v1 的包级函数有 `zap.NewDevelopment()`
  兜底，上传路径的日志会以 Debug 全开写进 stderr，绕开本仓配置的级别、轮转与字段规范。
  `internal/pkg/upload` 下新增依赖卫兵测试，v1 若经任何传递依赖回归即失败。

  **升级动作**：绕过本仓入口、直接用 gouploader 原生 API 自行装配上传器的派生项目，需自行接上
  `WithOnBackgroundError`，否则升级后这些事件不再有任何出口。

- **新增独立端口的 pprof 性能剖析端点**，默认关闭（`pprof.enabled: false`）。开启后以独立
  `http.Server` 绑定回环地址（`pprof.host`/`pprof.port`），与业务 HTTP 端口和中间件栈完全隔离，
  避免限流、鉴权等业务中间件干扰采样。`host` 强制为字面回环 IP（`netip.Addr.IsLoopback()` 判定，
  不接受主机名，不分环境放宽）——远程排障走 SSH 端口转发。支持 `block_profile_rate` /
  `mutex_profile_fraction` 采样率配置，默认关闭；端点生命周期接入 `App.Run`，随进程优雅启停并
  复位采样率，绑定失败即报错，不会出现"配置了却没有监听"的假象。

## v0.2.0

### 变更

- ⚠ **`redis.dbs` 必须包含库 0**（启用下列任一能力时）：使用 `store: redis` 的限流策略、
  `auth.mode: local`、`modules.user.deactivation_retention_days > 0`。缺失即启动失败。

  此前不校验，而这三处在装配时固定取库 0。配 `redis.dbs: [1, 2]`（只为 cache/tasks 而设）时
  Redis 完全健康，但资源层只打开声明过的库，取库 0 得到 nil 客户端——而 nil 客户端与
  "Redis 启动期不可达"在装配处无法区分。后果是**指向错误方向的诊断**：全 fail_open 的限流
  策略静默进入进程级永久降级、注销清理 worker 不注册，日志却说 Redis 不可达。

  **升级动作**：把 `0` 加进 `redis.dbs`。错误信息会列出是哪些已启用能力要求它。

- ⚠ **`proxy.remote_ip_headers` 留空不再回落框架默认值**。此前留空时 gin 的默认
  `["X-Forwarded-For", "X-Real-IP"]` 继续生效，而随仓样例把空值注释为"不配置=不从任何
  header 取 IP"——文档与行为相反，而客户端 IP 是审计事实与限流主体的来源。

  **升级动作**：在代理后运行且依赖转发头的部署，把实际使用的头显式写进
  `proxy.remote_ip_headers`（例：`["X-Forwarded-For"]`）。留空的部署此后客户端 IP
  取自连接对端。

- ⚠ **响应中零值时间字段不再输出**。`json:"...,omitempty"` 对 `time.Time` 这类 struct
  **永不生效**（零值仍输出 `"0001-01-01T00:00:00Z"`），已改为 Go 1.24 的 `omitzero`。
  受影响的是 `BaseTimeField` 与 `make module` 生成的 Response DTO。

  **升级动作**：客户端若依赖 `created_at` / `updated_at` 字段恒存在，需改为容忍其缺失。

- ⚠ **只读路由拒绝携带请求体**（返回 400）。此前 GET 带 body 被静默放行且服务端从不读取——
  客户端以为传了参数，实际什么也没发生。

- ⚠ **`user.avatar` 只接受 http/https 且长度不超过 255**。此前 `url` 校验不限 scheme，
  实测放行 `javascript:`、`data:`、`file:`、`vbscript:`——入库后由消费端渲染成链接即是
  存储型 XSS 载体；且无长度上界，超过列宽的合法 URL 会走到写库才失败（500 而非 422）。

  **升级动作**：存量数据中的非 http(s) 头像值不会被本次改动清洗，需业务自行决定处置。

- ⚠ **`doctor --deep` 在必需依赖不可达时退出码由 2 变为 1**。此前 redis 检查把必需性
  硬编码为可选，于是 `auth.mode: local` 的部署在 Redis 挂掉时得到"仅降级"，部署脚本
  据此放行，紧接着服务因必需 Redis 失败而根本起不来。

  **升级动作**：按退出码放行的部署脚本此后会在这种情形下停下——那正是本次要恢复的行为。

- ⚠ **跨源实际请求的响应不再携带 `Access-Control-Allow-Methods`**。按 CORS 规范该头只在
  预检响应中被浏览器消费。

- ⚠ **`golangci-lint` 钉住版本由 2.11.4 升到 2.13.1**。2.11.4 用 go1.26 构建，在 Go 1.27
  的机器上对**任何**包都无法完成类型检查（`export data version 4 > 2`），使 `make check`
  整条跑不了。

  **升级动作**：本地安装 2.13.1（版本不一致时 `make tool` 会拒绝执行并提示）。

### 修复

- **带路径参数的路由，跨源预检不再被误判为"路径不存在"**。可用方法集此前由应用层用
  `路由模式 == 具体请求路径` 推导，模式含 `:id` 时永不相等，于是预检返回 404 而同一路径的
  实际请求返回 200——浏览器据此拦下一个本该成功的请求。现改为读框架已完成真实路由匹配后
  写入的 `Allow` 头。脚手架模块模板默认生成参数路由，因此每个 `make module` 产出的模块
  只要配了 `allowed_origins` 就命中。

- **迁移路径不再施加客户端 I/O 超时**。此前它复用服务进程的 30s 上限（配置无法突破），
  而该超时对 DDL 等价于给单条语句封顶；MySQL 的 DDL 不随客户端断连回滚，于是大表建索引
  超时后留下"schema 已变更、迁移版本表未记录"的静默分叉，且空库上的迁移验证永远复现不出来。

- **限流后端在仅缺 `DEL` 权限时可正常构造**。此前探针键清理失败被当成能力失败，
  使"允许 EVALSHA、不允许 DEL"这种最小权限配置让含 fail_closed 策略的进程永久起不来——
  而限流的运行期路径从不删除键，键一律靠 TTL 回收。

- **只配 `database.maxopenconn` 不再导致空闲连接池被清零**。此前空闲数的默认回填要求
  两个字段同时未配置，于是"只调大最大打开连接数"这个常见的扩容动作会得到零空闲池，
  每个请求新建 TCP + 鉴权握手再关闭，且没有任何报错。

- **请求体治理排到认证之前**。内容类型不符的请求此前要先完成令牌解析、用户查库与撤销
  查询才被 415 拒绝。

- **路径上自行注册 OPTIONS 时，跨源预检不再返回 404**。预检的可用方法改为读 gin 写的
  `Allow` 头之后，路径若注册了 OPTIONS（或 `Any`），gin 会直接匹配到那条路由、不进 405
  分支，因而不写 `Allow`——而空 `Allow` 此前的唯一含义是"路径不存在"。现在用是否匹配到
  路由来区分两种成因：匹配到则把预检交给应用自己注册的那个 handler。

- ⚠ **`proxy.trusted_proxies` 已配置而 `proxy.remote_ip_headers` 留空 = 矛盾配置，启动失败**。
  `trusted_proxies` 的唯一用途就是决定"来自哪些对端时才采信转发头"，列了代理不列头等于
  那份信任没有任何作用。静默接受的后果是代理后所有客户端共用一个限流桶（IP 维度限流
  退化成全局限流），且审计里的来源 IP 全变成代理自己。

  **升级动作**：把代理实际设置的头写进 `proxy.remote_ip_headers`（常见为
  `["X-Forwarded-For"]`）；本服务前面没有代理时清空 `proxy.trusted_proxies`。

- **`user_roles` 与 `operation_audit_logs` 纳入模型注册表**。此前它们不在 DDL 生成的
  模型清单里，导致模型漂移检测对这两张表永久失明——改动其列宽、索引或可空性不会有任何提示。

- ⚠ **`tasks.enabled` 纳入 Redis 必需性推导**，这同时改变了**运行期** readiness。

  启动期：此前 Redis 被判为可选，而入队端在拿不到客户端时无法装配，于是 Redis 启动期
  不可达时进程仍然起不来，但报出的是"producer requires a redis client"——读起来像配置缺失，
  真实原因不在错误里任何位置。现在失败发生在资源装配阶段并指明是必需资源不可用。

  **运行期（这条是行为变化，不只是错误信息）**：只启用了队列的部署，此后 Redis 掉线会让
  `/ready` 返回 503（而非此前的 200 + degraded），实例被编排系统摘流，尽管 HTTP 面完全健康。

  **升级动作**：若你的部署接受"队列不可用但继续服务 HTTP"，不要启用 `tasks.enabled`，
  或改用一个允许降级的任务投递方案——当前语义是"启用队列即要求 Redis 可用"。

- **`role.admin` 授权从恒拒绝占位改为查询权威角色源**（新增 `user_roles` 表）。
  **默认行为不变**：未建表或表中无记录时一律拒绝，与此前的恒拒绝占位一致。

  此前它是 `DenyAll` 占位——派生项目拿到的是一个编译通过、路由挂得上、100% 返回 403
  的授权位，且没有任何接线示范。现在给出可删的最小实现：`RoleVerifier` port +
  `user_roles` 表 + `roleAuthorizer`。

  它**刻意不采信 JWT 的 role claim**：令牌寿命可达数十天，其中的角色是签发那一刻的事实，
  仅凭它放行等于把"一次签发"当成"持续有效的高权限"——一个被撤销权限的账号
  在令牌自然过期前仍然畅通无阻。

  **升级动作**：需要该能力时执行 `database/migrations/*_create_user_roles_table.sql`
  （草稿，须经 DBA 审阅）并写入角色数据；不需要时按 `SCAFFOLD.md` 的删除清单移除，
  或直接不建表——两种情况下行为都与升级前相同。

  换成 IAM / IdP 同步表时只改 `providers.go` 里 `svc.roles` 的构造，authorizer 与路由不用动。

- ⚠ **认证/授权失败的对客文案改变**。`code` 与 HTTP 状态码**不变**，仅响应体 `msg` 字段的取值变化。

  此前中间件把认证失败细分成多条英文消息发给客户端（`token parse failed`、
  `token subject is invalid`、`user is unavailable`、`token has been revoked`、
  `authorization requires an authenticated principal`），`user` 模块另有一条
  `user not authenticated`。现统一为 `goerr` 的内置中文文案（认证失败 → `认证失败`，
  权限不足 → `权限不足`，依赖不可用 → `依赖服务不可用`）。

  **为什么改**：区分失败原因等于告诉调用方他手里的令牌处于哪种状态——攻击者据此
  能判断一个令牌是"从未有效"还是"曾经有效但已撤销"，后者说明该账号真实存在且曾登录过。
  本仓在刷新令牌的处置上早已采用这条原则（刷新失败一律指向"重新登录"），
  认证入口此前与它相反。

  **技术原因不再泄漏，但也没有丢**：它们改为经 cause 传入，出现在服务端日志中
  （`Item.Error()`）而不出现在响应体（`Item.Message()`）。日志因此比改动前**更详细**——
  此前多处 cause 传的是 nil，日志与对客文案是同一个字符串。

  **迁移**：按 `msg` 文案做分支的客户端需改为按 `code` 或 HTTP 状态码判断——
  后两者才是稳定契约，且本次未变。按状态码/`code` 判断的客户端无需改动。

- **`internal/pkg/console` 不再直接依赖 `github.com/mgutz/ansi`**。该包对它的全部用量是一处
  `ansi.Color`，颜色只有绿/红/黄三种，现改为包内 ANSI 转义常量。公开 API
  （`Success`/`Error`/`Warning`/`Exit`/`ExitIf`）与输出字节**逐字保持**——替换前实测
  `ansi.Color("MSG","green")` 输出 `"\x1b[0;32mMSG\x1b[0m"`（注意 `0;` 前缀），新实现按此对齐，
  并补了逐字节断言的测试（此前只断言"输出非空"，色码写错也照样通过）。

  **移除的是这条 direct require，不是 console 这个包**——它是预置工具，按"电池内置"口径保留。
  该模块也不会从 `go.mod` 消失：`gtkit/migratex` 经 `migratex/console` 仍传递依赖它，故降为
  `// indirect`。本仓不再自己持有它，上游哪天弃用它时它会自然消失。

### 依赖

- **`go.mod` 不再含 `replace` 指令**。此前有一条 `replace github.com/gtkit/smsx v1.4.3 => v1.0.0`，
  用来绕开 `smsx/providers/aliyun` 伪版本里那条指向不存在 tag 的依赖声明。`replace` 只在自身
  为主模块时生效——母版经 `gonew` 复制后，派生项目的主模块就是它，这条补丁会原样生效于每个
  派生项目，且没人知道它为何存在。现已对齐到上游的 `smsx v1.1.0` + `smsx/providers/aliyun v1.1.0`
  双 tag（二者自洽），并新增门禁判据拒绝任何 `replace`。`httpc` 一并升至 v1.5.0，Go 基线声明
  统一为 `go 1.26`。无代码改动。

### 变更

- ⚠ **`module` 与 `project init` 不再出现在服务二进制里**。二者由 `//go:build scaffold`
  门控——`module` 会重写 `internal/bootstrap/providers.go`，`project init` 带
  `--force-reinit-git` 且会永久丢弃 git 历史，对已经跑起来的派生项目都不存在正确用法。

  **迁移动作**：调用方式从 `go run . module <name>` 改为 `make module NAME=<name> ARGS="..."`
  （或裸命令 `go run -tags scaffold . module <name>`）；`project init` 同理，新增
  `make project-init NAME=<name>`。CI 或脚本里若直接调用过这两条命令，需要加上 tag。

  这不移除 `golang.org/x/tools` 与 `github.com/gofrs/flock` 这两个依赖——`go mod tidy`
  枚举全部构建约束组合，它们仍是 direct require；变化的是**默认构建的依赖闭包**不再包含
  它们（隔离前为 12 个包，现为 0），由 `check-architecture` 强制。

- **启动横幅观感增强**。字形与内容不变，改动只在呈现。

  颜色由基本绿 `\x1b[32m` 改为 256 色索引的青→蓝渐变（`51 45 39 38 32 26`），
  色带精确穿过 Go 品牌主色 Gopher Blue —— `38;5;38` = #00AFD7，与 #00ADD8 的 RGB 差
  为 (0,2,1)，肉眼不可分。整条色带落在色立方的 r=0 平面上，同色相家族推移，不会串色。

  这不只是换了个颜色：基本 8 色的绿**没有确定的 RGB**，由终端主题决定
  （macOS Terminal #00A600、xterm #00CD00、VS Code Dark+ #0DBC79）。改用 256 色索引后，
  同一份代码在各终端渲染一致——颜色从「由终端决定」变成「由代码决定」。

  末尾补一条与字形等宽的收边线，宽度按 art 实际 rune 数计算而非写死常量，换字体不会错位。
  配色集中在 `bannerPalette` 一个切片，按行下标取模；色阶数与字形行数不必相等，
  增删色阶或换字体都不需要动打印逻辑。

### 文档

- **预置能力清单补齐并加门禁**。此前漏掉告警扇出、出站 HTTP 客户端、熔断、GET 响应缓存、
  任务队列五项能力与全部内部支撑包。其中**告警扇出有对外副作用**：进程记一条告警级日志
  就会向已配置的通道各发一条消息（飞书/钉钉/企微 webhook、SMTP 邮件、阿里云短信，短信按条
  计费）——不知情的派生项目会在生产上付这个代价。现已按「默认开启 / 需接线 / 示例可删 /
  内部支撑」四种身份逐项登记，并由 `scripts/check-capability-inventory.sh` 保证穷尽
  （存在性判据，检测不到归类错误，不是清单正确性的完整保证）。
- **`README.md` 补上 `auth` 模块的四个端点**（register/login/refresh/logout）。此前只记了
  `user` 模块，写着"当前业务模块只有 user"。
- **模块生成器的文档示例此前全部无法执行**：生成器要求必填 `--access` 或 `--public`，而
  README、`cmd/README.md`、`cmd/MODULE_GENERATOR.md` 里的示例一个都没带。现已按真实约束修正
  并逐条实际执行验证。

## v0.1.2

无破坏性变更：新增配置项留空即等于原行为，从 v0.1.1 升级无需改动 env.yml。

### 安全

- **body 绑定不再受 query 污染**。gin 的 `ShouldBind` 在 Content-Type 为 form 时会
  同时从 query string 与 body 取值填充结构体（字段无 `form` tag 时回退用字段名匹配），
  攻击者改一下 Content-Type 就能用 query 注入本该只来自 body 的字段。已实测确认这条
  路是通的。母版自带端点的 DTO 字段本就是用户可控的，注入拿不到额外收益——但任何
  一个 DTO 将来加了不该由用户控制的字段（`Role`/`TenantID`/`IsAdmin`）就会变成真实
  漏洞，且加字段的人不会收到提示。
  绑定统一改用 `gtkit/ginx` 的 `BindBody`（绑定前临时清空 RawQuery），并新增架构
  传感器：`transport/http` 出现 `c.ShouldBind`/`BindJSON` 即门禁失败。
  `ShouldBindQuery` 与 `ShouldBindUri` 不在此列——它们的数据来源本就是 query 与路径。

### 变更

- **告警 webhook 底层从 `gtkit/news/fs` 换成 `gtkit/msgbot`**，多出加签、失败重试与
  钉钉 / 企业微信支持。`news.platform` 留空按飞书处理，与本字段引入之前一致。
  新增可选配置：

  ```yaml
  news:
    platform: ""     # feishu（留空即此值）/ dingtalk / wecom
    secret: ""       # 平台侧开启加签时必填
  ```

### 文档

- SSE 与 WebSocket 的写超时分层写进 guide：SSE 直接用 `gtkit/ssex`（它自己管连接级
  写 deadline——起流时解除全局 `WriteTimeout`，每次写出前后临时设置与恢复），
  **不要再挂 `StreamTimeout`**，两套机制会互相覆盖。`StreamTimeout` 保留给直接往
  `c.Writer` 写的长响应（大文件下载、自研分块推送）。
- 补记一条容易致命的约束：SSE 后台写入的 goroutine 必须在 handler 返回前退出。
  gin 的 `c.Writer` 是 Context 内部字段，handler 返回后归还对象池，晚退出一步
  写的就是别人的响应——跨请求数据泄漏，且不报错。

## v0.1.1

### ⚠ 配置契约的破坏性变更

从 v0.1.0 升级时，`config/env/env.yml` **必须**同步以下四处，否则进程启动时
配置校验会 fail-fast 拒绝启动。四条都是启动期就能发现的，不会静默降级。

**1. `auth.mode` 必须显式声明**

```yaml
auth:
  mode: "local"      # 或 "external"
```

刻意没有默认值：默认成 `local`，打算接外部身份提供方的部署会平白多出一组
无人看管的凭据端点；默认成 `external`，使用者会以为认证已就绪而实际上没有。

- `local` —— 本服务保管用户凭据，注册/登录/刷新/登出端点全部注册。
- `external` —— 身份来自外部 IdP/网关，本服务只验签与授权，不注册认证端点。

**2. `auth.refresh_replay_secret` 在 `local` 模式下必填（≥32 字节）**

```yaml
auth:
  refresh_replay_secret: "<专用随机密钥，勿复用 JWT / 限流 / 数据库密钥>"
```

它加密重放窗口内保留的轮换后 refresh token。没有它，那个 token 只能明文存进
Redis——任何能读到该 Redis 的人都能拿它换一组新令牌。

生成：`openssl rand -base64 48`

**3. `application.readtimeout` 已移除，拆成两个键**

```yaml
application:
  read_header_timeout: 10   # 读请求头上限（秒）
  read_timeout: 60          # 读整个请求上限（秒）
```

旧键只约束读请求头，而读 body 用的是固定 60s 默认值——配置里那个数字严重
低估了实际暴露面。**旧键必须删除**：新旧并存时校验器无法判断哪个是你要的，
会直接拒绝启动。

**4. `jwt.token_ttl_hours` 必须严格短于 `auth.refresh_ttl`**

两者相等（例如都是 720h）时 refresh token 不晚于 access token 过期，
因而永远用不上，续期链路事实上是断的。

推荐把 `token_ttl_hours` 留 `0`：按 `auth.mode` 取安全默认（`local` 1 小时、
`external` 720 小时）。长寿命访问令牌只放大泄露窗口——撤销要等它自然过期。

### 新增能力（默认全部关闭，按需开启）

- **出站 HTTP 客户端**（`outbound.clients`）：按下游名装配，同名复用连接池、
  每名独立池，内置总超时与响应体上限兜底。另有 `NewRetrying` 提供幂等重试
  客户端（4xx 与 501 不重试）。
- **GET 响应缓存**（`cache.enabled`）：三道防线——配置门控、只对显式挂载中间件
  的路由生效、每请求身份判据（带 `Authorization`/`Cookie`/`Range` 一律绕过）。
  `memory` 后端有界（ristretto 准入/淘汰），`redis` 后端由 maxmemory 负责。
- **进程内泛型缓存**（`internal/pkg/cache`）：singleflight 合并并发加载、
  ±10% TTL 抖动、LRU 容量上限、失败不缓存。
- **OpenTelemetry 导出**（`telemetry.enabled`）：OTLP/HTTP metrics + trace。
  关闭时埋点仍在但走 no-op，进程与"没有这一步"完全等价。
- **异步任务队列**（`tasks.enabled`）：asynq 生产端与消费端同一开关。关闭时
  入队返回明确错误而非静默丢弃。

### 修复

- 限流：`fail_open` 策略在 Redis 启动期不可达时会拒绝每一个请求（与该模式
  语义相反）。顶替执行体现在自行完成降级翻译，`local_fallback` 改用本地桶。
- 限流：判定结果的零值此前等于"放行"，现已占位为 `OutcomeUnspecified`。
- 信号监督者的收尾入口现在幂等，重复调用不再 panic。

### 工程

- `make hooks` 安装 pre-push 钩子，推送前自动跑 `make check-ci`。
- 修复 `migrate-verify` 与 `.dockerignore`：这两步此前从未跑通，
  `make tag` 因而在本仓从未走完整发布门禁。
- 新增 `check-build-context` 传感器：主二进制依赖的源码目录必须被
  `.dockerignore` 放行，漏放行会在镜像构建时才暴露。

## v0.1.0

初始版本。
