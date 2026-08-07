# gin_api

`gin_api`（模块路径 `github.com/gtkit/gin-api`）是基于 Go 1.26、Gin、GORM、MySQL 和 Redis 的模块化 Web 服务脚手架，定位为**孵化新项目的脚手架母版**。

## 作为脚手架母版孵化新项目

用 `gonew` 复制并改名，再用 `project init` 收尾：

```bash
export GOPRIVATE=github.com/gtkit/*
gonew github.com/gtkit/gin-api@latest example.com/yourorg/your-project ./your-project
cd ./your-project
go run . project init --name your-project --dry-run   # 预览
go run . project init --name your-project             # 执行
```

完整流程、护栏说明与「移除 Laravel 兼容层」手动清单见 [`SCAFFOLD.md`](SCAFFOLD.md)。

## 快速开始

1. 创建本地配置：

   ```bash
   cp config/env.yml.example config/env/env.yml
   ```

2. 填写 MySQL、Redis、JWT 等本机配置。

   Redis 需 5.0 及以上：分布式限流的 Lua 脚本用 `TIME` 取时间基准，
   该调用要求 effect replication（Redis 5.0 起为默认）。

   其中 `auth.mode` **没有默认值，必须显式选择**：

   - `local` —— 本服务保管用户凭据，开箱提供注册/登录/刷新/登出端点
     （需要可用的 Redis：登出与刷新轮换依赖它做撤销）；
   - `external` —— 身份来自外部 IdP，不注册任何认证端点。

   两种模式的能力与接线方式见 `SCAFFOLD.md`。

3. 启动服务：

   ```bash
   make dev
   ```

4. 执行提交前检查：

   ```bash
   make check
   ```

运行配置固定从 `config/env/env.yml` 读取。该文件和 `config/pem/*.pem` 已被 Git 忽略，不得提交真实密钥。

## 架构

```text
cmd
  -> internal/bootstrap
  -> internal/runtime/module/<module>
  -> internal/module/<module>/application
  -> internal/repository/<table-or-aggregate>
```

- `cmd/`：Cobra 命令入口，只负责参数解析和启动。
- `internal/bootstrap/`：进程级资源、HTTP Server、路由和 worker 生命周期编排。
- `internal/runtime/module/`：模块装配，将 DB、配置和外部客户端转换成 application port 实现。
- `internal/module/`：业务模块，按 `application`、`dto`、`transport/http`、可选 `worker` 组织。
- `internal/repository/`：GORM/Redis 数据访问实现。
- `internal/models/`：持久化模型和共享枚举、值类型。
- `internal/platform/`：配置与基础设施连接管理。
- `internal/pkg/`：仅供当前项目复用的基础能力。
- `internal/shared/`：跨模块共享的轻量业务语义。
- `internal/scaffold/`：开发期模块生成器。

应用层不得依赖 Gin、GORM 或具体 repository。跨 repository 事务通过 application 定义的 `TxManager` port 编排，repository 统一经 `mdbCtx(ctx)` 获取事务句柄。

## 当前模块

当前业务模块只有 `user`：

- `GET /api/v1/user`：查询当前登录用户资料。
- `PUT /api/v1/user`：更新当前登录用户资料。
- `POST /api/v1/user/deactivate`：注销当前登录用户。

基础探针：

- `GET /health`：存活检查。
- `GET /ready`：MySQL、Redis 就绪检查。

### 通用 HTTP 行为

以下语义对全部路由生效，派生项目会继承：

- 未匹配的路径返回 404，未注册的方法返回 405 并携带 `Allow` 头；两者的响应体与业务接口共用同一 JSON 信封。
- 带尾斜杠的路径不做隐式重定向，按未匹配路由处置。
- `*gin.Context` 可直接当作 `context.Context` 传给下游，请求的取消与路由超时会一并传播。
- gin 运行模式由配置的 `env` 决定：`prod` 用 release、`test` 用 test、其余用 debug。

## 命令

```bash
go run . server
go run . doctor
go run . jwt
go run . module admin
go run . module staff --field nickname:string --field status:uint8
```

模块生成器说明见 `cmd/MODULE_GENERATOR.md`。

## 质量门禁

`make check` 依次执行：

1. 架构边界检查（`check-architecture`）。
2. 依赖清单整洁（`check-tidy`）、包级变量重绑定认领（`check-package-vars`）。
3. 镜像摘要固定（`check-image-digests`）、安全判定自检（`check-security-eval`）、发布接线（`check-release-wiring`）、应用身份零漂移（`check-appmeta`）、钩子安装语义（`check-hooks`）。
4. `golangci-lint run ./...`（`tool`）与 `go vet ./...`。
5. race 测试并生成聚合 coverage profile。
6. 校验全仓覆盖率不低于当前门禁（**以 `Makefile` 的 `COVERAGE_MIN` 为准**，本文不重复该数字——写死会与实际门禁分叉，而分叉时读者无从判断哪个是真的）。

各步骤的**权威清单是 `Makefile` 的 `check` 目标**：本文只做导读，改动门禁时以 Makefile 为准。

