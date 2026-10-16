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

使用 gojwt 生成带 kid 的 Ed25519 标准 PEM。默认按 env 配置的完整路径生成：

```bash
APP_ROOT=/app /path/to/gin_api jwt generate
```

读取 `/app/config/env/env.yml` 的 `jwt.active.kid`、`private_key_path`、`public_key_path`；
保留配置的完整文件名，不强制改成 kid 前缀。两条路径必须显式非空且不同，kid 必须有效。
相对路径基于 APP_ROOT，绝对路径保持；APP_ROOT 未设置时，env 文件和相对路径均基于当前工作目录。
命令不根据 bin 层级或源码目录查找；只读取密钥项，保留既有 `APP_JWT__ACTIVE__*` 覆盖层与生产配置文件安全校验。
两个目标的父目录都必须已存在且不是符号链接；可分别位于不同目录。

显式参数优先：`--kid` 可覆盖 env 中的 kid；传 `--out-dir` 时必须同时显式传 `--kid`，
此模式不要求 env 文件存在，沿用原有文件名规则：

```bash
mkdir -p /app/new-keys
/path/to/gin_api jwt generate --kid new-2026-10 --out-dir /app/new-keys
```

输出 `new-2026-10.private.pem`（PKCS#8，PRIVATE KEY，0600）与
`new-2026-10.public.pem`（PKIX，PUBLIC KEY，0644）。相对 `--out-dir` 基于当前工作目录，
显式空目录参数报错。已有任一目标（含软链接）会报错并保持不变，失败不会留下半套新密钥。
命令输出 kid 与路径配置片段，不自动修改 YAML，也不输出私钥内容。

默认模式仅用于配置目标尚不存在的首次生成；轮换使用新 kid 和新目录，按输出片段更新 active，
旧公钥配置到 previous，保留至旧 token 全部过期。运行时仍需填写必需的 issuer/audiences。
裸 `jwt` 仍不生成密钥。开发机可将二进制命令替换为 `go run .`。

标准格式可用 Go `crypto/x509` 或 OpenSSL 检查：

```bash
openssl pkey -in config/pem/dev-2026-10.private.pem -check -noout
openssl pkey -pubin -in config/pem/dev-2026-10.public.pem -pubcheck -noout
```

仅将旧 Ed25519 原始 PEM 重编码为 PKCS#8 / PKIX 时，应保留同一密钥材料与 kid；
重新生成则是密钥轮换。标准编码本身不改变 JWT 的签名算法（EdDSA）。

转换工具已编译进普通服务二进制，服务器无需 Go；实现位于
[reencode-jwt.go](../internal/pkg/jwtauth/reencode-jwt.go)。例如：

```bash
./bin/gin_api jwt reencode \
  --private-key /app/config/pem/current.private.pem \
  --public-key /app/config/pem/current.public.pem \
  --out-dir /app/config/pem-standard
```

三个参数都必填；相对路径相对命令执行目录，建议使用绝对路径。
输入可为旧 raw PEM 或标准 PEM，命令先校验私钥 seed 与密钥配对。
输出目录的父目录须存在，目标目录必须不存在（含文件或符号链接）；
输出为 `jwtpri.pem`（PKCS#8，0600）、`jwtpub.pem`（PKIX，0644），目录 0700。
原文件只读，现有目标路径被拒绝，失败返回非零退出码，不打印密钥内容。

转换后仅将 `jwt.active.private_key_path` / `public_key_path` 改为输出路径，
保留原 `kid`、`issuer`、`audiences`、`previous`、TTL 与兼容截止配置，重新加载服务。
若转换的是 previous 公钥对应的旧密钥，只更新对应 previous 的公钥路径，不把旧私钥配置回去。
命令不自动修改配置或重新签发 token；用一条仍有效的旧 token 请求原接口确认。
编码转换保持签名有效，过期、吊销及无 kid 接受截止时间等验签规则仍然生效。
开发机可将 `./bin/gin_api` 替换为 `go run .`。


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
