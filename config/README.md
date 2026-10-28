# 配置说明

## 并发性能初值

当前本地配置与分发示例使用 `log.level: info`、`log.sql: 0`、`log.consolestdout: 0`、
`log.filestdout: 1`：默认只输出文件，不逐条打印正常SQL，保留错误与慢查询。
排障需要控制台时可设 `consolestdout: 1`，但两个输出开关不能同时为0。
`level`必填，允许值和生产环境限制见下方校验表；这些显式示例值不改变旧配置的加载语义。

`database.maxlifeseconds: 1800` 是连接寿命初值，省略/0同样回落1800秒，负值拒绝；
部署时结合数据库/代理连接寿命调整，避免频繁重连。本机值不是生产校准结果。

`jwt.cache_max_entries: 1024` 是成功验签缓存的显式初值，省略或0仍表示关闭，负值拒绝。
容量只限制条目数；缓存最晚在令牌exp过期，自定义解析选项绕过缓存，账号状态与Redis撤销检查仍逐次执行。
按令牌复用率、内存与锁等待测量调整，不能用缓存延后禁用账号或登出生效。

## 加载方式

运行时固定从磁盘读取：

```text
config/env/env.yml
```

项目不再使用 `go:embed`，也不通过 `-c dev/test/prod` 选择配置文件。环境由 YAML 顶层 `env` 字段决定，取值为 `dev`、`test` 或 `prod`。

首次启动：

```bash
cp config/env.yml.example config/env/env.yml
```

`config/env/env.yml` 不进入 Git。配置缺失、YAML 无法解析或关键字段校验失败时，进程 fail-fast。

### APP_ROOT 与主配置路径

`APP_ROOT` 用于指定运行根目录，不参与 `APP_` 配置键覆盖。它未设置或为空字符串时，主配置沿用现有资源查找顺序；设置为非空值时，`config.Load` 只依次考虑：

1. `<APP_ROOT>/config/env/env.yml`
2. `<APP_ROOT>/v2/config/env/env.yml`

只有前一个候选不存在时才尝试后一个。两个都不存在时，返回第一个路径的缺文件错误；路径探测遇到权限等其他错误，或选中文件后读取、解析、校验失败，都直接失败，不改读其他位置。非空根不会再回退到可执行文件上级、源码目录或工作目录中的其他配置。

相对 `APP_ROOT` 仍相对进程工作目录解释，值中的空格不会被去除；部署建议使用绝对路径。`v2` 候选为兼容既有查找规则而保留，实际部署使用情况尚未确认。

此规则适用于 `server`、`migrate`、项目 CLI 的 `make` 及 `doctor` 进入共享配置加载器的路径。依赖“显式根缺配置时从别处补齐”的启动方式会改为失败，升级前须在指定根内准备所属环境的配置。CLI 的 `make` 指项目命令，不是 Makefile 工具；帮助和无参分支是否加载配置，仍按各命令原有控制流处理。

JWT 命令有独立的配置读取规则，不支持这里的 `v2` 候选；密钥、TLS 文件和写目录仍使用各自现有的资源路径规则。主配置的逻辑根约束不等同于文件系统或符号链接隔离。

配置只使用当前字段，不保留旧键别名或专门的迁移诊断。YAML 和 `APP_` 环境变量覆盖中的未知键统一在解码阶段拒绝。

## 目录

```text
config/
├── README.md
├── env.yml.example
├── env/
│   └── env.yml
└── pem/
    ├── <kid>.private.pem
    └── <kid>.public.pem
```

`config/env/env.yml` 和 `config/pem/*.pem` 均由本机或部署系统提供。

## 配置分组

