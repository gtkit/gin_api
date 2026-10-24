# gin_api

`gin_api`（模块路径 `github.com/gtkit/gin-api`）是基于 Go 1.27、Gin、GORM、MySQL 和 Redis 的模块化 Web 服务脚手架，定位为**孵化新项目的脚手架母版**。

## 作为脚手架母版孵化新项目

用 `gonew` 复制并改名，再用 `project init` 收尾：

```bash
export GOPRIVATE='github.com/gtkit/*'
gonew github.com/gtkit/gin-api@latest example.com/yourorg/your-project ./your-project
cd ./your-project
make project-init NAME=your_project ARGS=--dry-run   # 预览
make project-init NAME=your_project                  # 执行
```

私有模块的临时/持久配置、访问权限与内部代理说明见 [前提](SCAFFOLD.md#前提)；完整流程、护栏说明、CORS 请求头清理及可选依赖精简见 [`SCAFFOLD.md`](SCAFFOLD.md)。

派生初始化精确移除母版部署入口及 `REVIEW.md`、`REVIEW-my.md`，保留 `pprof/` 和其他文档。
`make build` 默认生成 linux/amd64 二进制并启用 `-trimpath`；保留 `Linux`、`BUILD_FLAGS` 覆写。
UPX 已安装时执行压缩，未安装时保留未压缩产物；编译或压缩失败仍返回失败。

## 快速开始

1. 创建本地配置：

   ```bash
   cp config/env.yml.example config/env/env.yml
   ```

2. 填写 MySQL、JWT 等本机配置。Redis 在本地认证、Redis 限流、Redis 响应缓存或任务队列启用时必须配置；无相关能力时地址与库号可留空。

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

核心配置在启动前校验：`database.dbtype` 只接受精确的 `mysql`；`log.level` 与 `log.path` 必填且拒绝纯空白，
日志级别去除首尾空白后只接受 `debug/info/warn/error/dpanic/panic/fatal`；`database.maxlifeseconds` 必须非负，
省略/0 仍使用默认 1800 秒。连接池和 Redis 的既有默认及消费方规则保持不变，详见 [配置说明](config/README.md#核心字段校验)。

Redis认证、限流与响应缓存统一使用 `redis.prefix`；省略或空串时原样回落 `application.name`，不补分隔符。存量升级须保持旧实际namespace：旧版忽略的非空prefix若不同于旧应用名，须先改为原namespace再升级；直接改名会使撤销/刷新状态失联。本次不搬迁key、不扩展DB/Redis集群。

Redis 各能力的启动、故障和恢复语义见 [Redis 故障与恢复](SCAFFOLD.md#redis-故障与恢复)。
仅可降级能力启用时，首次不可达会在同一进程内重试恢复；已挂载的缓存与共享限流后端随之恢复。
本地认证、fail-closed Redis 限流和任务队列仍要求 Redis 在启动时可用。

## 架构

```text
cmd
  -> internal/bootstrap
  -> internal/runtime/module/<module>
  -> internal/module/<module>/application
  -> internal/repository/<table-or-aggregate>
```

- `cmd/`：Cobra 命令入口，只负责参数解析和启动。
- `internal/bootstrap/`：`providers.go` 负责进程级资源与启动阶段编排，`modules.go` 集中装配模块服务、鉴权、路由和后台任务。
- `internal/runtime/module/`：模块装配，将 DB、配置和外部客户端转换成 application port 实现。
- `internal/module/`：业务模块，按 `application`、`dto`、`transport/http`、可选 `worker` 组织。
- `internal/repository/`：GORM/Redis 数据访问实现。
- `internal/models/`：持久化模型和共享枚举、值类型。
- `internal/platform/`：配置与基础设施连接管理。
- `internal/pkg/`：仅供当前项目复用的基础能力。
- `internal/shared/`：跨模块共享的轻量业务语义。

应用层不得依赖 Gin、GORM 或具体 repository。跨 repository 事务通过 application 定义的 `TxManager` port 编排，repository 统一经 `mdbCtx(ctx)` 获取事务句柄。

## 工程规范

分层约束、API 约定、数据访问、Worker、缓存、出站调用、可观测性、测试与门禁的完整规范
在 [`docs/`](docs/)，索引见 [`docs/README.md`](docs/README.md)。

每条硬约束尽量对应一条可执行判据（`make check`）：判据回答「违反了没有」，
文档回答「为什么这么定」。新增代码前先读 [`docs/architecture.md`](docs/architecture.md)，
提交前走一遍 [`docs/review-checklist.md`](docs/review-checklist.md)。

## 当前模块

`auth`（仅 `auth.mode: local` 时注册；`external` 模式下本服务只验签与授权，不注册任何认证端点）：

- `POST /api/v1/auth/register`：注册。
- `POST /api/v1/auth/login`：登录，签发访问令牌与刷新令牌。
- `POST /api/v1/auth/refresh`：刷新令牌轮换（一次性使用，重用即撤销整个令牌族）。
- `POST /api/v1/auth/logout`：登出，访问令牌 jti 进撤销表。需认证。

四个端点都禁止任何缓存层保存响应（携带令牌），并挂专用的严格限流：
`register`/`login` 用 `auth.credential`（按来源，全站唯一接受口令猜测的入口），
`refresh`/`logout` 用 `auth.token`。

当前不提供 AI 对话与会话管理模块，`/api/v1/ai/*` 接口不注册。
历史 SQL 和现有数据保留，旧迁移在新库仍可能创建 AI 表，详见 [数据库说明](database/README.md#ai-模块移除后的历史迁移)。

基础探针：

- `GET /health`：存活检查。
- `GET /ready`：MySQL、Redis 就绪检查。响应体按依赖分列状态与失败原因，
  例如 `{"database":{"status":"unavailable","error":"dial tcp ...: connection refused"}}`。
  **错误详情会带出内网地址与数据库账号名，该端点只应对编排器与内网可达，不得暴露到公网。**

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
make project-init NAME=my_service ARGS="--dry-run"
```

`project init` 是开发期工具，由 `scaffold` 构建约束门控，不在服务二进制里。
`make project-init` 已封装该标签，裸命令形式为
`go run -tags scaffold . project init --name <name>`。
业务模块按 [架构约束](docs/architecture.md) 实现，并在 `internal/bootstrap/modules.go` 显式接线。

Make 命令统一从项目根目录执行，也可使用 `make -C <项目目录> <目标>`。不指定目标时仍执行 `dev`。根 `Makefile` 通过固定顺序的 `include` 加载以下分类文件；各文件共享同一套变量和目标，不作为独立入口。

| 文件 | 职责 |
|------|------|
| `Makefile` | 公共变量、默认与条件入口、`check` / `check-ci` 聚合目标、端口兼容规则 |
| `make/build.mk` | 编译、镜像构建、受管产物清理 |
| `make/dev.mk` | 开发依赖、应用 Compose 操作、钩子安装 |
| `make/quality.mk` | lint、vet、modern、测试、覆盖率、架构及安全检查入口 |
| `make/scaffold.mk` | 项目初始化、应用身份生成和检查 |
| `make/migration.mk` | 迁移、环境与名称占位、迁移文件及数据库验证 |
| `make/release.mk` | 发布门禁编排、tag 入口、发布接线验证 |

现有命令、命令行及环境变量覆盖方式保持不变；公共变量和覆盖率门槛仍以根 `Makefile` 为准。子文件应随根文件一并保留，缺少任一必需文件时 Make 会报错。

## 质量门禁

`make check` 依次执行：

1. 架构边界检查（`check-architecture`）。
2. 依赖清单整洁（`check-tidy`）、包级变量重绑定认领（`check-package-vars`）。
3. 镜像摘要固定（`check-image-digests`）、安全判定自检（`check-security-eval`）、发布接线（`check-release-wiring`）、应用身份零漂移（`check-appmeta`）、钩子安装语义（`check-hooks`）。
4. `golangci-lint run ./...`（`tool`）与 `go vet ./...`。
5. race 测试并生成聚合 coverage profile。
6. 校验全仓覆盖率不低于当前门禁（**以 `Makefile` 的 `COVERAGE_MIN` 为准**，本文不重复该数字——写死会与实际门禁分叉，而分叉时读者无从判断哪个是真的）。

各步骤的**权威清单是 `Makefile` 的 `check` 目标**：本文只做导读，改动门禁时以 Makefile 为准。

`make check-ci` 执行与 `make check` 相同的质量门禁。

门禁有两条自动执行点，覆盖前提不同、互不替代：

| 执行点 | 何时反馈 | 覆盖前提 |
|--------|---------|---------|
| 本地 pre-push 钩子 | 推送**前**拦截 | 需先执行一次 `make hooks` |
| GitHub Actions（`.github/workflows/check.yml`） | 推送**后**反馈 | 无前提，克隆即生效 |

两者执行的是同一条 `make check-ci`，不存在只在一侧生效的检查项。

`deployment.sh` 与 `.gitlab-ci.yml` 是外部冻结的专属**部署**方案，其中不含质量 job——
质量门禁走上面两条。

### 装上钩子（克隆后第一件事）

```bash
make hooks
```

它把 `scripts/git-hooks/pre-push` 装到 `.git/hooks/`，推送前自动跑 `make check-ci`。
本地钩子在推送前检查，GitHub Actions 在推送和 PR 时执行同一门禁；GitLab 专属部署流水线保持独立。

挂 pre-push 而不是 pre-commit：门禁是分钟级的，挂在每次提交上的结果必然是被
`--no-verify` 长期绕过。确需跳过用 `SKIP_GATE=1 git push`，跳过时会在错误输出留痕。

`make hooks` 幂等，改了钩子内容重跑一次即可；若 `.git/hooks/` 下已有**非本仓**的
同名钩子，它报错中止而不覆盖——那是你自己的自动化，本仓不替你决定怎么合并。

## 部署

发布物是在目标机通过 `go build` 生成的**版本化静态二进制**；GitLab CI 只负责调用部署脚本
（质量门禁走「质量门禁」一节的两条执行点，不在这条部署流水线上），进程由 Supervisor 托管。
环境边界：服务器无 Docker、不跑本机 MySQL/Redis（应用连接远程实例，配置在 `env.yml`）；
数据库 schema 由外部维护；项目提供 `migrate` 命令的最小运行时（配置、日志、数据库），
迁移 SQL 需包含 Up/Down 区段，经 DBA 审阅后再显式执行。

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

**这条部署流水线中不含任何质量门禁 job**——它是外部冻结的部署契约，只负责发布。
质量门禁由 pre-push 钩子与 GitHub Actions 承担（见「质量门禁」一节）。

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
**容器内路径固定为 `/app/bin/server`，与 `APP_NAME` 无关**。镜像默认`ENTRYPOINT`为exec form，
应用profile的入口先检查UID再exec原二进制，检查后的shell不常驻PID 1。
宿主机侧的 `bin/$(APP_NAME)` 由冻结的部署脚本负责，与容器内路径互不牵连。

### 本地 Compose 应用入口

默认`make dev-up`仍只启动MySQL/Redis。显式`app` profile提供应用服务；服务器部署方式保持。
先检查本机已有镜像和容器：同类依赖已启动时优先复用，不重复创建MySQL/Redis。
复制`compose.env.example`准备本地私密env文件，并按模板填写应用挂载、UID/GID和本地连接。
隔离配置中的JWT路径应指向`/app/config/pem`内的临时密钥，私钥0600、公钥0644；
`redis.prefix`必须独占，测试库必须先应用本仓既有迁移。应用启动不自动执行DDL，`/ready`不替代迁移版本核对。

```bash
make check-compose-app # 合成环境解析和shell入口fixture；不创建容器，不读取真实.env
make dev-app-up COMPOSE_ENV_FILE=/absolute/path/to/local-compose.env
# 已有本机依赖：按模板设置DEV_APP_DB_HOST/DEV_APP_REDIS_ADDR，再用此入口
make dev-app-up-existing COMPOSE_ENV_FILE=/absolute/path/to/local-compose.env
make dev-app-down COMPOSE_ENV_FILE=/absolute/path/to/local-compose.env
```

容器内固定APP_ROOT=/app、HTTP 0.0.0.0:8008，宿主只映射127.0.0.1的DEV_APP_PORT。
配置和密钥只读挂载，日志目录需预先存在且可由DEV_APP_UID写入；缺少挂载不自动创建。
默认身份1000:1000，可填宿主`id -u`/`id -g`，入口拒绝UID 0，不能靠放宽私钥权限解决身份不匹配。
Docker Desktop复用已有服务可用host.docker.internal；其他环境须提供已确认可达的本机地址。
DEV_APP_DB_PASSWORD/DEV_APP_REDIS_PASSWORD未设置时继承依赖密码，显式空值则保留为空，仅用于确实无密码的本地测试服务。

`dev-app-up`构建并启动应用及其健康依赖，`dev-app-up-existing`以--no-deps只启动应用；
执行前须按AGENTS.md确认本地镜像/容器与授权，不隐式拉取缺失镜像。APP_NAME仍由project.mk注入，
BUILD_COMMIT/BUILD_TIME可固定；COMPOSE_PROJECT默认APP_NAME，验收可指定独占项目名。
`dev-app-down`只停止/移除app，保留依赖及数据卷；无其他服务的独占网络需确认无使用者后单独清理。
容器以/ready作健康检查，SIGTERM宽限3分钟；它是本地默认，改变服务关停预算时须同步核对。
解析和入口fixture通过不等于镜像、真实API、依赖故障或信号验收通过。

### 数据库集成测试（本地手动）

`make test-integration`使用项目根之外的显式隔离APP_ROOT，先验证配置元数据再连接真实依赖；
包含命令装配套件和仓储事务回滚。Make强制PLUGIN_INTEGRATION=1，发布入口还设置
REQUIRE_DB_INTEGRATION=1使缺依赖失败；日常check仍不依赖数据库。准备方法见`docs/testing.md`。

## 配置与性能

- 配置说明：`config/README.md`
- pprof 默认关闭；启用后使用独立回环地址端点。
- 迁移 SQL 使用含 Up/Down 区段的 goose 单文件格式。执行走内置的 `migrate` CLI
  （`make migrate-up` / `migrate-status`，连库验证见 `make migrate-verify`），
  上线前须经 DBA 审阅。

常用入口：`make run`（dev别名）、`make lint`（检查锁定工具版本并执行lint）、`make test`（scaffold标签、race完整测试）、`make clean`（只删除默认bin/<APP_NAME>与.cache/coverage.out）。`make migrate-status test`等迁移命令中的test仍是环境占位，不会启动测试或服务；自定义输出不由clean管理，越界、symlink或非普通文件会被拒绝。入口fixture自检已接入make check，实际Go回归另行执行。

`go run . --version`或二进制`--version`无需配置、数据库或Redis，输出version/commit/build_time。版本常量仍只在version.go；普通go build缺少注入时commit/time显示unknown。make build/image注入当前HEAD及UTC构建时间，CI可显式提供BUILD_COMMIT/BUILD_TIME固定输入；Docker直接构建的同名ARG默认unknown。未提交工作区内容不能仅由HEAD证明，交付应同时保留diff和构建证据。

JWT生成公钥0644可通过doctor；Unix上私钥不得开放group/other访问，公钥及previous公钥不得group/other可写。Windows权限位不代表ACL，未声称ACL已验。生成器只清理本次独占创建的文件，输出失败仍返回错误，已完成且可用的密钥不会因打印失败被删除。
