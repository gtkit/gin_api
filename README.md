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

1. 架构边界检查。
2. OpenAPI 文档结构和真实 Gin 路由一致性检查。
3. `golangci-lint run ./...`。
4. `go vet ./...`。
5. race 测试并生成聚合 coverage profile。
6. 校验全仓覆盖率不低于当前门禁 62.0%（以 `Makefile` 的 `COVERAGE_MIN` 为准）。

`make check-ci` 在 `make check` 基础上额外执行脚手架防腐冒烟 `check-generator`：把发布候选工作树
（已跟踪文件 + 新增未跟踪且非 ignored 文件，排除 ignored）导出到临时目录跑生成器，验证产物编译 /
过传感器 / goimports 干净。它另有两个自检入口：`--self-test` 证明门禁验的是工作树而非 HEAD，
`--verify-scope` 证明导出范围恰好覆盖四类边界行为。

**所有质量门禁都在本地执行**：`deployment.sh` 与 `.gitlab-ci.yml` 是外部冻结的专属部署方案，
CI 中不含质量 job，提交前由开发者自行运行上述入口。

## 部署

发布物是在目标机通过 `go build` 生成的**版本化静态二进制**；CI 负责质量门禁和调用发布脚本，
进程由 Supervisor 托管。
环境边界：服务器无 Docker、不跑本机 MySQL/Redis（应用连接远程实例，配置在 `env.yml`）；
数据库 schema 由外部维护，项目当前不含迁移 runtime（模块生成器产出的 `.up.sql`/`.down.sql`
草稿仅供 DBA 审阅后手工执行）。

### 生产发布

GitLab CI 流水线示例（分支、Runner、路径均应在创建真实项目后重新配置）：推送
`develop`/`master` → `quality`（`make check`）→ `deploy_*`（目标机 Runner 上执行）。
SSH 私钥与 known_hosts 仅注入部署 job，质量 job 不接触部署凭据；两个示例部署共用目录，
因此通过同一个 `resource_group` 串行执行。

```bash
bash deployment.sh -b <分支> -e <环境标识> -p <RootPath>
```

`deployment.sh` 流程：git 拉取分支（CI 环境精确到触发流水线的 commit）→ 校验配置就位 →
目标机执行 `GOWORK=off CGO_ENABLED=0 go build` 生成临时文件 → 新二进制执行 `doctor` →
落盘为 `RootPath/releases/app_client-<sha>` → 同目录临时软链原子替换
`RootPath/bin/app_client` → `supervisorctl restart` 并确认稳定处于 RUNNING → 清理旧版本。
Supervisor 重启、状态检查或已启用的 HTTP 健康检查失败时，脚本会恢复旧软链并重启旧版本；
首次发布失败且没有旧版本时会移除活动软链并停止服务。

**回滚**（免拉代码免构建，直接切回历史版本）：

```bash
bash deployment.sh -p <RootPath> -r <短sha>    # releases/ 中已有的版本
```

目标机一次性准备：

1. Go 工具链、Git、Supervisor；`RootPath` 下准备 `config/env/env.yml`（参照 example）与 `config/pem/`。
2. supervisor program 指向软链（发布不改 supervisor 配置）：

   ```ini
   [program:gin_api]
   directory=/home/www/gin-api
   command=/home/www/gin-api/bin/app_client server
   environment=APP_ROOT="/home/www/gin-api"
   user=www
   autorestart=true
   ```

   改完执行 `supervisorctl update`。
3. 在 GitLab CI 中配置 `SSH_PRIVATE_KEY1` 与经过核验的 `SSH_KNOWN_HOSTS`。脚手架不在流水线内
   动态执行 `ssh-keyscan`，避免把网络返回值直接作为信任根。
4. HTTP 健康检查默认关闭，因为脚手架不能假定端口。实际项目可设置
   `HEALTHCHECK_URL=http://127.0.0.1:<端口>/health`；启用后目标机还需安装 `curl`。
   重试次数、间隔与单次超时可通过 `HEALTHCHECK_ATTEMPTS`、`HEALTHCHECK_INTERVAL`、
   `HEALTHCHECK_TIMEOUT` 覆盖。

### 本地 Docker 镜像打包（可选）

```bash
make image    # 构建 gin_api:<当前commit短sha>，仅本地使用；服务器不运行 Docker
```

镜像内不含 `config/env/env.yml`、PEM 私钥与 `.git`（`.dockerignore` 保证）；运行配置挂载注入。

### 数据库集成测试（本地手动）

`make test-integration` 用真实 MySQL 验证 repository 与 schema 一致性（配置取自
`config/env/env.yml`，事务内强制回滚、零持久化；无 DB 时自动 SKIP）。
当前不设 CI 强制 job；将来需要时设 `REQUIRE_DB_INTEGRATION=1` 可把 SKIP 变为失败。

## 配置与性能

- 配置说明：`config/README.md`
- pprof 默认关闭；启用后使用独立回环地址端点。
- 模块生成器生成可审阅的 `.up.sql` / `.down.sql` 草稿，仅供 DBA 审阅后手工执行（项目不含迁移 runtime）。
