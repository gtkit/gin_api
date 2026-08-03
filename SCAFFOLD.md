# 脚手架母版使用说明

本仓库（`github.com/gtkit/gin-api`）是**孵化新项目的脚手架母版**。用它起一个新项目分两步：`gonew` 复制并改名 → `project init` 收尾。

## 前提

母版是私有托管，`gonew` 需要能解析它。使用方在环境里设置：

```bash
export GOPRIVATE=github.com/gtkit/*
# 若走内部 GOPROXY，另按团队约定配置 GOPROXY / GONOSUMCHECK
```

安装 gonew（Go 官方模板工具）：

```bash
go install golang.org/x/tools/cmd/gonew@latest
```

## Step A：gonew 复制并重写模块路径

```bash
gonew github.com/gtkit/gin-api@latest example.com/yourorg/your-project ./your-project
cd ./your-project
```

`gonew` 会把母版内所有 `github.com/gtkit/gin-api/...` 的 import 重写为你的新模块路径。此时项目已是一个**可编译的真实项目**（未做身份/清理收尾）。

## Step B：project init 实例化收尾

```bash
# 预览将发生的改动（不落盘）
go run . project init --name your_project --dry-run

# 实际执行
go run . project init --name your_project
```

`project init` 会：

- 把 `--name` 写入 `project.mk`（应用身份的**唯一来源**：`APP_NAME` / `APP_DISPLAY_NAME`），
  再执行 `make gen-appmeta` 生成 `internal/appmeta`，并校验生成物与来源零差异——
  不直接正则改写生成产物，否则会形成第二个身份来源；
- 删除母版所属环境的专属部署入口（`.gitlab-ci.yml` 与 `deployment.sh`）。它们绑定母版环境的
  进程管理服务名、部署根路径与分支，不存在参数化后通用的形态，保留等于分发一条必然断裂的部署链——
  **派生项目需自备部署入口**；
- 以指定分支（`--branch`，默认 `main`）初始化 git 并建立首个提交；
- 收尾自检 `go build ./...` + `make check`。

**明确不做**：不修改 `Makefile` 的 `COVERAGE_MIN`。派生代码主体与母版相同，没有覆盖率下降的事实依据，
把门禁重置到更低的"起步地板"既无依据又违反覆盖率棘轮只升不降。派生项目直接继承母版已验证的门槛。

**参数**

| flag | 说明 |
|------|------|
| `--name` | 必填。须匹配 `^[a-z][a-z0-9_]{0,62}$`，在**任何文件被修改之前**校验 |
| `--branch` | 初始分支名，默认 `main`；由 `git check-ref-format --branch` 判定，不读 `init.defaultBranch` |
| `--dry-run` | 只预览不落盘、不动 git |
| `--force-reinit-git` | 允许在已有 git 仓库上执行，**会永久丢弃现有 git 历史** |
| `--no-commit` | 只初始化并暂存，不产生提交；该模式不要求 git 提交身份 |

**护栏**

- 项目名与分支名先于任何文件改动被校验（项目名会同时进入 Go 字符串字面量与 Make KV，
  未校验即为字面量破坏与 Make 表达式注入面）；
- **已有 git 仓库默认拒绝执行**——它会丢弃历史，而普通输入（gonew 产物）本来就没有 `.git`；
  确需重建时显式传 `--force-reinit-git`；
- `.git` 为普通文件（linked worktree）、符号链接或其他特殊类型时，**即使带 flag 也拒绝**；
- 工作区非纯净时拒绝执行；在母版自身（模块路径仍为 `github.com/gtkit/gin-api`）上拒绝执行；
- 执行前在项目根**之外**建立受影响文件与旧 git 的快照，任一阶段失败都逐字回滚
  （含权限位；成功重建时旧 `.git/info/exclude` 会在首个 `git add` 之前写回，
  保证本地忽略规则对首个提交依然生效）；
- 可重复执行（幂等）。

## 之后

- 用生成器加业务模块：`go run . module <name> --field title:string ...`（自动接线进分阶段 `providers.go`，见 `cmd/MODULE_GENERATOR.md`）。
- `user` 模块保留作为认证/参考起点（它是 `sharedauth.UserVerifier` 提供者），按新项目需要改造或替换。
- 提交前跑 `make check`；改动触及生成器/模板/`providers.go` 时再跑 `make check-ci`（额外含脚手架防腐冒烟 `check-generator`）。质量门禁全部在本地执行——部署脚本与 CI 配置是外部冻结方案，其中不含质量 job。

## 移除 Laravel 兼容层（手动清单）

母版内置了 Laravel（tymon/jwt-auth）存量 token 的**单向兼容验证**，默认经 `cfg.JWT.LegacyLaravel.Enabled` 关闭。绿地新项目通常不需要它。`project init` **不会自动删除**（涉及多文件源码编辑，自动手术脆弱），请按下面清单手动移除：

1. 删除文件：
   - `internal/pkg/jwtauth/legacy.go`
   - `internal/pkg/jwtauth/legacy_test.go`
   - `internal/middleware/auth_legacy_test.go`
