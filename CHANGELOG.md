# CHANGELOG

本文件只记录**已发布版本**的对外可见变更：配置契约、公开 API、以及升级时
必须动手的迁移步骤。实现细节与内部重构不进这里——它们在 git 历史里。

破坏性变更一律用 ⚠ 标注，并附具体的迁移动作。

## 未发布

### 变更

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
