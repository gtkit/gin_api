# 配置说明

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
- `email`、`sms`、`aliyun`、`news`、`baidu`：外部服务配置。
- `pay`：微信支付和支付宝配置；未启用时不要求密钥。
- `llm`：provider、模型、超时和会话配置。
- `cors`：允许的来源。
- `pprof`：独立性能分析端点，默认关闭。

字段结构以 `internal/platform/config/types.go` 和 `config/env.yml.example` 为准。

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

`jwt.active.kid` 和两条密钥路径必填，路径可为绝对路径或相对 `APP_ROOT` 的路径；
无隐式默认密钥。现有 `jwtpri.pem` / `jwtpub.pem` 也可显式配置，文件名不决定编码。
`jwt.previous` 仅配置仍需验签的旧公钥（最多 8 个）；`issuer` 与 `audiences` 必填。

`jwt.token_ttl_hours` 正值覆盖访问令牌寿命，<=0 时 `auth.mode=local` 默认 1 小时、
`external` 默认 720 小时。`auth.refresh_ttl` 仍控制本地认证的独立刷新令牌，
不受 gojwt 内部刷新窗口参数替代。gojwt 新签发令牌包含 `auth_time`，项目刷新入口仍
通过轮换验签器验证后重新签发；`kid`、作用域与 `jti=token_id` 规则保持不变。

旧 raw Ed25519 PEM 可用编译后二进制 `./bin/gin_api jwt reencode --private-key <旧私钥> --public-key <旧公钥> --out-dir <新目录>` 保持原材料转换，服务器无需安装 Go。
新目录必须不存在，父目录须存在；固定输出 jwtpri.pem（0600）/jwtpub.pem（0644），原文件不改写。
转换后只更新 active 的两条路径（或对应 previous 的公钥路径），保留原 kid、作用域和兼容截止值。
重编码不使现有签名失效，但不延长过期或绕过吊销/截止规则；生成新密钥则须走上述轮换流程。
