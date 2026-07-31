# Scaffold Commands

当前 root 命令由 Cobra 注册，命令名取自 `internal/appmeta.CommandName`，而后者由
`project.mk` 的 `APP_NAME` 生成（母版当前值 `gin_api`）。使用 `go run .` 时命令保持一致。
改命令名请改 `project.mk` 再跑 `make gen-appmeta`，不要直接改生成产物。

## server

启动 HTTP 服务和后台 worker：

```bash
go run . server
```

启动时固定加载 `config/env/env.yml`，配置缺失或关键字段无效会立即失败。

## doctor

仅加载并校验配置，不连接数据库或 Redis，不启动 worker，也不绑定监听端口：

```bash
go run . doctor
```

输出只包含环境、应用名、监听地址以及 pprof 开关状态，不输出密码、密钥或证书内容。

## jwt

生成 Ed25519 JWT 密钥：

```bash
go run . jwt
```

默认输出到：

- `config/pem/jwtpri.pem`
- `config/pem/jwtpub.pem`

PEM 文件已被 Git 忽略。

## module

生成模块、repository、model、HTTP、runtime 装配、测试和 migration SQL：

```bash
go run . module admin
go run . module staff --field nickname:string --field status:uint8
go run . module draft --no-bootstrap --dry-run
```

完整参数和生成文件说明见 `cmd/MODULE_GENERATOR.md`。
