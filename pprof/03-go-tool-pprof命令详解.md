# go tool pprof 命令逐条详解

> 本文逐条拆解一组真实使用的 `go tool pprof` / `curl` 命令，解释每个参数、每种写法的含义与差异，
> 并扩展所有常见用法与坑。命令里的端口、路径沿用你给的实际例子（业务端口 `8008`、文件路径 `~/pprof/...`）。

---

## 目录

1. [先搞懂两个基础概念](#1-先搞懂两个基础概念)
2. [Shell 转义：`?`、`=`、换行符到底是什么](#2-shell-转义)
3. [逐条详解你的命令](#3-逐条详解你的命令)
4. [`~/pprof/` 自动保存目录与命名规则](#4-pprof-自动保存目录)
5. [类似用法大全（按场景）](#5-类似用法大全按场景)
6. [交互模式命令速查](#6-交互模式命令速查)
7. [常见报错与排查](#7-常见报错与排查)

---

## 1. 先搞懂两个基础概念

理解这两点，所有命令都能秒懂。

### 概念一：go tool pprof 有「两种模式」

```bash
# 模式 A —— 交互式命令行：不加 -http，进入 (pprof) 提示符，敲 top/list/web 等
go tool pprof <数据源>

# 模式 B —— Web UI：加 -http=localhost:端口，浏览器打开火焰图/调用图/源码标注
go tool pprof -http=localhost:8082 <数据源>
```

区别只在**有没有 `-http`**。有就开网页，没有就进命令行。

### 概念二：`<数据源>` 有「三种形态」

`go tool pprof` 最后那个参数（数据源 / source）可以是：

| 形态 | 例子 | 含义 |
| --- | --- | --- |
| **在线 URL** | `http://localhost:8008/debug/pprof/profile?seconds=30` | 现场向运行中的服务采集 |
| **本地文件** | `cpu.pprof` 或 `~/pprof/pprof.mygin.samples.cpu.003.pb.gz` | 分析已保存的 profile |
| **二进制 + profile** | `./bin/gin_api cpu.pprof` | 老式两段式，用二进制补符号信息 |

> 在线 URL 时，pprof 会先把数据下载下来（并顺手存一份到 `~/pprof/`，见第 4 节），再进入分析。

---

## 2. Shell 转义

你的命令里出现了对问号、等号、换行符的转义，先把它们讲清楚——这些是 **shell（zsh）层面**的东西，不是 pprof 的参数。

### 问号 `?` —— 必须处理

在 zsh / bash 里，问号是**通配符**（匹配任意单个字符）。URL `profile?seconds=30` 里的问号若不处理，shell 会试图拿它去匹配文件名：

- zsh 默认行为：匹配不到就报 `zsh: no matches found`，命令根本不执行。
- 解决办法二选一：① 用反斜杠转义问号；② **给整个 URL 加引号（更推荐，最清晰）**。

```bash
# 推荐写法：整个 URL 加双引号，问号、等号都不用单独转义
go tool pprof "http://localhost:8008/debug/pprof/profile?seconds=30"
```

### 等号 `=` —— 通常不必，转了也无害

zsh 只有在**词首**才对等号做特殊扩展。URL 中间的等号不在词首，**本来就不需要转义**。给等号加反斜杠属于「过度保险」，不会出错，但加引号后就完全不用管它了。

### 行尾的换行符 —— 这是误粘贴，删掉

命令末尾若出现换行符的字面表示（两个反斜杠 n 那种），多半是从别处复制时带进来的，**不是命令的一部分**，敲命令时不要带上，否则 shell 会把它当成一个奇怪的参数传给 pprof。

> 一句话总结：**给 URL 加双引号**，问号、等号、误粘贴的烦恼一次性消失。

---

## 3. 逐条详解你的命令

> 下面每条都按「你写的原始命令 → 等价的推荐写法 → 逐部分含义 → 它干了什么」来讲。
> 注意：你的 pprof 端点在 **`8008`（业务端口）** 上，说明当前是把 pprof 挂在了业务 engine 上（对应 `01` 文档的「方式 B」）。生产环境这样暴露有安全风险，建议读 `01` 文档第 3 节考虑独立端口方案；但作为命令讲解，这里完全沿用你的 8008。

### 命令 ①：在线采集 + 进入命令行

```bash
# 你的写法（问号、等号做了反斜杠转义）
go tool pprof http://localhost:8008/debug/pprof/profile?seconds=30

# 推荐写法（加引号）
go tool pprof "http://localhost:8008/debug/pprof/profile?seconds=30"
```

逐部分：

```
go tool pprof          # 调用 pprof 分析器
http://localhost:8008  # 目标服务地址（你的业务端口 8008）
/debug/pprof/profile   # CPU profile 端点
?seconds=30            # 采样时长 30 秒
```

**它干了什么**：向 `8008` 的服务现场采集 **30 秒 CPU profile**，下载完进入 `(pprof)` 交互提示符。接着可以敲：

```
(pprof) top           # 看最耗 CPU 的函数
(pprof) top -cum      # 按累计耗时排序
(pprof) list 函数名   # 看某函数逐行耗时
(pprof) web           # 生成调用图 SVG（需 graphviz）
(pprof) quit          # 退出
```

> 采集这 30 秒里，服务必须有真实负载，否则采到的是空闲。

### 命令 ②：Web UI 分析本地文件（`-http=localhost:8082` 写法）

```bash
go tool pprof -http=localhost:8082 /Users/xiaozhaofu/pprof/pprof.mygin.samples.cpu.003.pb.gz
```

逐部分：

```
go tool pprof                                  # 分析器
-http=localhost:8082                           # 开 Web UI，只监听本机 8082
/Users/.../pprof.mygin.samples.cpu.003.pb.gz   # 本地 profile 文件（见第 4 节它从哪来）
```

**它干了什么**：把这份**已保存的** CPU profile 用网页打开，浏览器自动跳到 `http://localhost:8082`，看火焰图。**不连任何服务**，纯离线分析。

### 命令 ③：同上，但写成 `-http localhost:8082`（空格写法）

```bash
go tool pprof -http localhost:8082 /Users/xiaozhaofu/pprof/pprof.mygin.samples.cpu.003.pb.gz
```

**与命令 ② 完全等价**。区别只是 flag 的写法：

- `-http=localhost:8082` —— 等号连接（flag 与值之间用等号）
- `-http localhost:8082` —— 空格连接（flag 与值之间用空格）

Go 的 flag 解析两种都支持，效果一样。

> 小建议：**优先用 `-http=localhost:8082`（等号）**。空格写法在某些情况下（值以减号开头、或后面紧跟位置参数时）更容易引起歧义；等号写法永远不会被误解析。`localhost` 只绑定本机，比 `:8082` 直接监听所有网卡更稳妥。

### 命令 ④：Web UI 直接分析在线端点

```bash
# 你的写法
go tool pprof -http localhost:8082 http://localhost:8008/debug/pprof/profile?seconds=30

# 推荐写法
go tool pprof -http=localhost:8082 "http://localhost:8008/debug/pprof/profile?seconds=30"
```

逐部分：

```
go tool pprof          # 分析器
-http=localhost:8082   # 开 Web UI，只监听本机 8082
http://localhost:8008/debug/pprof/profile?seconds=30   # 在线采集 30s CPU
```

**它干了什么**：现场采集 30 秒 CPU profile，采完**直接用网页打开**火焰图。= 命令 ① 的采集 + 命令 ② 的网页，一步到位。**最常用的一条**。

### 命令 ⑤：用 curl 把 profile 下载成文件

```bash
# 你的写法
curl -o cpu.pprof http://127.0.0.1:8008/debug/pprof/profile?seconds=30

# 推荐写法
curl -o cpu.pprof "http://127.0.0.1:8008/debug/pprof/profile?seconds=30"
```

逐部分：

```
curl                   # 通用下载工具（不是 pprof）
-o cpu.pprof           # 把响应保存为 cpu.pprof
http://127.0.0.1:8008/debug/pprof/profile?seconds=30   # 采集 30s CPU
```

**它干了什么**：把 30 秒 CPU profile **存成本地文件 `cpu.pprof`**，但**不分析**。好处是把「采集」和「分析」解耦——采一次，反复分析、归档、对比、或拿去做 PGO。

### 命令 ⑥：Web UI 分析刚下载的文件

```bash
go tool pprof -http=localhost:8082 cpu.pprof
```

**它干了什么**：把命令 ⑤ 下载的 `cpu.pprof` 用网页打开。= 命令 ⑤ + ⑥ 组合，等价于命令 ④，只是中间多了一个可归档的文件。

> 命令 ⑤ + ⑥ 的组合 vs 命令 ④ 的取舍：
> - 要**反复看 / 归档 / 做 PGO** → 用 ⑤ 先存文件，再 ⑥ 分析。
> - 只想**看一眼** → 用 ④ 一步到位。

### 命令 ⑦：这条会失败——目标不是 pprof 端点

```bash
go tool pprof --seconds 20 http://127.0.0.1:8008/api/v1/ping
```

逐部分：

```
go tool pprof          # 分析器
--seconds 20           # 基于时间的采集，20 秒（--seconds 与 -seconds 等价，单双横线都行）
http://127.0.0.1:8008/api/v1/ping   # ← 问题在这里：这是业务接口，不是 pprof 端点
```

**为什么会失败**：`go tool pprof` 期望数据源返回 **pprof 格式（protobuf）的 profile 数据**。`/api/v1/ping` 是你的**业务接口**，返回的是 JSON / 文本，不是 profile。pprof 拿到后无法解析，会报类似 `parsing profile: unrecognized profile format` 或 `failed to fetch any source profiles`。

**常见误解**：以为 `--seconds 20 .../api/v1/ping` 是「对 ping 接口压测/观察 20 秒」。**不是**。`go tool pprof` 自己**不会压测、不会调用业务接口产生负载**，它只是去**读取一个 profile 数据源**。`--seconds` 只是告诉它「这是个持续 N 秒的采样端点」，端点本身必须是 `profile` / `trace` 这类。

**你大概想做的两件事之一，正确写法**：

```bash
# 想法 A：采 20 秒 CPU profile（目标改成 /debug/pprof/profile）
go tool pprof --seconds 20 "http://127.0.0.1:8008/debug/pprof/profile"

# 想法 B：一边给 ping 接口压测、一边采样观察这 20 秒服务表现
#   终端 1：持续压测 ping（需要 hey/wrk 这类工具，pprof 不做这事）
hey -z 20s -c 50 "http://127.0.0.1:8008/api/v1/ping"
#   终端 2：同时采 20 秒 CPU profile
go tool pprof -http=localhost:8082 --seconds 20 "http://127.0.0.1:8008/debug/pprof/profile"
```

> 记住：**pprof 的数据源永远指向 `/debug/pprof/...`，不要指向业务接口。**

### 命令⑦延伸：`-seconds` 标志完全详解

既然命令 ⑦ 用到了 `--seconds`，这里把这个标志彻底讲清——它本身很有用，只是别配错数据源。

**它是真实标志**。本机 `go tool pprof -help` 实测可见 `-seconds` 与 `-timeout` 两个相关标志：分别是「基于时间的采集时长」和「采集的超时秒数」。

- `-seconds`（单横线）与 `--seconds`（双横线）**等价**，Go 的 flag 解析两者通吃。
- 它最终等同于给端点 URL 加 `?seconds=N`，**二选一即可，别重复写**：

```bash
go tool pprof --seconds 20 "http://127.0.0.1:8008/debug/pprof/profile"
go tool pprof "http://127.0.0.1:8008/debug/pprof/profile?seconds=20"
```

**核心：`seconds=N` 对不同端点行为不同**（依据 `net/http/pprof` 官方文档）：

| 端点 | `seconds=N` 的行为 |
| --- | --- |
| cpu（`profile`）、`trace` | 按 N 秒**时长采集**（采样这段时间） |
| heap、allocs、goroutine、block、mutex、threadcreate | 返回 **delta（增量）profile**，即「这 N 秒内的变化量」 |

所以有一个很实用的用法——**排查随时间增长**，不用手动采两次再 `-base` 对比，一条命令直接拿到增量：

```bash
go tool pprof -http=localhost:8082 -seconds 30 "http://127.0.0.1:8008/debug/pprof/heap"
go tool pprof -seconds 20 "http://127.0.0.1:8008/debug/pprof/goroutine"
```

补充三点：

- CPU（`profile`）端点不指定时**默认采 30 秒**，`-seconds` 用来覆盖这个默认值。
- 采集时长较长时（如 `-seconds 120`），用 `-timeout <秒>` 放宽客户端超时，避免提前断开。
- `-seconds` **只对在线 URL 有意义**；分析本地文件（如 `cpu.pprof`）时数据已固定，加它无作用。

---

## 4. `~/pprof/` 自动保存目录

你那个文件路径 `/Users/xiaozhaofu/pprof/pprof.mygin.samples.cpu.003.pb.gz` 是怎么来的？—— 它是 **`go tool pprof` 在线采集时自动存的副本**。

### 规则

每当你用 `go tool pprof <在线URL>` 采集，pprof 会**自动**把下载到的原始 profile 存一份到 **`$HOME/pprof/`** 目录，方便事后离线复看。文件名按固定格式生成：

```
pprof . mygin . samples.cpu . 003 . pb.gz
  │      │         │            │     │
  │      │         │            │     └─ 扩展名：protobuf + gzip 压缩
  │      │         │            └─────── 序号：第 003 次采集（每采一次自增）
  │      │         └──────────────────── 采样类型：samples/cpu = CPU 采样
  │      └────────────────────────────── 被采样的二进制名：mygin（你的程序名）
  └───────────────────────────────────── 固定前缀 pprof
```

从文件名能直接读出：这是程序 `mygin` 的**第 3 次 CPU profile** 采集结果。

### 不同 profile 类型的命名

```
pprof.mygin.samples.cpu.NNN.pb.gz                       # CPU
pprof.mygin.alloc_objects.alloc_space...NNN.pb.gz       # heap（名字更长）
pprof.mygin.goroutine.NNN.pb.gz                         # goroutine
```

### 实用技巧

```bash
# 列出历史采集，按时间倒序——快速找回"上次采的那份"
ls -lt ~/pprof/

# 直接复看最近一次采集（无需重新采）
go tool pprof -http=localhost:8082 "$(ls -t ~/pprof/*.pb.gz | head -1)"

# 对比上上次和最近一次（看增量/回归）
go tool pprof -http=localhost:8082 -base ~/pprof/pprof.mygin.samples.cpu.002.pb.gz \
                                ~/pprof/pprof.mygin.samples.cpu.003.pb.gz
```

> 注意：本项目 `pprof/scripts/collect.sh` 是把产物存到项目内 `pprof/profiles/`；而 `go tool pprof` 直连在线 URL 时存到 `~/pprof/`。两者不冲突，知道各自位置即可。

---

## 5. 类似用法大全（按场景）

### 5.1 换数据源类型（把 `profile` 换成别的端点）

```bash
# CPU（30s）
go tool pprof -http=localhost:8082 "http://localhost:8008/debug/pprof/profile?seconds=30"
# 堆内存（瞬时）
go tool pprof -http=localhost:8082 "http://localhost:8008/debug/pprof/heap"
# 累计分配
go tool pprof -http=localhost:8082 "http://localhost:8008/debug/pprof/allocs"
# goroutine
go tool pprof -http=localhost:8082 "http://localhost:8008/debug/pprof/goroutine"
# 阻塞 / 锁竞争（需服务端开了采样率）
go tool pprof -http=localhost:8082 "http://localhost:8008/debug/pprof/block"
go tool pprof -http=localhost:8082 "http://localhost:8008/debug/pprof/mutex"
```

### 5.2 Web UI 端口与绑定地址

```bash
go tool pprof -http=:8082 cpu.pprof          # 监听所有网卡的 8082
go tool pprof -http=localhost:8082 cpu.pprof # 只绑本地（更安全，推荐）
go tool pprof -http=127.0.0.1:0 cpu.pprof    # 端口写 0 = 随机空闲端口
```

### 5.3 不开网页，直接出结果（适合脚本 / 无图形界面的线上机）

```bash
go tool pprof -top cpu.pprof                 # 直接打印 top（不进交互、不开网页）
go tool pprof -top -nodecount=20 cpu.pprof   # 只看前 20 个
go tool pprof -list='HandlerName' cpu.pprof  # 直接打印某函数逐行耗时（正则匹配）
go tool pprof -tree cpu.pprof                # 文本调用树
go tool pprof -peek='Marshal' cpu.pprof      # 看某函数的调用者/被调用者
```

### 5.4 导出静态图片 / 数据

```bash
go tool pprof -svg cpu.pprof > cpu.svg       # 调用图导出 SVG（需 graphviz）
go tool pprof -png cpu.pprof > cpu.png       # 导出 PNG（需 graphviz）
go tool pprof -proto a.pprof b.pprof > m.pgo # 合并多份为一份（PGO 用，见 02 文档）
go tool pprof -raw cpu.pprof                 # 打印原始采样文本
```

### 5.5 聚焦 / 过滤（火焰图太杂时缩小范围）

```bash
go tool pprof -http=localhost:8082 -focus='gin_api' cpu.pprof # 只看匹配的调用栈
go tool pprof -http=localhost:8082 -ignore='runtime' cpu.pprof     # 忽略匹配的
go tool pprof -http=localhost:8082 -hide='gc' cpu.pprof            # 隐藏匹配节点
```

### 5.6 对比两份 profile（优化前后 / 排查内存增长）

```bash
# -base：显示 new 相对 old 的「增量」（正=增加，负=减少）
go tool pprof -http=localhost:8082 -base old.pprof new.pprof
# -diff_base：与 -base 类似，常用于内存对比
go tool pprof -http=localhost:8082 -diff_base heap1.pprof heap2.pprof
```

### 5.7 heap 的四种观察视角（采样维度）

```bash
go tool pprof -http=localhost:8082 -inuse_space  heap.pprof   # 当前占用字节（找常驻大户，默认）
go tool pprof -http=localhost:8082 -inuse_objects heap.pprof  # 当前存活对象数
go tool pprof -http=localhost:8082 -alloc_space  heap.pprof   # 累计分配字节（找 GC 压力源）
go tool pprof -http=localhost:8082 -alloc_objects heap.pprof  # 累计分配对象数
# Web UI 里也可在右上角 SAMPLE 下拉切换这四种
```

### 5.8 trace（执行跟踪，专用查看器，不是 pprof）

```bash
# trace 用 go tool trace 看，不是 go tool pprof
curl -o trace.out "http://localhost:8008/debug/pprof/trace?seconds=5"
go tool trace trace.out      # 打开调度/GC/网络时间线
```

### 5.9 二进制 + profile（符号缺失时补符号）

```bash
# 当 profile 里符号不全（如 strip 过的二进制远程采集），用本地二进制补符号
go tool pprof -http=localhost:8082 ./bin/gin_api cpu.pprof
```

---

## 6. 交互模式命令速查

进入 `go tool pprof <source>`（不带 -http）后，`(pprof)` 提示符下可用：

| 命令 | 作用 |
| --- | --- |
| `top` / `topN` | 按 flat 自身耗时排前 N |
| `top -cum` | 按 cum 累计耗时排序 |
| `list 正则` | 某函数源码逐行标注耗时（最常用，直达代码行） |
| `web` | 浏览器打开调用图 SVG（需 graphviz） |
| `weblist 正则` | 浏览器打开带耗时标注的源码 |
| `peek 正则` | 看调用者 / 被调用者 |
| `tree` | 文本调用树 |
| `traces` | 列出采样到的调用栈 |
| `focus= / ignore= / hide=` | 交互式过滤 |
| `granularity=lines` | 把粒度从函数细化到行 |
| `svg` / `png` | 导出图片 |
| `o` / `options` | 查看 / 修改当前选项 |
| `help` | 帮助 |
| `quit` / `q` | 退出 |

> flat vs cum：**flat** = 函数自身耗时（不含子调用），定位「这个函数本身慢」；**cum** = 含子调用的累计耗时，定位「慢在它下游」。

---

## 7. 常见报错与排查

| 报错 / 现象 | 原因 | 解决 |
| --- | --- | --- |
| `zsh: no matches found` | 问号被 zsh 当通配符 | URL 加双引号，或反斜杠转义问号 |
| `parsing profile: unrecognized profile format` | 数据源不是 pprof 格式（如指向了 `/api/v1/ping`） | 目标改为 `/debug/pprof/profile` 等真正的 pprof 端点（见命令 ⑦） |
| `failed to fetch any source profiles` | URL 不通 / 端口错 / 服务没开 pprof | 确认服务在跑、端口对、pprof 已启用；`curl` 该 URL 验证 |
| `connection refused` | 端口不对，或 pprof 绑了 `127.0.0.1` 而你从外部连 | 核对端口；远程用 SSH 端口转发（见 `01` 文档第 6 节） |
| Web UI 打开了但 `Graph` 视图空白 / 报 graphviz | 没装 graphviz | `brew install graphviz`；或改看内置的 Flame Graph（不依赖 graphviz） |
| `profile is empty` / top 全是 runtime | 采集时服务在空闲 | 采集期间施加真实/压测负载 |
| Web UI 端口 `8082` 被占用 | 端口冲突 | 换端口 `-http=localhost:8083`，或用 `-http=127.0.0.1:0` 随机端口 |
| CPU profile 采了但只有几秒就返回 | 服务端给 pprof 设了写超时 | 别给 pprof server 设 WriteTimeout（见 `01` 文档接入代码注释），或用独立端口方案 |

---

相关文档：接入与采集看 [`01-pprof性能调优指南.md`](./01-pprof性能调优指南.md)，编译期优化看 [`02-PGO优化指南.md`](./02-PGO优化指南.md)。