- `application`：监听地址、端口、域名、超时、时区。
- `database`：MySQL 连接和连接池。
- `redis`：地址、密码和初始化 DB 列表。
- `log`：日志级别、路径和滚动策略。
- `jwt`、`ssl`：JWT 和 TLS 文件路径。
- `auth`：本地认证或外部身份模式。
- `email`、`sms`、`news`：通知渠道与告警配置。
- `ratelimit`、`cache`：限流和响应缓存。
- `telemetry`：可观测性导出，默认关闭。
- `outbound`、`tasks`：可选出站 HTTP 和任务队列，默认未接线；恢复配置字段及装配后才能使用，见 [可选扩展](../internal/optional/README.md)。
- `cors`：允许的来源。
- `pprof`：独立性能分析端点，默认关闭。

字段结构以 `internal/platform/config/types.go` 和 `config/env.yml.example` 为准。
请求读取超时使用 `application.read_header_timeout` 和 `application.read_timeout`；
JWT 密钥路径放在 `jwt.active` 或 `jwt.previous` 的密钥配置中。

## 核心字段校验

| 字段 | 用途与约束 | 默认或回落规则 |
|---|---|---|
| `database.dbtype` | 数据库类型标识，只接受精确的 `mysql`；其他类型、大小写变体及首尾空白均拒绝 | 必填，无默认值 |
| `log.level` | 日志输出级别，去除首尾空白后仅接受 `debug/info/warn/error/dpanic/panic/fatal`，不转换大小写 | 必填，无配置默认值；空串或纯空白在配置阶段拒绝 |
| `log.path` | 主日志路径，拒绝空串和纯空白；生产绝对路径必须位于绝对 `APP_ROOT` 下 | 必填，无配置默认值；消费时去除首尾空白 |
| `database.maxlifeseconds` | 连接最大存活时间，单位秒；必须非负，负值只返回一条该字段诊断 | 省略/0 使用原有默认 1800 秒，正数按配置使用 |

校验只检查配置，不修改原值；多项错误仍一次聚合返回。连接池与 Redis 规则保持原样：
`maxopenconn` 省略/0 使用默认 25，`maxidleconn` 省略/0 使用默认 10 并钳制到生效的打开上限；
两项显式为正数时要求 idle 不超过 open。Redis 库号按启用的消费方校验，不统一强制 DB1、DB2。

## MySQL 预编译语句与 ProxySQL

`internal/platform/mysql/open.go` 的 `buildOpenOptions` 固定传入 `ormx.WithPrepareStmt(true)`，
服务连接和迁移连接都使用这组选项。当前 YAML **没有** `database.prepare_stmt` 等开关，
不能通过增加一个未被读取的字段来关闭它。

缓存通过同一 `buildOpenOptions` 给服务和迁移传入有限参数：

| 配置键 | 用途 | 省略/0 | 合法显式值 |
|---|---|---|---|
| `database.prepare_stmt_max_size` | 每个 GORM 缓存驻留的 SQL 种类上限 | 128 | 1..4096 |
| `database.prepare_stmt_ttl_seconds` | 每条缓存语句的存活秒数，独立于 SQL 执行超时 | 3600 | 1..86400 |

负数或超上界在配置加载时拒绝，两项可分别设置；`APP_DATABASE__PREPARE_STMT_MAX_SIZE`
和 `APP_DATABASE__PREPARE_STMT_TTL_SECONDS` 可覆盖 YAML。旧配置不加新键也使用有限默认值。
淘汰和过期会异步关闭语句；TTL 不是空闲超时，不因命中缓存而续期。

容量配置不是 MySQL 的全局 prepared statement 配额。部署估算可先用
`缓存种类上限 × 实际参与连接数 × 同库副本数 + 其他客户端占用`；例如 128 × 25 = 3200
只是单副本的粗略估算输入。实际占用还受连接轮换、异步关闭、SQL 种类及其他进程影响，
必须用真实 MySQL/代理指标核对，不能据此承诺瞬时上限或生产容量充分。

部署交接需确认实际配置来源、同库副本数、连接池上限、MySQL 配额和代理版本，再决定是否覆盖默认值。
本次没有读取或修改含密钥的 `config/env/env.yml`，没有真实服务或多副本容量验证。
若回滚到不认识新字段的旧二进制，**先从 YAML 和 APP_ 覆盖中移除这两个新键，再回滚二进制**，
否则旧版本的严格配置解码会拒绝未知字段；回滚不涉及数据库 schema 或历史 SQL。