`make check-ci` 在 `make check` 基础上额外执行脚手架防腐冒烟 `check-generator`：把发布候选工作树
（已跟踪文件 + 新增未跟踪且非 ignored 文件，排除 ignored）导出到临时目录跑生成器，验证产物编译 /
过传感器 / goimports 干净。它另有两个自检入口：`--self-test` 证明门禁验的是工作树而非 HEAD，
`--verify-scope` 证明导出范围恰好覆盖四类边界行为。

**所有质量门禁都在本地执行**：`deployment.sh` 与 `.gitlab-ci.yml` 是外部冻结的专属部署方案，
CI 中不含质量 job。

### 装上钩子（克隆后第一件事）

```bash
make hooks
```

它把 `scripts/git-hooks/pre-push` 装到 `.git/hooks/`，推送前自动跑 `make check-ci`。
门禁存在不等于门禁被执行——CI 侧不承载质量 job，本地这一道就是代码进入共享历史前
唯一的自动检查点。

挂 pre-push 而不是 pre-commit：门禁是分钟级的，挂在每次提交上的结果必然是被
`--no-verify` 长期绕过。确需跳过用 `SKIP_GATE=1 git push`，跳过时会在错误输出留痕。

`make hooks` 幂等，改了钩子内容重跑一次即可；若 `.git/hooks/` 下已有**非本仓**的
同名钩子，它报错中止而不覆盖——那是你自己的自动化，本仓不替你决定怎么合并。

## 部署

发布物是在目标机通过 `go build` 生成的**版本化静态二进制**；CI 只负责调用部署脚本
（质量门禁在本地执行，见下），进程由 Supervisor 托管。
环境边界：服务器无 Docker、不跑本机 MySQL/Redis（应用连接远程实例，配置在 `env.yml`）；
数据库 schema 由外部维护，项目当前不含迁移 runtime（模块生成器产出的 `.up.sql`/`.down.sql`
草稿仅供 DBA 审阅后手工执行）。

### 生产发布

**部署脚本 `deployment.sh` 与 `.gitlab-ci.yml` 是本项目所属环境的专属方案，属外部冻结契约：
本仓不修改它们、不声明其编排，构建产物向它们的既有约定收敛。** 派生项目（经 gonew + `project init`）
不会继承这两个文件，需自备部署入口。

当前契约（只读取、不修改）：

| 契约点 | 当前值 |
|--------|--------|
| CI 触发 | `.gitlab-ci.yml` `only: main`，唯一 job `deploy_main` |
| CI 调用 | `sh deployment.sh master <RootPath>`（两个位置参数：分支、部署根路径） |
| 二进制路径 | `deployment.sh` 要求 `<RootPath>/bin/gin_api` 可执行，缺失即中止 |
| 进程管理 | supervisor 服务名 `gin_api` |

产物名来自 `project.mk` 的 `APP_NAME`：`make build` 输出 `bin/$(APP_NAME)`，当前为 `bin/gin_api`，
与上表一致。改 `APP_NAME` 需同步确认部署侧约定。

**CI 中不含任何质量门禁 job**，因此所有门禁都在本地执行（见「质量门禁」一节），提交前自行运行。

目标机一次性准备：

1. `RootPath` 下准备 `config/env/env.yml`（参照 `config/env.yml.example`）与 `config/pem/`。
2. supervisor program 指向构建产物：

   ```ini
   [program:gin_api]
   directory=/home/www/gin-api
   command=/home/www/gin-api/bin/gin_api server
   environment=APP_ROOT="/home/www/gin-api"
   user=www
   autorestart=true
   ```

   改完执行 `supervisorctl reread && supervisorctl update`。
3. 只改配置不改代码时不必跑部署脚本：编辑 `config/env/env.yml` 后
   `supervisorctl restart gin_api` 即可（配置在进程启动时一次性装配，不重启不生效）。

### 本地 Docker 镜像打包（可选）

```bash
make image    # 构建 $(APP_NAME):<当前commit短sha>，仅本地使用；服务器不运行 Docker
```

镜像内不含 `config/env/env.yml`、PEM 私钥与 `.git`（`.dockerignore` 保证）；运行配置挂载注入。

产物名由 `--build-arg APP_NAME` 决定（`make image` 从 `project.mk` 取），构建阶段显式断言其非空；
**容器内路径固定为 `/app/bin/server`，与 `APP_NAME` 无关**——`ENTRYPOINT` 必须是 exec form
（shell form 会让 shell 成为 PID 1，破坏信号转发与优雅关停），而 exec form 不做变量展开。
宿主机侧的 `bin/$(APP_NAME)` 由冻结的部署脚本负责，与容器内路径互不牵连。

### 数据库集成测试（本地手动）

`make test-integration` 用真实 MySQL 验证 repository 与 schema 一致性（配置取自
`config/env/env.yml`，事务内强制回滚、零持久化；无 DB 时自动 SKIP）。
当前不设 CI 强制 job；将来需要时设 `REQUIRE_DB_INTEGRATION=1` 可把 SKIP 变为失败。

## 配置与性能

- 配置说明：`config/README.md`
- pprof 默认关闭；启用后使用独立回环地址端点。
- 模块生成器生成可审阅的 `.up.sql` / `.down.sql` 草稿。执行走内置的 `migrate` CLI
  （`make migrate-up` / `migrate-status`，连库验证见 `make migrate-verify`），
  上线前须经 DBA 审阅。
