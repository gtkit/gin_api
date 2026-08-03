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

## JWT 密钥轮换操作手册

母版的 JWT 是**带 kid 的多密钥**体系：轮换不再让存量令牌批量失效。

**生成一组新密钥**（kid 必须是新的——同 kid 会被无条件拒绝）：

```bash
go run . jwt generate --kid k2026b --out-dir config/pem
```

**轮换步骤**：

1. 生成新 kid；
2. 把**当前** active 的公钥挪进 `jwt.previous`（只放公钥，不放私钥）；
3. 把新 kid 设为 `jwt.active`；
4. 重启进程。

重叠窗口（previous 保留多久）必须**覆盖旧令牌的剩余 TTL**，否则轮换仍会造成批量失效。默认 TTL 是 30 天，所以旧公钥至少保留 30 天。活跃用户会在刷新时自然迁移到新 kid。

**几条硬约束**：

- `previous` 最多 8 个：无 kid 的存量令牌只能逐个尝试，没有上限就是无界成本；
- 密钥集在**进程生命周期内不可变**，不做热重载——轮换靠重启；
- `jwt.unkeyed_token_accept_until` 是无 kid 存量令牌的**绝对** RFC3339 截止时刻，默认空即不接受。取值应为「切换时刻 + 旧令牌最大 TTL」，**迁移完成后删除该配置**；
- 它与 `jwt.laravel_accept_until` 是两个独立维度，分别删除。

## 限流的两条使用约束

**1. `store: local` 的多副本语义**：有效限额 = 配置值 × 副本数。它适合"高位防护上限"（挡住明显异常的调用量），不适合精确业务限额——"每账号每分钟 5 次登录"在 3 个副本上实际是 15 次，而那正是攻击者需要的余量。精确限额必须用 `store: redis`。

**2. `failure_mode: local_fallback` 只保证算法模型一致，不保证状态连续**：切换时 Redis 里的令牌计数不会迁移，本地影子桶从满桶起算，瞬时会多放行至多一个 Burst。不要据此做容量规划。

另：`ratelimit.key_hmac_secret` 的轮换语义是**桶全部重算 = 一次性限额重置**（不做双密钥灰度）。换密钥意味着所有主体重新拿到一个满桶，选在低峰期操作。

## 引入 WebSocket 端点时的强制约束

母版**不含任何 WebSocket 端点**，因此也没有连接注册表——按"无消费者不建机制"的原则，现在建它只是一个没人用的抽象。

但引入 WS 端点的**同一个 change** 必须同时补上两件事，否则关停会出现"假成功"：

1. **连接注册表**：`Hijack` 之后的连接**不受 `http.Server.Shutdown` 管理**——Shutdown 只等非 hijacked 的请求收尾。没有注册表的话，Shutdown 会正常返回，而 WS 连接还全都活着。
2. **draining 阶段独立 drain**：向每个连接发 close frame，超时后强制 `Close()`。它排在分相关停的哪一步取决于该端点是生产者还是消费者语义，需在该 change 里明确。

超时侧同样要注意：WS 走 `TimeoutStreaming`（可续期的写空闲超时），**不能**用 `TimeoutClientVisible`——那个 wrapper 会缓存响应且不透明，Hijack 拿不到底层连接。

## 移除 Laravel 兼容层（手动清单）

母版内置了 Laravel（tymon/jwt-auth）存量 token 的**单向兼容验证**，默认经 `cfg.JWT.Laravel.Enabled` 关闭。绿地新项目通常不需要它。`project init` **不会自动删除**（涉及多文件源码编辑，自动手术脆弱），请按下面清单手动移除：

1. 删除文件：
   - `internal/pkg/jwtauth/laravel.go`
   - `internal/pkg/jwtauth/laravel_test.go`
   - `internal/middleware/auth_laravel_test.go`
2. 简化 `internal/bootstrap/providers.go` 的 `newTokenParser`：去掉 Laravel 回退分支，直接 `return issuer, nil`（或让 `buildMiddlewares` 直接用 `runtime.TokenIssuer()`，并删除 `newTokenParser`）。
3. 删除 `internal/platform/config/types.go` 中的 `LaravelJWTConfig` 类型、`JWT.Laravel` 字段及其校验分支。
4. 清理相关测试用例：`internal/bootstrap/providers_token_test.go`、`internal/bootstrap/providers_phases_test.go` 中的 Laravel 回退开启态用例，`internal/platform/config/load_test.go` 的 `TestValidateLaravelJWTConfig`。
5. 跑 `make check` 确认全绿。

> 注意：代码里 `applog` / `data` / `initProcessRuntime` 只是现有基础设施（日志 / DAO / worker）的命名，**与 Laravel 无关，不要动**。

## 预置能力清单

逐项标注状态，避免"以为它已经在工作"或"以为它是示例可以删"两类误解。

### 【默认开启】开箱即生效，无需接线

| 能力 | 位置 | 说明 |
|------|------|------|
| 分层架构传感器 | `scripts/check-architecture.sh` | `make check` 的第一步；含路由注册唯一入口、客户端 IP 唯一 accessor 等规则 |
| 包级变量重绑定检查 | `scripts/check-package-vars.go` | 每处重绑定必须删除或在 `security`-风格的登记表中认领 |
| 应用身份单一来源 | `project.mk` + `make gen-appmeta` | `check-appmeta` 保证生成物零漂移 |
| 路由契约 | `internal/router/routespec` | 访问/超时策略零值即拒绝；双向契约测试防旁路路由 |
| 路由级超时 | `internal/middleware/routetimeout.go` | 三种 mode；两级预算（ctx + socket 写 deadline）；服务器层 `WriteTimeout=0` |
| 请求 ID | `internal/middleware/requestid.go` | 进程前缀 + 原子计数；**不采信客户端传入值** |
| 安全响应头 | `internal/middleware/securityheaders.go` | 五类固定头；不发 HSTS（由 TLS 终止层负责） |
| 可信代理边界 | `proxy.trusted_proxies` | **默认不信任任何代理**；部署在反代之后必须配置 |
| 认证与授权分离 | `internal/middleware` + `internal/shared/auth` | 认证只判身份；授权依赖故障映射 503 而非 403 |
| 限流契约 | `internal/ratelimit` + `internal/router/routespec` | 策略零值即拒绝；键基数达上限拒绝新建（不淘汰活跃桶） |
| JWT 密钥轮换 | `internal/pkg/jwtauth` | 令牌带 kid；未知 kid 直接拒绝（无回退遍历）；退役公钥仍可验签 |
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
| Laravel 存量 token 兼容 | 配 `jwt.laravel.enabled`；终态应删除，见下节清单 |
| pprof | 配 `pprof.enabled`；**所有环境**都只允许字面回环 IP，绑定失败即启动失败 |
| 限流 | 配 `ratelimit.policies` + `ratelimit.key_hmac_secret`，并在 `RouteSpec.RateLimitPolicyID` 引用策略 ID |

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
