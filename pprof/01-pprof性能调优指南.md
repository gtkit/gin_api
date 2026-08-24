# pprof 性能调优指南（gin_api）

> 目标读者：需要定位本项目 CPU 热点、内存占用、goroutine 泄漏、锁竞争的开发者。
> 本文所有接入代码都基于项目真实结构（`internal/bootstrap`、`internal/router`、`internal/platform/config`），可直接落地。
> 当前源码状态：pprof 端点**已内置**，由 `pprof.enabled` 配置门控（默认关闭，
> 只允许绑字面回环 IP）。第 3 节讲它的选型理由与你要动的配置，落地只需改配置。

---

## 目录

1. [pprof 是什么，能分析什么](#1-pprof-是什么能分析什么)
2. [环境准备](#2-环境准备)
3. [本项目的 pprof 端点](#3-本项目的-pprof-端点)
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
| **goroutine** | `/debug/pprof/goroutine` | 当前有多少 goroutine、分别卡在哪？ | 瞬时快照 |
| **goroutineleak** | `/debug/pprof/goroutineleak` | 哪些 goroutine 已**永久卡死**、再也不可能被唤醒？（直接给出泄漏集合） | **每次请求强制一次完整 GC**，见 4.3 |
| **block（阻塞）** | `/debug/pprof/block` | goroutine 在哪些同步原语上**阻塞**等待？（channel、锁等待） | 需开采样率，**有开销** |
| **mutex（锁）** | `/debug/pprof/mutex` | 哪些互斥锁**竞争**激烈？ | 需开采样率，**有开销** |
| **trace（执行跟踪）** | `/debug/pprof/trace?seconds=N` | 调度、GC、网络阻塞的时间线细节（最细粒度） | 采集期间开销较大 |
| threadcreate | `/debug/pprof/threadcreate` | 系统线程创建情况（少用） | 瞬时 |

> 选型经验：
> - 服务 CPU 高 → **CPU profile**
> - 内存持续涨 / OOM → **heap**（看常驻）+ **allocs**（看高频分配）
> - 内存正常但 GC 频繁、延迟抖动 → **allocs**
> - 请求变慢但 CPU 不高 → **block** + **trace**（多半阻塞在 IO / 锁 / channel）
> - goroutine 数量只增不减 → **goroutineleak**（直接拿到泄漏集合）→ 再用 **goroutine** 看全体在等什么

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

## 3. 本项目的 pprof 端点

> 端点**已经内置并接好生命周期**，位置是 `internal/bootstrap/pprof.go`，装配点在
> `internal/bootstrap/app.go:164`。本节解释它为什么长这样、你要动哪些配置。
>
> 这里刻意**不再提供一份可照抄的实现代码**：实现就在上面那个文件里、30 行左右、带完整
> 中文注释。文档里再放一份副本，只会在下一次实现改动后变成一份编译不过的过期代码——
> 这件事已经发生过一次。

### 3.1 为什么是独立端口

| 方式 | 做法 | 优点 | 缺点 | 是否推荐 |
| --- | --- | --- | --- | --- |
| **A. 独立端口** | 另起一个只绑 `127.0.0.1` 的 `http.Server`，挂 `net/http/pprof` | 与业务端口物理隔离；不经过业务中间件；线上可用防火墙/SSH 管控 | 多一个监听 | ✅ **本项目采用** |
| B. 挂到 Gin | 把 pprof 注册到业务 engine 的 `/debug/pprof` | 不用新端口 | 与业务同端口，安全风险大；经过访问日志产生噪音；需自己加鉴权 | ⚠ 仅内网/测试环境临时用 |
| C. 匿名 import | `import _ "net/http/pprof"` + 自己起 `http.ListenAndServe` | 代码最少 | 把 handler 注册进全局 `DefaultServeMux`，污染全局、易误暴露 | ❌ 不推荐 |

选 A 的具体理由，对照 `internal/router/init.go:218` 起的全局中间件栈
（`GinRecovery` → `RequestID` →〔可选 otelgin〕→ `GinLogger` → `Metrics` →
`SecurityHeaders` → `ErrorHandler` →〔可选 `Cors`〕→ `MaxBytesLimit`）：

- 栈里**没有全局鉴权**——鉴权是按路由组挂的。方式 B 会让 `/debug/pprof` 裸奔。
- 栈里有 `GinLogger` 与 `Metrics`——方式 B 的每次采集都会刷访问日志、打进业务指标。
- 栈里有 `MaxBytesLimit`——它限制请求体，对 pprof 的 GET 无碍，但把调试端点放进业务
  中间件链意味着任何一次中间件调整都可能意外影响采集。

独立端口把这三件事一次性规避。

### 3.2 实现的三个关键决定

读 `internal/bootstrap/pprof.go` 时，这几处是刻意为之，不要"顺手优化"掉：

- **先 `net.Listen` 再 `Serve`**：绑定失败即启动失败。若放进后台 goroutine 只打日志，
  就会出现"配置说开了、实际没监听"，排障的人对着一个不存在的地址反复重试。
- **不设 `WriteTimeout`**：CPU 与 trace 采集是长连接（`profile?seconds=30` 会持续 30 秒），
  写超时会把采集从中间掐断。只设 `ReadHeaderTimeout` 防慢连接。
- **关停时把 block/mutex 采样率清零**：采样率是**进程全局状态**，端点没了却不清零，
  等于让进程持续付出采样开销——而那笔开销在监控上只表现为"莫名其妙变慢"。

端点用**独立 `http.ServeMux`**，且靠 `pprof.Index` **按名分发**全部内置 profile，
而不是逐个显式注册。后者看起来更明确，实际会让 Go 新增的内置 profile 静默 404 ——
`goroutineleak` 就是这么自动可用的。`TestPprofMuxServesEveryBuiltinProfile` 钉住这一点。

### 3.3 你要动的配置

配置结构是 `internal/platform/config/types.go` 的 `PprofConfig`，对应配置文件里的 `pprof:` 段：

```yaml
pprof:
  enabled: true              # 默认 false；生产按需临时打开
  host: 127.0.0.1            # 只允许字面回环 IP；prod 下非回环会被配置校验拒绝
  port: "6060"
  block_profile_rate: 0      # 排查 channel/锁阻塞时临时设 10000（约每 10µs 阻塞采一次）
  mutex_profile_fraction: 0  # 排查锁竞争时临时设 100（1/100 的竞争事件被采样）
```

字段语义与安全边界：

- `enabled` 关闭时进程完全不监听，不是"监听了但拒绝访问"。
- `host` 在 prod 环境只接受字面回环地址，这条由配置校验强制（`auth-input-pprof-hardening`
  的 spec 要求），远程访问走第 6 节的 SSH 端口转发。
- `block_profile_rate` / `mutex_profile_fraction` 是**有运行时开销**的采样，
  默认 0（关闭）。排查完记得改回 0；端点关停时代码会把它们清零，但配置留着的话下次启动又会开。

### 3.4 验证端点可用

```bash
# 1) 以开发模式启动（确保对应 env 配置里 pprof.enabled = true）
make dev

# 2) 索引页应列出全部 profile 类型
curl -s http://127.0.0.1:6060/debug/pprof/ | head -20

# 3) 一条命令确认"跑着的这个二进制"是否带 goroutineleak（Go 1.27+ 构建才有）
curl -s http://127.0.0.1:6060/debug/pprof/ | grep -c goroutineleak

# 4) 或直接跑自检脚本，它会把上面这些都查一遍
./pprof/scripts/doctor.sh
```

### 3.5 备选：临时挂到 Gin（仅内网）

若确实不想开新端口（例如临时在内网测试机排查），可以把 `net/http/pprof` 的 handler 用
`gin.WrapF` / `gin.WrapH` 适配后挂进一个**带访问保护**的路由组。两个必须注意的点：

- **务必加保护**（BasicAuth / IP 白名单），否则等于把内存内容与调用栈公开。
- 本项目 `internal/router/init.go` 设了 `engine.RedirectTrailingSlash = false`，
  而 `go tool pprof` 默认访问带尾斜杠的 `/debug/pprof/`，路径要写准确。

这类细节正是推荐独立端口的原因之一。

---

## 4. 采集 profile

### 4.1 用配套脚本（推荐）

```bash
# 采集 30 秒 CPU profile（可加第 2 个参数指定秒数）
./pprof/scripts/collect.sh cpu 30

# 采集堆内存快照
./pprof/scripts/collect.sh heap

# 采集 goroutine 快照（看全体卡在哪）
./pprof/scripts/collect.sh goroutine

# 只采**判定为泄漏**的 goroutine（Go 1.27 起内置；会强制一次 GC，见 4.3）
./pprof/scripts/collect.sh goroutineleak

# 采集累计分配（排查 GC 压力）
./pprof/scripts/collect.sh allocs

# 采集 5 秒 trace
./pprof/scripts/collect.sh trace 5

# 一次性采全部（CPU/heap/goroutine/allocs/block/mutex/goroutineleak）
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

# goroutine：?debug=2 可直接得到人类可读的全栈文本（看全体卡在哪）
curl -o goroutine.txt "http://127.0.0.1:6060/debug/pprof/goroutine?debug=2"

# goroutineleak：直接拿到泄漏集合（Go 1.27+，会强制一次 GC，见 4.3）
curl -s "http://127.0.0.1:6060/debug/pprof/goroutineleak?debug=1"

# trace
curl -o trace.out "http://127.0.0.1:6060/debug/pprof/trace?seconds=5"
```

> **采集 CPU profile 的关键**：采集期间服务必须处于**有代表性的负载**下，否则采到的全是 idle。线上用真实流量；本地用压测工具（如 `wrk`、`hey`、`ab`）对热点接口打流量，边打边采。

### 4.3 goroutineleak：只看真正泄漏的 goroutine

Go 1.27 起 `runtime/pprof` 内置 `goroutineleak`。它和 `goroutine` 的区别是**已经做完了判断**：
`goroutine` 给你全部 goroutine 让你自己看谁不对，`goroutineleak` 只给出运行时判定为
**永久卡死**的那些。

```bash
# 二进制形态（喂给 go tool pprof）
curl -o goroutineleak.pprof "http://127.0.0.1:6060/debug/pprof/goroutineleak"

# 人类可读形态：debug=1 打印泄漏点的调用栈与计数
curl -s "http://127.0.0.1:6060/debug/pprof/goroutineleak?debug=1"

# 只看这 N 秒内**新增**的泄漏（delta），排查"越跑越多"
curl -o leak-delta.pprof "http://127.0.0.1:6060/debug/pprof/goroutineleak?seconds=30"
```

**判定口径（决定了你能信它什么）**：运行时用 GC 可达性分析——goroutine 阻塞在某个并发原语上，
且该原语**从任何可运行 goroutine 都不可达**，则判定为泄漏（谁也不可能再唤醒它）。
所以：

- 报出来的**是**泄漏，实测精度很高：5000 个阻塞在「全局 channel + 有活跃发送方」的 worker
  一个都没被误报，500 个真泄漏全部报出。
- 反过来，仍被全局变量或活跃 goroutine 持有的原语所阻塞的泄漏**不在这份结果里**——
  那种 goroutine 理论上还能被唤醒，只是实际没人唤醒。**空结果的含义是"没有不可达的卡死"，
  不是"没有泄漏"**。这类要靠 `goroutine?debug=2` 人工看。

**代价：每次请求都会强制一次完整 GC**，且这次 GC 的 STW 明显高于普通 GC。本机实测
（129MB 堆、5504 goroutine、darwin/amd64，仅供量级参考）：

| | STW 暂停 | 请求耗时 |
| --- | --- | --- |
| 普通强制 GC | 45–105µs | — |
| 泄漏检测 GC | 790–950µs（约 17 倍） | 约 18ms |
| 对照：`goroutine` profile | 无额外 GC | 约 10ms |

**因此它是排障工具，不要接进 Prometheus 抓取或任何周期性轮询。** 需要长期观察泄漏趋势，
就监控 goroutine 总数，等它异常时再来采这个 profile。

**两个坑**：

- `?debug=2` **不是**"更详细的泄漏栈"，它会退化成打印**全部** goroutine 栈（与
  `goroutine?debug=2` 相同）。要可读的泄漏集合用 `debug=1`。
- 端点是否存在取决于**构建这个二进制的 Go 版本**，不是你本机的 Go 版本。用 1.26 工具链
  构建的二进制访问它只会得到 404。`./pprof/scripts/doctor.sh` 会直接告诉你跑着的这个
  二进制有没有它。

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

> **跨 Go 版本对比时必须标注工具链版本**。Go 1.27 默认开启 size-specialized malloc，
> 小对象（<80B）的分配成本比 1.26 低约 30%，分配密集的程序整体约快 1%。用 1.26 采的
> profile 和 1.27 采的 profile 直接 `-base` 对比，会把这部分工具链收益记成自己优化的成果。
> 归档 profile 时把 `go version` 一起记下来，对比前先确认两份来自同一版本。

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

### 另一条与脱敏有关的约束：goroutine 标签会进入 traceback

Go 1.27 起（本项目 `go.mod` 已声明 1.27），traceback 的头行会包含 `runtime/pprof` 的
goroutine 标签。也就是说，一旦代码里用 `pprof.Labels` / `pprof.Do` 给 goroutine 打了标签，
标签内容就会随 **panic 栈和崩溃日志**一起输出，而崩溃日志的流向通常比 pprof 端点宽得多。

- 本项目自身不调用 `pprof.Labels` / `pprof.Do`，当前无标签可泄露。
- 若在派生项目里用标签承载租户 ID、用户标识这类信息，要么别把敏感值放进标签，
  要么用 `GODEBUG=tracebacklabels=0` 关掉这个行为（该开关官方预期长期保留）。

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
- **goroutine 泄漏**：每个 goroutine 都要有明确退出路径；用 `context` 控制生命周期。
  排查顺序是**先 `goroutineleak` 拿到泄漏集合并定位到代码行，再用 `goroutine?debug=2`
  看这些 goroutine 周围的上下文**（谁本该唤醒它们）。`goroutineleak` 为空不等于没有泄漏，
  判定边界见 4.3。

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

> 本仓目前没有 benchmark（`grep -rl "func Benchmark" --include=*_test.go .` 为空）。
> 要用这条路径，先为目标热点包补一个 `func BenchmarkXxx(b *testing.B)`，再跑上面的命令。
> 补哪个包应由性能工作本身决定——为了让文档成立而随便补一个基准，等于给自己造一条
> 没人看的曲线。

---

## 9. .gitignore 与产物管理

- `pprof/*.md` 与 `pprof/scripts/*.sh` 被 git 跟踪，随仓库分发（派生项目经 `gonew` 一并继承）。
- 采集产物由 `pprof/.gitignore` 忽略：`profiles/` 目录，以及散落的 `*.pprof`、`*.prof`、
  `*.pb.gz`、`*.out`。目的是防止把几十 MB 的 profile 误提交进仓库。
- PGO 的 `default.pgo` **不在本目录**，而在项目根目录，且**必须提交**——详见
  `02-PGO优化指南.md`。不要在任何 `.gitignore` 里忽略它。

---

## 10. 命令速查表

```bash
# ——— 采集（服务跑在 127.0.0.1:6060）———
curl -o cpu.pprof "http://127.0.0.1:6060/debug/pprof/profile?seconds=30"  # CPU 30s
curl -o heap.pprof "http://127.0.0.1:6060/debug/pprof/heap"               # 堆快照
curl -o goroutine.txt "http://127.0.0.1:6060/debug/pprof/goroutine?debug=2" # goroutine 全栈
curl -s "http://127.0.0.1:6060/debug/pprof/goroutineleak?debug=1"          # 泄漏集合（会强制 GC）
curl -o trace.out "http://127.0.0.1:6060/debug/pprof/trace?seconds=5"     # trace 5s

# ——— 分析 ———
go tool pprof -http=:8080 cpu.pprof          # Web UI / 火焰图
go tool pprof cpu.pprof                       # 命令行交互（top/list/web/peek）
go tool pprof -http=:8080 -base old new       # 两份对比
go tool trace trace.out                       # trace 专用查看器

# ——— 脚本 ———
./pprof/scripts/collect.sh all                # 采全部类型（含 goroutineleak，排最后）
./pprof/scripts/collect.sh goroutineleak      # 只采泄漏集合
./pprof/scripts/doctor.sh                     # 自检：含"端点是否提供 goroutineleak"
./pprof/scripts/analyze.sh <file|url>         # 打开 Web UI
./pprof/scripts/bench-profile.sh <包路径>     # benchmark 剖析

# ——— 线上 ———
ssh -N -L 16060:127.0.0.1:6060 user@host      # 端口转发后按本地访问
```

---

下一步：读 [`02-PGO优化指南.md`](./02-PGO优化指南.md)，用 pprof 采集的 CPU profile 进一步做编译器级优化。
