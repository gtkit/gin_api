# pprof 性能调优指南（gin_api）

> 目标读者：需要定位本项目 CPU 热点、内存占用、goroutine 泄漏、锁竞争的开发者。
> 本文所有接入代码都基于项目真实结构（`internal/bootstrap`、`internal/router`、`internal/platform/config`），可直接落地。
> 当前源码状态：pprof 端点**已内置**，由 `pprof.enabled` 配置门控（默认关闭，
> 只允许绑字面回环 IP）。第 3 节保留为原理与选型说明，落地只需改配置。

---

## 目录

1. [pprof 是什么，能分析什么](#1-pprof-是什么能分析什么)
2. [环境准备](#2-环境准备)
3. [在本项目接入 pprof（重点）](#3-在本项目接入-pprof重点)
4. [采集 profile](#4-采集-profile)
5. [分析 profile](#5-分析-profile)
6. [线上安全访问（SSH 端口转发）](#6-线上安全访问ssh-端口转发)
7. [实战调优流程](#7-实战调优流程)
8. [用 benchmark + pprof 做离线剖析](#8-用-benchmark--pprof-做离线剖析)
9. [.gitignore 与产物管理](#9-gitignore-与产物管理)
10. [命令速查表](#10-命令速查表)

---

## 1. pprof 是什么，能分析什么

`pprof` 是 Go 官方内置的性能剖析工具，由两部分组成：

- **运行时采样**：Go runtime 在程序运行时按一定频率采样，记录「时间花在哪些函数」「内存在哪里分配」等。
- **`go tool pprof` 分析器**：把采样数据（profile）解析成 top 列表、调用图、火焰图、源码级耗时标注。

本项目能采集的 profile 类型：

| 类型 | 端点 | 回答的问题 | 开销 |
| --- | --- | --- | --- |
| **CPU** | `/debug/pprof/profile?seconds=N` | CPU 时间花在哪些函数上？（找计算热点） | 采集期间有开销，采完即止 |
| **heap（堆）** | `/debug/pprof/heap` | 当前**存活对象**占用了多少内存、在哪分配的？（找常驻内存大户） | 几乎无开销，瞬时快照 |
| **allocs（分配）** | `/debug/pprof/allocs` | 程序启动以来**累计分配**了多少？（找高频分配、GC 压力源） | 几乎无开销 |
| **goroutine** | `/debug/pprof/goroutine` | 当前有多少 goroutine、卡在哪？（找 goroutine 泄漏 / 阻塞） | 瞬时快照 |
| **block（阻塞）** | `/debug/pprof/block` | goroutine 在哪些同步原语上**阻塞**等待？（channel、锁等待） | 需开采样率，**有开销** |
| **mutex（锁）** | `/debug/pprof/mutex` | 哪些互斥锁**竞争**激烈？ | 需开采样率，**有开销** |
| **trace（执行跟踪）** | `/debug/pprof/trace?seconds=N` | 调度、GC、网络阻塞的时间线细节（最细粒度） | 采集期间开销较大 |
| threadcreate | `/debug/pprof/threadcreate` | 系统线程创建情况（少用） | 瞬时 |

> 选型经验：
> - 服务 CPU 高 → **CPU profile**
> - 内存持续涨 / OOM → **heap**（看常驻）+ **allocs**（看高频分配）
> - 内存正常但 GC 频繁、延迟抖动 → **allocs**
> - 请求变慢但 CPU 不高 → **block** + **trace**（多半阻塞在 IO / 锁 / channel）
> - goroutine 数量只增不减 → **goroutine**（泄漏）

---

## 2. 环境准备

```bash
# 1) go tool pprof 随 Go 自带，确认 Go 版本（本项目要求 1.27）
go version

# 2) 火焰图的「调用图（Graph）」视图依赖 graphviz；「Flame Graph」视图是内置的不依赖它
#    建议装上，分析更顺手
brew install graphviz          # macOS
# sudo apt-get install -y graphviz   # Debian/Ubuntu 线上机

# 3) 确认能编译（PGO 章节会用到）
go build -tags="sonic" ./...
```

---

## 3. 在本项目接入 pprof（重点）

### 3.1 三种接入方式对比

| 方式 | 做法 | 优点 | 缺点 | 是否推荐 |
| --- | --- | --- | --- | --- |
| **A. 独立端口** | 另起一个只绑 `127.0.0.1` 的 `http.Server`，挂 `net/http/pprof` | 与业务端口物理隔离；不经过业务中间件；线上可用防火墙/SSH 管控 | 需新增一个文件 + 改 `app.go` 几行 | ✅ **推荐** |
| B. 挂到 Gin | 仿照 `RegisterHealthRoutes` 把 pprof 注册到业务 engine 的 `/debug/pprof` | 不用新端口 | 与业务同端口，安全风险大；经过 `GinLogger` 产生噪音日志；需自己加鉴权 | ⚠️ 仅内网/测试环境临时用 |
| C. 匿名 import | `import _ "net/http/pprof"` + 自己起 `http.ListenAndServe` | 代码最少 | 把 handler 注册进全局 `DefaultServeMux`，污染全局、易误暴露 | ❌ 不推荐 |

> 为什么本项目首选 A：
> 查 `internal/middleware/init.go`，全局中间件栈是 `Recovery → ErrorHandler → Cors → requestid → GinLogger`，**没有全局超时、没有全局鉴权**。这意味着若用方式 B，`/debug/pprof` 会完全裸奔（任何人可访问），且每次采集都会刷一条访问日志。独立端口能彻底规避这两点。

### 3.2 第 1 步：新增配置项

在 `internal/platform/config/types.go` 的 `Config` 结构体中追加一个字段（紧挨现有字段，保持风格一致）：

```go
// Config 顶层结构（节选，仅展示新增部分）
type Config struct {
    Env Env       `mapstructure:"-"`
    App AppConfig  `mapstructure:"application"`
    // ... 其余现有字段保持不变 ...
    Pprof PprofConfig `mapstructure:"pprof"` // 新增：性能剖析端点配置
}

// PprofConfig 控制 pprof 调试端点。
// 默认不启用；仅在需要排查性能问题时按环境打开。
type PprofConfig struct {
    // Enabled 是否启动独立的 pprof HTTP 端点。
    Enabled bool `mapstructure:"enabled"`
    // Host pprof 监听地址。强烈建议保持 127.0.0.1，线上通过 SSH 端口转发访问。
    // 留空时代码内兜底为 "127.0.0.1"。
    Host string `mapstructure:"host"`
    // Port pprof 监听端口，留空时兜底为 "6060"。
    Port string `mapstructure:"port"`
    // BlockProfileRate block profile 采样率：每发生 N 纳秒阻塞采样一次，0 表示关闭。
    // 典型排查值 10000（约每 10µs 阻塞采样），排查完应改回 0。
    BlockProfileRate int `mapstructure:"block_profile_rate"`
    // MutexProfileFraction mutex profile 采样比例：1/N 的锁竞争事件被采样，0 表示关闭。
    // 典型排查值 100，排查完应改回 0。
    MutexProfileFraction int `mapstructure:"mutex_profile_fraction"`
}
```

对应配置文件（本项目用 `-c <env>` 选择 `config/` 下的文件）里加一段，例如**仅在 dev / test 开启**：

```yaml
# config/<env> 对应配置文件中新增（YAML 示例，键名与 mapstructure tag 对齐）
pprof:
  enabled: true            # 生产环境建议设为 false，需要时再临时打开
  host: 127.0.0.1          # 只绑回环，外部无法直连
  port: "6060"
  block_profile_rate: 0    # 排查锁/channel 阻塞时临时改为 10000
  mutex_profile_fraction: 0 # 排查锁竞争时临时改为 100
```

> 不确定本项目配置文件具体格式时，先打开 `config/` 目录里某个现有 env 文件照葫芦画瓢；键的层级以 `mapstructure` tag 为准。

### 3.3 第 2 步：新增 `internal/bootstrap/pprof.go`

新建文件，内容如下（**已含详细中文注释**，可直接复制）：

```go
package bootstrap

import (
    "context"
    "net"
    "net/http"
    "net/http/pprof"
    "runtime"
    "time"

    "gin_api/internal/platform/config"

    "github.com/gtkit/logger"
)

// pprofShutdownTimeout pprof 端点优雅关闭的等待上限。
const pprofShutdownTimeout = 3 * time.Second

// startPprofServer 根据配置启动一个独立的 pprof HTTP 端点。
//
// 设计要点：
//  1. 使用独立的 http.ServeMux，不污染全局 DefaultServeMux（方式 C 的弊端）；
//  2. 默认只绑 127.0.0.1，外部无法直连，线上通过 SSH 端口转发访问；
//  3. 与业务 engine 完全隔离，不经过任何业务中间件（无日志噪音、无超时干扰）。
//
// 返回一个 shutdown 函数：调用它会优雅关闭 pprof 端点。
// 当未启用（cfg.Enabled == false）时返回一个空操作函数，调用方无需做 nil 判断。
func startPprofServer(cfg config.PprofConfig) (shutdown func(context.Context) error) {
    // 空操作关闭函数：未启用时返回它，简化调用方逻辑。
    noop := func(context.Context) error { return nil }

    if !cfg.Enabled {
        logger.Info("pprof endpoint disabled")
        return noop
    }

    // 按需开启 block / mutex 采样。这两类采样有运行时开销，
    // 因此默认关闭（采样率为 0），仅在配置显式打开时生效。
    if cfg.BlockProfileRate > 0 {
        runtime.SetBlockProfileRate(cfg.BlockProfileRate)
        logger.Infof("pprof block profile enabled, rate=%d", cfg.BlockProfileRate)
    }
    if cfg.MutexProfileFraction > 0 {
        runtime.SetMutexProfileFraction(cfg.MutexProfileFraction)
        logger.Infof("pprof mutex profile enabled, fraction=%d", cfg.MutexProfileFraction)
    }

    // 兜底默认值：未配置时绑回环 + 6060。
    host := cfg.Host
    if host == "" {
        host = "127.0.0.1"
    }
    port := cfg.Port
    if port == "" {
        port = "6060"
    }
    addr := net.JoinHostPort(host, port)

    // 手动把 net/http/pprof 的 handler 注册到独立 mux。
    // 注意：pprof.Index 会处理 /debug/pprof/ 下的 heap、goroutine、allocs、
    // block、mutex、threadcreate 等子路径；而 profile/cmdline/symbol/trace
    // 是特殊 handler，必须单独注册。
    mux := http.NewServeMux()
    mux.HandleFunc("/debug/pprof/", pprof.Index)
    mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
    mux.HandleFunc("/debug/pprof/profile", pprof.Profile) // CPU profile
    mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
    mux.HandleFunc("/debug/pprof/trace", pprof.Trace) // 执行跟踪

    server := &http.Server{
        Addr:    addr,
        Handler: mux,
        // 不要给 pprof server 设置 WriteTimeout！
        // CPU/trace 采集是长连接（profile?seconds=30 会持续 30 秒），
        // 设置写超时会导致采集被中途掐断。这也是不挂到业务 engine 的原因之一。
        ReadHeaderTimeout: 5 * time.Second,
    }

    go func() {
        logger.Infof("pprof endpoint starting on http://%s/debug/pprof/", addr)
        // ListenAndServe 在 server 关闭时返回 http.ErrServerClosed，属正常关闭。
        if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
            logger.Errorf("pprof endpoint exited: %v", err)
        }
    }()

    // 返回优雅关闭函数：交给调用方在进程退出时调用。
    return server.Shutdown
}
```

### 3.4 第 3 步：在 `app.go` 的生命周期里接入

只需在 `App.Run` 开头启动、退出前关闭。下面是基于现有 `internal/bootstrap/app.go` 的**最小改动**（用 `+` 标出新增行）：

```go
func (a *App) Run(ctx context.Context) error {
    defer a.closeResources()

+   // 启动独立 pprof 端点（按配置决定是否真正监听）。
+   // 放在最前面，确保进程一启动就能采样；defer 保证退出时优雅关闭。
+   pprofShutdown := startPprofServer(a.runtime.Config().Pprof)
+   defer func() {
+       shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pprofShutdownTimeout)
+       defer cancel()
+       if err := pprofShutdown(shutdownCtx); err != nil {
+           logger.Errorf("shutdown pprof endpoint: %v", err)
+       }
+   }()

    runCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
    defer stop()

    // ... 其余代码保持不变 ...
}
```

> 说明：`a.runtime` 是 `App` 已持有的字段（见 `app.go` 第 20 行），`Config()` 方法在 `runtime.go` 已存在，无需新增访问器。

### 3.5 第 4 步：验证接入成功

```bash
# 1) 以开发模式启动（确保对应 env 配置里 pprof.enabled = true）
make dev          # 等价于 go run -tags="sonic" . server -c dev

# 2) 另开终端，访问 pprof 索引页，应返回 HTML 列表
curl -s http://127.0.0.1:6060/debug/pprof/ | head -20

# 3) 用 go tool 拉一份 5 秒 CPU profile 并进入交互界面
go tool pprof http://127.0.0.1:6060/debug/pprof/profile?seconds=5
#   进入后输入 top 看占用，输入 quit 退出
```

看到端点列表、`go tool pprof` 能进入交互界面，即接入成功。

### 3.6 备选：挂到 Gin（方式 B，仅内网临时用）

若确实不想开新端口（例如临时在内网测试机排查），可仿照 `internal/router/health.go` 的 `RegisterHealthRoutes`，新增一个注册函数并在 `providers.go` 调用。**务必加访问保护**，否则裸奔：

```go
// 放在 internal/router/ 下，例如 pprof.go
package router

import (
    "net/http"
    "net/http/pprof"

    "github.com/gin-gonic/gin"
)

// RegisterPprofRoutes 把 pprof 端点挂到 Gin engine 的 /debug/pprof 分组。
// guard 是访问保护中间件（如 BasicAuth / IP 白名单），生产环境必须传入有效保护。
func RegisterPprofRoutes(engine *gin.Engine, guard ...gin.HandlerFunc) {
    group := engine.Group("/debug/pprof", guard...)
    // gin.WrapF / gin.WrapH 把标准库 handler 适配成 gin.HandlerFunc。
    group.GET("/", gin.WrapF(pprof.Index))
    group.GET("/cmdline", gin.WrapF(pprof.Cmdline))
    group.GET("/profile", gin.WrapF(pprof.Profile))
    group.GET("/symbol", gin.WrapF(pprof.Symbol))
    group.GET("/trace", gin.WrapF(pprof.Trace))
    // Index 内部会按子路径分发到 heap/goroutine/allocs/block/mutex 等。
    group.GET("/:name", gin.WrapH(http.HandlerFunc(pprof.Index)))
}
```

> ⚠️ 注意 Gin 路由的尾斜杠：本项目在 `init.go` 设了 `engine.RedirectTrailingSlash = false`，访问时路径要写准确。`go tool pprof` 默认访问 `/debug/pprof/`（带尾斜杠）。这类坑也是推荐独立端口的原因之一。

---

## 4. 采集 profile

### 4.1 用配套脚本（推荐）

```bash
# 采集 30 秒 CPU profile（可加第 2 个参数指定秒数）
./pprof/scripts/collect.sh cpu 30

# 采集堆内存快照
./pprof/scripts/collect.sh heap

# 采集 goroutine 快照（排查泄漏）
./pprof/scripts/collect.sh goroutine

# 采集累计分配（排查 GC 压力）
./pprof/scripts/collect.sh allocs

# 采集 5 秒 trace
./pprof/scripts/collect.sh trace 5

# 一次性采全部（CPU/heap/goroutine/allocs/block/mutex）
./pprof/scripts/collect.sh all

# 指定目标地址（默认 127.0.0.1:6060）
PPROF_ADDR=10.0.0.5:6060 ./pprof/scripts/collect.sh cpu
```

所有产物带时间戳保存到 `pprof/profiles/`，便于归档对比。

### 4.2 用原生命令（理解脚本在做什么）

```bash
# CPU：采集 30 秒（注意 ?seconds= 决定采样时长，期间要让服务有真实负载）
curl -o cpu.pprof "http://127.0.0.1:6060/debug/pprof/profile?seconds=30"

# heap：瞬时快照
curl -o heap.pprof "http://127.0.0.1:6060/debug/pprof/heap"

# goroutine：?debug=2 可直接得到人类可读的全栈文本（排查泄漏极有用）
curl -o goroutine.txt "http://127.0.0.1:6060/debug/pprof/goroutine?debug=2"

# trace
curl -o trace.out "http://127.0.0.1:6060/debug/pprof/trace?seconds=5"
```

> **采集 CPU profile 的关键**：采集期间服务必须处于**有代表性的负载**下，否则采到的全是 idle。线上用真实流量；本地用压测工具（如 `wrk`、`hey`、`ab`）对热点接口打流量，边打边采。

---

## 5. 分析 profile

### 5.1 Web UI（火焰图，最直观，推荐）

```bash
# 用脚本：传 profile 文件
./pprof/scripts/analyze.sh pprof/profiles/cpu-20260625-1530.pprof

# 用脚本：直接分析在线端点（自动采集再打开 UI）
./pprof/scripts/analyze.sh http://127.0.0.1:6060/debug/pprof/heap

# 等价的原生命令
go tool pprof -http=:8080 pprof/profiles/cpu-20260625-1530.pprof
```

浏览器会自动打开 `http://localhost:8080`，重点看：

- **Flame Graph（火焰图）**：横条越宽 = 占用越多；从上往下是调用栈。**找最宽的格子就是热点**。
- **Graph（调用图）**：节点越大/边越粗 = 占用越多（需要 graphviz）。
- **Top**：按耗时/占用排序的函数列表。
- **Source**：函数源码逐行标注耗时（定位到具体代码行）。
- 右上角 **VIEW** 和 **SAMPLE** 下拉：heap profile 可切 `inuse_space`（常驻）/`alloc_space`（累计分配）/`inuse_objects`/`alloc_objects`。

### 5.2 命令行交互（无 GUI 的线上机）

```bash
go tool pprof pprof/profiles/cpu-20260625-1530.pprof
```

进入交互界面后的核心命令：

| 命令 | 作用 |
| --- | --- |
| `top` / `top20` | 按自身耗时排序的前 N 个函数 |
| `top -cum` | 按累计耗时（含子调用）排序 |
| `list 函数名` | 显示该函数源码并逐行标注耗时（最常用，直接定位代码行） |
| `web` | 生成调用图 SVG 并打开（需 graphviz） |
| `peek 函数名` | 看某函数的调用者和被调用者 |
| `traces` | 显示采样到的调用栈 |
| `tree` | 以文本树展示调用关系 |
| `quit` / `q` | 退出 |

读 `top` 输出的两列关键指标：
- **flat**：函数**自身**消耗（不含它调用的子函数）。flat 高 = 这个函数本身慢。
- **cum**：**累计**消耗（含子函数）。cum 高但 flat 低 = 慢在它调用的下游。

### 5.3 两份 profile 对比（优化前后 / 内存增长）

```bash
# -base：以 old 为基线，显示 new 相对它的增量（正=变多，负=变少）
go tool pprof -http=:8080 -base old.pprof new.pprof
```

排查内存泄漏：间隔一段时间采两份 heap，用 `-base` 对比，**持续增长且不释放**的分配点就是嫌疑。

---

## 6. 线上安全访问（SSH 端口转发）

线上 pprof 绑在 `127.0.0.1:6060`，外部连不上。通过 SSH 把线上端口转发到本地：

```bash
# 把线上机的 127.0.0.1:6060 映射到本地 16060
ssh -N -L 16060:127.0.0.1:6060 user@线上机IP

# 另开终端，像访问本地一样分析线上服务
go tool pprof -http=:8080 http://127.0.0.1:16060/debug/pprof/profile?seconds=30
./pprof/scripts/collect.sh cpu 30   # 配合 PPROF_ADDR=127.0.0.1:16060
```

> 这样做的好处：pprof 端点永不暴露公网，访问权限完全由 SSH 登录权限控制，无需在应用层再写鉴权。

---

## 7. 实战调优流程

一个可复用的端到端方法论：

```
1. 立目标   明确优化什么：降 CPU？降内存？降 P99 延迟？先有可量化指标。
            ↓
2. 测基线   压测热点接口，记录基线：QPS、P50/P99、CPU%、RSS、GC 次数。
            （用 wrk/hey 打固定流量，记录 go tool pprof 之外的系统指标）
            ↓
3. 采样     边压测边采对应 profile（CPU 高就采 cpu，内存高就采 heap+allocs）。
            ↓
4. 定位     火焰图找最宽的格子 / top 找 flat 最高的函数 / list 定位到代码行。
            ↓
5. 改一处   一次只改一个热点（避免多变量干扰）。常见优化方向见下。
            ↓
6. 复测     用同样的压测参数复测，与基线对比；用 -base 对比两份 profile 确认热点下降。
            ↓
7. 守住     用 benchmark 固化（见第 8 节），防止后续回归。回到第 3 步迭代下一个热点。
```

常见优化方向（按 profile 类型）：

- **CPU 热点**：减少重复计算 / 加缓存；避免在循环里做正则编译、反射、JSON 反复编解码；高频路径换更快实现（本项目已用 `sonic` 加速 JSON）。
- **内存 alloc 高**：用 `sync.Pool` 复用对象；预分配 slice/map 容量；避免不必要的 `[]byte`↔`string` 转换；减少闭包逃逸。
- **block / mutex**：缩小锁粒度 / 临界区；读多写少用 `sync.RWMutex` 或 `sync.Map`；用分片锁；channel 缓冲区调优。
- **goroutine 泄漏**：每个 goroutine 都要有明确退出路径；用 `context` 控制生命周期；`goroutine?debug=2` 看卡在哪一行。

> 一条实测教训：service 层用 map 做本地缓存而未加锁，曾在并发下直接 panic。
> 优化并发热点时，改完务必跑 `make test-race`（= `go test -race`）验证没引入数据竞争——
> 竞争在低负载下往往不显形，等到线上才暴露。

---

## 8. 用 benchmark + pprof 做离线剖析

不依赖运行中的服务，对单个函数/包做精准剖析，也是 PGO 之外固化性能的手段：

```bash
# 用脚本：对指定包跑 benchmark 并生成 cpu+mem profile
./pprof/scripts/bench-profile.sh ./internal/pkg/encoding

# 等价原生命令
go test -run=^$ -bench=. -benchmem \
  -cpuprofile=cpu.pprof -memprofile=mem.pprof \
  ./internal/pkg/encoding

# 分析
go tool pprof -http=localhost:8080 cpu.pprof
```

> 本项目已有 benchmark：`internal/pkg/encoding/encoding_test.go`、`internal/pkg/copier/copy_test.go`，可直接作为剖析与回归基准的样板。

---

## 9. .gitignore 与产物管理

本目录已附带 `pprof/.gitignore`，忽略采集产物（`*.pprof`、`*.prof`、`profiles/` 等），防止误提交大文件。

**关于文档本身能否提交**：当前根 `.gitignore` 中的 Markdown 忽略规则是 `#*.md`（已注释），所以本目录的 `.md` 文档可以被 git 跟踪。若以后恢复启用 `*.md`，需要在其后追加例外：

```gitignore
# 允许 pprof 目录下的 Markdown 文档纳入版本控制
!pprof/**/*.md
```

> 只有在根 `.gitignore` 重新启用 `*.md` 时才需要这个例外。`default.pgo`（PGO 用）不受 Markdown 规则影响，正常可提交，详见 `02-PGO优化指南.md`。

---

## 10. 命令速查表

```bash
# ——— 采集（服务跑在 127.0.0.1:6060）———
curl -o cpu.pprof "http://127.0.0.1:6060/debug/pprof/profile?seconds=30"  # CPU 30s
curl -o heap.pprof "http://127.0.0.1:6060/debug/pprof/heap"               # 堆快照
curl -o goroutine.txt "http://127.0.0.1:6060/debug/pprof/goroutine?debug=2" # goroutine 全栈
curl -o trace.out "http://127.0.0.1:6060/debug/pprof/trace?seconds=5"     # trace 5s

# ——— 分析 ———
go tool pprof -http=:8080 cpu.pprof          # Web UI / 火焰图
go tool pprof cpu.pprof                       # 命令行交互（top/list/web/peek）
go tool pprof -http=:8080 -base old new       # 两份对比
go tool trace trace.out                       # trace 专用查看器

# ——— 脚本 ———
./pprof/scripts/collect.sh all                # 采全部类型
./pprof/scripts/analyze.sh <file|url>         # 打开 Web UI
./pprof/scripts/bench-profile.sh <包路径>     # benchmark 剖析

# ——— 线上 ———
ssh -N -L 16060:127.0.0.1:6060 user@host      # 端口转发后按本地访问
```

---

下一步：读 [`02-PGO优化指南.md`](./02-PGO优化指南.md)，用 pprof 采集的 CPU profile 进一步做编译器级优化。
