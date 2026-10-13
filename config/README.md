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

## 密钥规则

- 数据库密码、Redis 密码、JWT 私钥、支付私钥、API Key 不得提交。
- 多行 PEM 和证书使用文件挂载，不写入受版本控制的 YAML。
- `config/env.yml.example` 只保留空值或安全示例。
- 当前 loader 只读取 YAML；部署时应直接挂载最终配置文件，不要依赖尚未实现的环境变量覆盖。

## 配置自检

部署前可执行：

```bash
go run . doctor
```

该命令只验证配置文件，不代表 MySQL、Redis 或第三方服务已连通。

## JWT 标准密钥

JWT 使用 gojwt 的 Ed25519 签发：私钥为 PKCS#8 PEM（`PRIVATE KEY`，0600），
公钥为 PKIX PEM（`PUBLIC KEY`，0644），可由 Go `crypto/x509` 和 OpenSSL 读取。
使用 `go run . jwt generate --kid <kid> --out-dir config/pem` 生成；目录须已存在，
命令拒绝覆盖已有文件。完整命令见 [cmd/README.md](../cmd/README.md#jwt)。

`jwt.active.kid` 和两条密钥路径必填，路径可为绝对路径或相对 `APP_ROOT` 的路径；
无隐式默认密钥。现有 `jwtpri.pem` / `jwtpub.pem` 也可显式配置，文件名不决定编码。
`jwt.previous` 仅配置仍需验签的旧公钥（最多 8 个）；`issuer` 与 `audiences` 必填。

`jwt.token_ttl_hours` 正值覆盖访问令牌寿命，<=0 时 `auth.mode=local` 默认 1 小时、
`external` 默认 720 小时。`auth.refresh_ttl` 仍控制本地认证的独立刷新令牌，
不受 gojwt 内部刷新窗口参数替代。gojwt 新签发令牌包含 `auth_time`，项目刷新入口仍
通过轮换验签器验证后重新签发；`kid`、作用域与 `jti=token_id` 规则保持不变。

旧 raw Ed25519 PEM 需在本地保持原密钥材料重编码；不要用生成命令覆盖原文件。
重编码不使现有签名失效，生成新密钥则须走上述轮换流程。
