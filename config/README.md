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
    ├── jwtpri.pem
    └── jwtpub.pem
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