2. 简化 `internal/bootstrap/providers.go` 的 `newTokenParser`：去掉 legacy 分支，直接 `return issuer, nil`（或让 `buildMiddlewares` 直接用 `runtime.TokenIssuer()`，并删除 `newTokenParser`）。
3. 删除 `internal/platform/config/types.go` 中的 `LegacyLaravelJWTConfig` 类型、`JWT.LegacyLaravel` 字段及其校验分支。
4. 清理相关测试用例：`internal/bootstrap/providers_token_test.go`、`internal/bootstrap/providers_phases_test.go` 中的 legacy 开启态用例，`internal/platform/config/load_test.go` 的 `TestValidateLegacyLaravelJWTConfig`。
5. 跑 `make check` 确认全绿。

> 注意：代码里 `legacylog` / `legacydao` / `initLegacyRuntime` / `NewLegacyWorker` 只是现有基础设施（日志 / DAO / worker）的命名，**与 Laravel 无关，不要动**。

## 预置能力清单

逐项标注状态，避免"以为它已经在工作"或"以为它是示例可以删"两类误解。

### 【默认开启】开箱即生效，无需接线

| 能力 | 位置 | 说明 |
|------|------|------|
| 分层架构传感器 | `scripts/check-architecture.sh` | `make check` 的第一步；含路由注册唯一入口、客户端 IP 唯一 accessor 等规则 |
| 包级变量重绑定检查 | `scripts/check-package-vars.go` | 每处重绑定必须删除或在 `security`-风格的登记表中认领 |
| 应用身份单一来源 | `project.mk` + `make gen-appmeta` | `check-appmeta` 保证生成物零漂移 |
| 路由契约 | `internal/router/routespec` | 访问/超时策略零值即拒绝；双向契约测试防旁路路由 |
| 可信代理边界 | `proxy.trusted_proxies` | **默认不信任任何代理**；部署在反代之后必须配置 |
| 认证与授权分离 | `internal/middleware` + `internal/shared/auth` | 认证只判身份；授权依赖故障映射 503 而非 403 |
| 请求体上限 / 413 契约 | `internal/middleware/maxbody.go` | 声明长度与 chunked 两条路径都覆盖 |
| 有界 body 日志 | `internal/middleware/logger.go` | 默认关闭；开启时前缀有界、脱敏 |
| 配置严格解码 | `internal/platform/config` | 未知键即启动失败 |
| 数据库连接校验 | `internal/platform/data.OpenDB` | 实测 vendor/版本、会话时区/字符集/sql_mode |
| 迁移体系 | `database/` + `make migrate-*` | 含基线迁移与 `make migrate-verify` |

### 【注册位 — 需要接线才生效】

| 能力 | 如何启用 |
|------|---------|
| 后台任务 | 在 `providers.go` 的 `scaffold:module-workers` 标记前追加一行 `Worker` 构造 |
| 具名授权实现 | 在 `buildAuthorizers` 的 `scaffold:module-authorizers` 标记前注册；ID 与 `RouteSpec.AccessPolicy.AuthorizerID` 对齐 |
| `role.admin` 授权 | **当前是恒拒绝占位**（无权威角色源），所有请求 403。接入角色表/IAM 后替换该 adapter，RouteSpec 无需改动 |
| 迁移专用凭据 | 配 `database.migrate_username`；不配则回退运行凭据并告警 |
| Laravel 存量 token 兼容 | 配 `jwt.legacy_laravel.enabled`；终态应删除，见下节清单 |
| pprof | 配 `pprof.enabled`；生产强制回环绑定 |

### 【示例 — 可按需删除】

| 内容 | 说明 |
|------|------|
| `user` 模块 | 认证/参考起点，是 `sharedauth.UserVerifier` 的提供者。删除前需替换该提供者 |
| `entitlement.vip` authorizer | 会员资格判定示例；无 VIP 概念的项目可连同 `VIPVerifier` 一起删 |
| 三张表的基线迁移 | 与 `user` 模块配套；替换业务模型时一并重做 |

### 本地开发依赖（可选）

```bash
cp compose.env.example .env    # 填本机密码；.env 不入库
make dev-up                    # 拉起 MySQL/Redis 并等待健康
make dev-down                  # 停止，保留数据卷
make dev-reset                 # 删除数据卷（需二次确认）
```

**这套编排只用于本地开发与测试**，不是生产部署方案，也不替代已冻结的 `deployment.sh` / `.gitlab-ci.yml`。
端口只绑 `127.0.0.1`——开发库口令通常很弱，绑 `0.0.0.0` 会把它暴露到局域网。

### 供应链门禁

```bash
make security-source     # 日常：发布候选快照的漏洞与密钥扫描
make security-release    # 发版前：追加全历史密钥扫描，禁止跳过漏洞扫描
```

刻意**不并入 `make check`**：安全扫描依赖网络与 Docker，并入会让 `make check` 在离线环境失败，
最终逼人绕过整个门禁。`make tag` 会强制执行 `security-release`。