ProxySQL 支持 MySQL 预编译语句，不能把启用 PrepareStmt 一概判定为不兼容。
需要关注的是预编译语句生命周期与后端连接复用：按
[ProxySQL 官方说明](https://proxysql.com/documentation/mysql-prepared-statements-pooling-and-caching)，
持有未关闭的预编译语句会限制相应客户端会话的后端连接复用；长期缓存语句会影响连接共享效率。
SQL 文本 `PREPARE` 与驱动使用的二进制预编译协议还有不同的复用约束。

接入具体部署前，应记录 ProxySQL/MySQL 版本、路由规则、连接池上限和超时，验证参数化查询、
事务、连接回收/重连和后端切换，并观察实际后端连接占用及预编译语句统计。
`database.maxopenconn` 是单个应用连接池上限，不能据此直接推断 ProxySQL 的总后端连接需求。
本仓没有针对指定 ProxySQL 部署的联调结果，不承诺任意版本/配置兼容，也不因缺少说明而认定现有查询会失败。
若部署验证表明需要关闭缓存，再单独实施开关与代码变更，并同步示例和验收；本次仍保持预编译开启。

## 通知通道与告警开关

默认 server 和 migration 均直接构造机器人、邮件、短信渠道及通知组合，再打开日志。构造发生在各组件 Open，不发送启动测试消息。配置校验与原规则一致；没有告警目标不等于无需提供已启用客户端的必填配置。

| 字段 | 用途、启用与约束 | 留空/默认与关联 |
|---|---|---|
| `news.fsurl` | 默认文本告警 http(s) webhook；显式目标只覆盖本次机器人地址 | 空跳过默认机器人发送；已注册机器人仍支持显式目标，邮件/短信不受影响 |
| `news.platform` | `feishu/dingtalk/wecom`；忽略大小写和首尾空白 | 空按飞书处理；沿用现有验证白名单 |
| `news.secret` | 平台签名密钥，不写日志 | 空不配置签名；平台要求加签时须提供 |
| `news.appid/appsecret` | 图片消息应用凭据字段 | 默认空；文本通知不读取；图片调用方构造 msgbot/feishu.App 后传给 `SendPicMsg` |
| `email.host` | SMTP 客户端开关；非空才构造 | 空禁用且其他 email 字段须为空；非空要求 from |
| `email.port` | SMTP 端口，0..65535 | 0 用库默认 25；465 通常配 implicit_ssl=true |
| `email.user/pass` | SMTP 认证账号/密码 | user 空不认证（仅可信中继）；pass 空不提供密码 |
| `email.from/from_name` | 发件地址与显示名 | host 非空时 from 必填合法地址；from_name 空不设置 |
| `email.implicit_ssl` | true 选 SMTPS | false 使用客户端强制 STARTTLS 策略 |
| `email.alert_to` | 合法告警收件地址列表 | 空不发邮件告警；非空仍要求有效 host/from；主题 `[env] service alert`，正文为原告警参数用空格连接 |
| `sms.provider` | 默认短信客户端开关，仅 `aliyun` | 空禁用且其他 SMS 字段须为空；实现可替换不等于新增供应商配置 |
| `sms.access_id/access_secret` | Aliyun 凭据 | provider 非空时必填，无默认值 |
| `sms.region_id` | 短信区域 | 空用驱动默认 cn-hangzhou |
| `sms.sign_name` | 必填全局签名 | 模板级 sign_name 非空时覆盖它 |
| `sms.templates` | 逻辑名到供应商模板的映射；每项 code 必填 | 空没有模板；每项 sign_name 空用全局签名 |
| `sms.alert_phones/alert_template` | 告警号码与逻辑模板名，须成对；模板须存在 | 同时空则不发短信告警；模板收到 `{"env": 环境名}`，供应商模板应与该参数一致 |

三个通道依次尽力发送：机器人→邮件→逐个号码短信，每次有独立 10 秒预算，也受调用取消和关闭预算约束；不增加项目重试。日志只有一个 64 条待处理队列，满队列可丢弃，不能当作可靠消息存储。所有发送有真实副作用，短信可能计费；本地验收仅用替身和回环服务。

纯日志组合可直接构造日志且不传 `logging.Options.Notification`；默认入口仍选择现有通知实现。替换、资源边界及关闭限制见 [可观测性说明](../docs/observability.md#通知渠道与替换方式)。本次没有新增配置项或部署数据迁移。

## 密钥规则

- 数据库密码、Redis 密码、JWT 私钥、支付私钥、API Key 不得提交。
- 多行 PEM 和证书使用文件挂载，不写入受版本控制的 YAML。
- `config/env.yml.example` 只保留空值或安全示例。
- 配置从外置 YAML 加载，并支持 `APP_` 前缀环境变量覆盖，双下划线分层，例如 `APP_REDIS__ADDR`。

## 配置自检

部署前可执行：

```bash
go run . doctor
```

默认只校验配置和文件元数据；`doctor --deep --check-timeout 3s` 才分别建连检查 MySQL、Redis，并在单项预算内释放连接，不初始化 JWT。未配置的可选 Redis 记为 skipped。

Redis 的地址无默认值。启用本地认证、任何 Redis 存储限流、Redis 响应缓存或任务队列时必须提供 `redis.addr`（host:port）和非空、非负、不重复的 `redis.dbs`；库号还须包含对应能力使用的 DB。无上述能力时，两项可省略或留空，启动和探针均不连接 Redis。

## JWT 标准密钥

JWT 使用 gojwt 的 Ed25519 签发：私钥为 PKCS#8 PEM（`PRIVATE KEY`，0600），
公钥为 PKIX PEM（`PUBLIC KEY`，0644），可由 Go `crypto/x509` 和 OpenSSL 读取。
服务器使用 `APP_ROOT=/app /path/to/gin_api jwt generate`，默认读取该根目录下 config/env/env.yml 的 active 完整路径和 kid；未设 APP_ROOT 时基于当前工作目录，不依赖 bin 或源码位置。
生成命令保留 APP_JWT__ACTIVE__* 环境覆盖及生产 env 权限校验。
两条路径必填且不同，绝对路径保持，相对路径基于上述根目录，目标父目录须已存在且非软链接。
显式 `--out-dir <dir> --kid <kid>` 优先，使用 kid.private.pem/kid.public.pem 且无需 env；相对显式目录基于当前工作目录。
命令拒绝覆盖已有文件；完整命令见 [cmd/README.md](../cmd/README.md#jwt)。

`jwt.active.kid` 与公钥路径在两种认证模式下均必填；`auth.mode=local` 还要求私钥路径。
`external` 只加载公钥验签，不构造签发器，也不解析或读取 active 私钥路径，路径可留空。
路径可为绝对路径或相对 `APP_ROOT` 的路径，无隐式默认密钥。
现有 `jwtpri.pem` / `jwtpub.pem` 也可显式配置，文件名不决定编码。
`jwt.previous` 仅配置仍需验签的旧公钥（最多 8 个）；`issuer` 与 `audiences` 必填。

### JWT 文件权限与启动校验

`server` 在 `bootstrap.NewApp → newRuntime → jwtauth.Assemble` 中读取密钥，
进入 `App.Run` 提供服务之前就会校验；不依赖操作者先执行 `doctor`。
`internal/pkg/jwtauth/assemble.go` 按认证模式选择签发或仅验签能力；local 模式再按
有效配置的 `env` 决定私钥是否严格检查。实际文件检查位于 `internal/pkg/jwtauth/keyset.go`。

| 文件/环境 | server 的现有规则 | 部署建议 |
|---|---|---|
| active 私钥，local 且 `env: prod` | `mode & 0077` 必须为 0，即 group/other 无任何权限；失败阻断启动 | 使用 0600，或服务进程可读的 0400；不要求权限值恰好等于 0600 |
| active 私钥，local 且 `env: dev/test` | 不强制上述权限位条件；仍校验文件存在、可读、普通文件及密钥格式 | 同样建议收紧到 0600，不把开发放宽当成生产规则 |
| active 私钥，external 各环境 | 不读取，也不要求文件存在 | 验签服务只挂载公钥 |
| active 与 previous 公钥，Unix 各环境 | `mode & 0022` 必须为 0，禁止 group/other 写入；允许他人只读 | 使用 0644 或可读的 0444，防止验签公钥被其他账户改写 |

符号链接会先解析到实际目标，再检查目标文件类型、权限与内容。目录、设备等非普通文件会被拒绝；
读取的私钥须为 Ed25519 PKCS#8 PEM，公钥须为 Ed25519 PKIX PEM；local 的 active 密钥还必须配对。
这些格式与配对校验在 dev/test 下也保留。`jwt generate` 和 `jwt reencode` 仍按各自规则
要求并处理完整密钥对，server 的 external 模式不改变两条命令的语义。

`doctor` 的 `jwt_keys` 只看元数据：Unix 下不分环境，私钥都要求 group/other 无权限，
公钥都禁止 group/other 写入。因此 dev/test 的宽权限私钥可能通过 server 的权限分支，
却被 doctor 标为失败；doctor 也不能证明 PEM 内容有效或公私钥配对。

以上权限位规则以 Unix 为准。Windows 使用 ACL，doctor 只保留存在性/普通文件检查，
不检查 ACL；server 的 prod 私钥权限位判断也不能作为 Windows ACL 安全证明。
生产交付平台为 Linux，部署时应在目标平台确认文件归属、可读性与权限，
不能把 Windows 本地生成或 doctor 通过视为生产权限验收。

### 令牌寿命与旧密钥转换

`jwt.token_ttl_hours` 正值覆盖访问令牌寿命，<=0 时 `auth.mode=local` 默认 1 小时、
`external` 默认 720 小时。`auth.refresh_ttl` 仍控制本地认证的独立刷新令牌，
不受 gojwt 内部刷新窗口参数替代。gojwt 新签发令牌包含 `auth_time`，项目刷新入口仍
通过轮换验签器验证后重新签发；`kid`、作用域与 `jti=token_id` 规则保持不变。

旧 raw Ed25519 PEM 可用编译后二进制 `./bin/gin_api jwt reencode --private-key <旧私钥> --public-key <旧公钥> --out-dir <新目录>` 保持原材料转换，服务器无需安装 Go。
新目录必须不存在，父目录须存在；固定输出 jwtpri.pem（0600）/jwtpub.pem（0644），原文件不改写。
转换后只更新 active 的两条路径（或对应 previous 的公钥路径），保留原 kid、作用域和兼容截止值。
重编码不使现有签名失效，但不延长过期或绕过吊销/截止规则；生成新密钥则须走上述轮换流程。

### 请求和密码运算容量

`application.max_inflight_requests` 控制每进程 `/api/v1` 的同时处理数：0/省略使用 128，正数显式指定，负数启动失败；不提供关闭值。满载直接返回 503，不排队，不附固定 Retry-After；`/health`、`/ready` 独立。长轮询、SSE 等将长期占用名额，接入时须另行分组和定限。

`auth.bcrypt_max_concurrent` 的 0/省略仍取有效 CPU 并发数。`auth.bcrypt_max_waiters` 使用 `null` 表示默认额外等待一批同等人数，显式 0 才禁排队，正整数指定等待人数。`auth.bcrypt_queue_timeout` 的 0/省略使用 1s，实际不超过父请求预算；只约束等待，不中断已开始的 bcrypt。负值非法；队满或排队超时返回 503，调用方取消保留取消语义。四个本地认证 JSON 路由均额外限制为 8 KiB。

这些初值未经生产容量校准，须结合吞吐、平均延迟、DB 等待和 503 调整；网关不要对过载 503 无条件重试。指标和内存预算见 [容量说明](../docs/capacity-and-memory.md)。
