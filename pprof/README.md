# pprof 性能调优工具箱

> 本目录为 `gin_api` 项目的性能剖析（profiling）与性能引导优化（PGO）操作手册与配套脚本。
> 所有文档与脚本均针对本项目的真实结构（Gin + GORM + cobra 启动、`sonic` 构建标签）编写。

---

## 一、这个目录里有什么

| 文件 | 作用 |
| --- | --- |
| `README.md` | 本文件，导览 + 三分钟快速上手 |
| `01-pprof性能调优指南.md` | **核心文档**：pprof 原理、如何在本项目接入、采集、分析、实战调优全流程 |
| `02-PGO优化指南.md` | **PGO 文档**：是否需要 PGO 的判断、完整接入流程、CI 集成、注意事项 |
| `scripts/collect.sh` | 一键采集各类 profile（CPU / heap / goroutine / goroutineleak / allocs / block / mutex / trace） |
| `scripts/analyze.sh` | 启动 pprof Web UI（火焰图）分析指定 profile 文件或在线端点 |
| `scripts/bench-profile.sh` | 用 Go benchmark 生成 CPU / 内存 profile，供离线分析或 PGO 使用（本仓暂无 benchmark，需先为热点包补 `func BenchmarkXxx`） |
| `scripts/pgo-merge.sh` | 采集 / 合并多份 CPU profile 生成 `default.pgo`，并校验构建是否启用 PGO |
| `scripts/doctor.sh` | 自检脚本：命令依赖、脚本语法、端点连通性、PGO、`.gitignore` 状态 |
| `04-落地状态与后续建议.md` | 当前目录是否满足需求、源码接入状态、下一步建议 |
| `profiles/` | （运行脚本后自动生成）采集到的 profile 产物存放目录，已被 git 忽略 |
| `.gitignore` | 忽略采集产物，避免把临时 profile 提交进仓库 |

---

## 二、三分钟快速上手

> 前提：pprof 端点已内置，配置门控。在 `config/env/env.yml` 里把 `pprof.enabled`
> 设为 `true` 即启动独立端点（默认 `127.0.0.1:6060`，只允许字面回环 IP，
> 所有环境一致；远程排障走 SSH 端口转发）。未开启时下面的命令会连接失败。

```bash
# 0. 准备：安装可视化依赖（火焰图的 Graph 视图需要 graphviz；macOS）
brew install graphviz

# 0.1 先做本地自检
./pprof/scripts/doctor.sh

# 1. 采集一份 30 秒 CPU profile（服务需处于有负载状态）
./pprof/scripts/collect.sh cpu

# 2. 用 Web UI 打开刚采集的 profile，看火焰图找热点
./pprof/scripts/analyze.sh pprof/profiles/cpu-<时间戳>.pprof

# 3. 采集当前堆内存快照，排查内存占用
./pprof/scripts/collect.sh heap

# 4. 只采"已经永久卡死"的 goroutine（Go 1.27+；会强制一次 GC，别高频跑）
./pprof/scripts/collect.sh goroutineleak

# 5. 一次性采集全部类型（CPU/heap/goroutine/allocs/block/mutex/goroutineleak）
./pprof/scripts/collect.sh all
```

---

## 三、本项目关键信息速查

调优时经常要用到这些项目事实，集中列在这里，避免来回翻代码：

| 项 | 值 / 位置 | 说明 |
| --- | --- | --- |
| 启动入口 | `main.go` → `cmd.Execute()` → `server` 子命令 | cobra 多命令 |
| 服务装配 | `internal/bootstrap/`（`app.go` / `providers.go` / `http.go`） | HTTP server 在此构建 |
| Gin engine 创建 | `internal/router/init.go` 的 `New(envName, opt)` | 返回 `*gin.Engine` |
| 健康探针路由 | `internal/router/init.go:349` 注册 `GET /health`，处理逻辑在 `internal/router/health.go` | 附加路由的写法可参照它 |
| 全局中间件栈 | `internal/router/init.go:218` 起 | `GinRecovery`→`RequestID`→〔otelgin〕→`GinLogger`→`Metrics`→`SecurityHeaders`→`ErrorHandler`→〔`Cors`〕→`MaxBytesLimit`（**无全局超时、无全局鉴权**——两者都按路由组挂） |
| 业务监听地址 | `config.App.Host` + `config.App.Port` | 由 `-c <env>` 选择的配置文件提供 |
| 环境判断 | `config.Env`，`IsProd()`/`IsTest()`/`IsDev()` | 控制是否启用 pprof |
| 开发启动 | `make dev` = `go run -tags="sonic" . server -c dev` | |
| 生产构建 | `make build` 默认 linux/amd64，含 `-trimpath`、sonic tag 与构建元数据；保留 `Linux`/`BUILD_FLAGS` 覆写。UPX 存在时压缩，缺失时跳过 | PGO 的 `default.pgo` 放项目根目录即自动生效 |
| Go 版本 | 1.27（`go.mod`） | 完整支持 PGO |

---

## 四、阅读顺序建议

1. **先读** `01-pprof性能调优指南.md`：理解 pprof 能干什么、在本项目怎么接入、怎么采集和分析。
2. 跑通快速上手，对自己的服务采一次 CPU + heap profile，熟悉火焰图。
3. **再读** `02-PGO优化指南.md`：当 pprof 已经帮你定位并优化了明显热点之后，用 PGO 榨取编译器层面的额外收益。
4. **读** `04-落地状态与后续建议.md`：确认当前目录是否满足需求，以及下一步推荐动作。
5. **随时查** `03-go-tool-pprof命令详解.md`：敲命令时遇到「这个参数啥意思」「这种写法对不对」「报错怎么办」，当字典翻。

> pprof 与 PGO 的关系：pprof 是「诊断工具」，帮你发现并手动优化热点；PGO 是「自动优化」，把生产 CPU profile 喂给编译器，让它针对真实热路径做内联等优化。**两者互补**，PGO 用的 profile 正是 pprof 采集的 CPU profile。

---

## 五、安全红线（务必先看）

pprof 端点会暴露**内存内容、调用栈、命令行参数**等敏感信息，且 CPU/trace 采集本身有性能开销。

- **绝不要**把 `/debug/pprof` 直接暴露在公网业务端口上。
- 本项目方案：pprof 跑在**独立端口**且默认**只绑 `127.0.0.1`**，线上通过 SSH 端口转发访问（见 `01` 文档第 6 节）。
- 生产开启 block/mutex profile 会有额外开销，**用完务必关闭**（采样率归零）。

---

## 六、关于 `.gitignore`

- `pprof/*.md` 与 `pprof/scripts/*.sh` 被 git 跟踪，随仓库分发（派生项目经 `gonew` 继承）；`project init` 保留本目录，仅精确移除母版部署入口和两份 REVIEW 报告。
- 采集产物（`profiles/`、`*.pprof`、`*.prof`、`*.pb.gz`、`*.out`）由 `pprof/.gitignore` 忽略。
- PGO 的 `default.pgo` 在**项目根目录**且**必须提交**（官方最佳实践，见 `02` 文档），
  任何 `.gitignore` 都不该忽略它。
