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

使用 gojwt 生成带 kid 的 Ed25519 标准 PEM 密钥（输出目录须已存在）：

```bash
mkdir -p config/pem
go run . jwt generate --kid dev-2026-10 --out-dir config/pem
```

输出 `dev-2026-10.private.pem`（PKCS#8，`PRIVATE KEY`，0600）与
`dev-2026-10.public.pem`（PKIX，`PUBLIC KEY`，0644）。`--kid` 必填，
`--out-dir` 默认 `config/pem`；命令输出的配置片段写入 `jwt.active`，
并设置必填的 `jwt.issuer` / `jwt.audiences`。

已有文件会报错且保持不变；轮换时使用新 kid，把旧公钥配置到 `jwt.previous`。
裸 `go run . jwt` 不生成密钥。命令不输出私钥内容，PEM 文件不进入 Git。

标准格式可用 Go `crypto/x509` 或 OpenSSL 检查：

```bash
openssl pkey -in config/pem/dev-2026-10.private.pem -check -noout
openssl pkey -pubin -in config/pem/dev-2026-10.public.pem -pubcheck -noout
```

仅将旧 Ed25519 原始 PEM 重编码为 PKCS#8 / PKIX 时，应保留同一密钥材料与 kid；
重新生成则是密钥轮换。标准编码本身不改变 JWT 的签名算法（EdDSA）。

## module

生成模块、repository、model、HTTP、runtime 装配、测试和 migration SQL：

开发期工具，由 `scaffold` 构建约束门控，不在服务二进制里：

```bash
go run -tags scaffold . module admin --access authenticated
go run -tags scaffold . module staff --field nickname:string --field status:uint8 --access admin
go run -tags scaffold . module draft --no-bootstrap --dry-run --access optional
```

`make module NAME=admin ARGS="..."` 是同一件事的封装形式。

完整参数和生成文件说明见 `cmd/MODULE_GENERATOR.md`。
