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
