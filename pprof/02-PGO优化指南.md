# PGO 性能引导优化指南（gin_api）

> PGO = Profile-Guided Optimization（基于性能剖析数据的编译优化）。
> 它把 pprof 采集的 **CPU profile** 喂给 Go 编译器，让编译器针对程序的**真实热路径**做更激进的优化（内联、去虚拟化等）。
> 本文覆盖「是否需要 → 怎么做 → 各种场景 → 坑」，所有命令贴合本项目（Go 1.27、`sonic` 标签、根目录 main 包、`make build` 构建）。

---

## 目录

1. [PGO 是什么 / 原理 / 收益](#1-pgo-是什么--原理--收益)
2. [本项目是否需要 PGO（结论先行）](#2-本项目是否需要-pgo结论先行)
3. [前置条件](#3-前置条件)
4. [标准流程（五步）](#4-标准流程五步)
5. [与本项目构建链路结合](#5-与本项目构建链路结合)
6. [常见场景全覆盖](#6-常见场景全覆盖)
7. [度量 PGO 收益](#7-度量-pgo-收益)
8. [注意事项与坑](#8-注意事项与坑)
9. [命令速查表](#9-命令速查表)

---

## 1. PGO 是什么 / 原理 / 收益

### 原理

普通编译时，编译器对「哪个函数是热点、哪个分支常走」一无所知，只能用启发式规则（比如函数体小于某阈值才内联）。PGO 改变了这一点：

```
线上真实流量  →  pprof 采集 CPU profile  →  命名为 default.pgo 放 main 包目录
                                              ↓
                              go build 自动读取 → 编译器据此优化：
                                • 更激进地内联热点函数
                                • 去虚拟化（把接口调用 devirtualize 成具体类型直接调用）
                                • 更好的代码布局 / 寄存器分配
```

### 收益

- Go 官方文档截至 Go 1.22 的代表程序数据：PGO 构建约有 **2%~14%** 性能提升；本项目实际收益必须用 benchmark 或端到端压测确认。
- 收益来源是「热路径」，所以**热点越集中、接口调用越多的服务收益越明显**；纯 IO 等待型服务收益小。
- PGO 是**纯编译期**优化：不改一行业务代码、不增加运行时开销、构建可重现。

### 与 pprof 的关系

| | pprof | PGO |
| --- | --- | --- |
| 角色 | 诊断工具，人工定位并优化热点 | 自动优化，编译器据 profile 调优 |
| 输入 | 运行中的服务 | pprof 采集的 CPU profile |
| 产出 | 火焰图 / top 列表 | 更快的二进制 |
| 关系 | **先用 pprof 手动优化明显热点** | **再用 PGO 榨取编译器级增量** |

> 顺序建议：先靠 `01-pprof性能调优指南.md` 把算法级、明显的热点优化掉；PGO 是锦上添花的「最后一公里」，不要指望它替代手动优化。

---

## 2. 本项目是否需要 PGO（结论先行）

**结论：建议做，性价比高。**

判断依据（对照本项目）：

| 判断维度 | 本项目情况 | 是否利好 PGO |
| --- | --- | --- |
| 是否长期运行的服务 | 是（Gin HTTP 服务 + 后台 worker） | ✅ 长期运行最划算 |
| 是否有稳定热路径 | 是（鉴权中间件、JSON 编解码、DB/Redis 访问、LLM/订单处理每请求都走） | ✅ 热点集中 |
| 接口调用是否多 | 多（模块化分层、大量 interface 注入，见 `modules.go`） | ✅ 去虚拟化收益大 |
| Go 版本是否支持 | 1.27（`go.mod`），远超 PGO 要求的 1.21 | ✅ 完全支持 |
| 接入成本 | 仅需放一个 `default.pgo` 到根目录，构建命令不用改 | ✅ 极低 |
| 风险 | 几乎为零（不改业务代码，可随时 `-pgo=off` 回退） | ✅ 低风险 |

**什么情况下可以先不做**：服务还在频繁重构、热路径尚未稳定时，profile 容易过期，可等架构稳定后再做。但即便如此，PGO 对 profile 陈旧有较好鲁棒性（见第 8 节），所以「早做」也无妨。

---

## 3. 前置条件

1. **pprof 已开启**：PGO 的 profile 来自 pprof 的 CPU profile 端点。端点已内置，把 `pprof.enabled` 配成 `true` 即可（见 `01` 文档第 3 节）。
2. **能拿到代表性负载**：线上真实流量最佳；没有线上就用压测（`wrk`/`hey`）或 benchmark（见场景 D）。
3. **Go 1.21+**：本项目 1.27，满足。

---

## 4. 标准流程（五步）

> 配套脚本 `scripts/pgo-merge.sh` 已封装第 1~2、4 步，这里同时给出脚本和等价原生命令。

### 第 1 步：采集代表性 CPU profile

```bash
# 推荐：分多次采集，平滑偶然偏差（每次 30s，共 3 份）
PPROF_ADDR=127.0.0.1:6060 ./pprof/scripts/pgo-merge.sh collect 3 30

# 等价原生命令（采一份）
curl -o cpu1.pprof "http://127.0.0.1:6060/debug/pprof/profile?seconds=30"
```

> 关键：采集期间服务必须承载**真实/代表性**负载。idle 状态采到的 profile 对 PGO 毫无意义。

### 第 2 步：合并多份为一份

```bash
# 脚本：合并 profiles/pgo/ 下所有 *.pprof
./pprof/scripts/pgo-merge.sh merge

# 等价原生命令：-proto 输出合并后的标准格式
go tool pprof -proto cpu1.pprof cpu2.pprof cpu3.pprof > default.pgo
```

### 第 3 步：命名并放到 main 包目录

PGO 的默认约定文件名是 **`default.pgo`**，必须放在 **main 包所在目录**。本项目 `main.go` 在**项目根目录**，所以：

```
gin_api/
├── main.go          ← main 包在这里
├── default.pgo      ← 因此 default.pgo 放这里（脚本已自动放好）
├── go.mod
└── ...
```

### 第 4 步：构建（自动启用，命令不用改）

`go build` 默认就是 `-pgo=auto`：自动在 main 包目录查找 `default.pgo`，找到即启用。**本项目现有构建命令完全不用改**：

```bash
# 本地开发构建
go build -tags="sonic" .

# 生产构建（Makefile 现有命令，default.pgo 在根目录会被自动识别）
make build
```

### 第 5 步：验证 PGO 真的生效

```bash
# 脚本：一键校验 default.pgo 有效性 + 编译器是否收到 -pgoprofile
./pprof/scripts/pgo-merge.sh verify

# 等价原生命令：在编译日志里找 -pgoprofile 标志
go build -a -x -tags="sonic" -o /dev/null . 2>&1 | grep pgoprofile
# 有输出（形如 -pgoprofile=$WORK/b001/...）即说明 PGO 已启用
```

---

## 5. 与本项目构建链路结合

### 现有构建链路

- **本地/部署机构建**：`make build`
  默认执行 `CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath`，追加现有 `BUILD_FLAGS`
  中的 sonic tag、`-s -w` 和构建元数据，输出到 `bin/$(APP_NAME)`。可覆盖完整 `Linux` 或 `BUILD_FLAGS` 变量。
  编译成功后，UPX 存在则执行 `upx -9`，缺失则提示并保留未压缩产物；压缩失败仍报错。
- **CI**：两条流水线都只跑门禁、不做构建/发布——`.github/workflows/check.yml`
  （用 `go-version-file: go.mod` 取 Go 版本）与 `.gitlab-ci.yml` 的 `check` 阶段，
  内容都是 `make check`。

### PGO 融入点

因为 `default.pgo` 在根目录会被 `go build` 自动识别，所以：

- `make build` **零改动**即带上 PGO（只要根目录有 `default.pgo`）。
- 交叉编译不受影响：CPU profile 记录的是**函数级**调用信息，与 `GOARCH/GOOS` 无关。在 mac 上采集的 profile 可用于 linux 构建（但仍建议用代表生产负载的 profile）。
- `upx` 压缩在 PGO 优化**之后**执行，二者不冲突。

### 把 default.pgo 纳入版本控制（推荐）

PGO 官方最佳实践是**把 `default.pgo` 提交进仓库**，好处：

- 任何人 `git clone` 后 `make build` 都自动享受 PGO，构建可重现。
- 不受 Markdown 忽略规则影响（`default.pgo` 不是 md）。

```bash
git add default.pgo
# 注意：提交需你本轮明确授权，本文档不代为提交
```

---

## 6. 常见场景全覆盖

### 场景 A：本地快速试一把（最快上手）

没有线上流量，想先看 PGO 效果：

```bash
# 1) 启动服务（dev，确保 pprof.enabled=true）
make dev
# 2) 另开终端用压测工具打热点接口，边打边采
hey -z 40s -c 50 http://127.0.0.1:8080/api/v1/<热点接口>   # 持续 40s 施压
./pprof/scripts/pgo-merge.sh collect 1 30                  # 同时采 30s
# 3) 合并 + 构建 + 验证
./pprof/scripts/pgo-merge.sh merge
./pprof/scripts/pgo-merge.sh verify
```

### 场景 B：从生产采集 profile（最具代表性，推荐）

生产 pprof 绑 `127.0.0.1`，先 SSH 端口转发（见 `01` 文档第 6 节）：

```bash
# 1) 转发线上 6060 到本地 16060
ssh -N -L 16060:127.0.0.1:6060 user@prod-host &
# 2) 在业务高峰期采集多份（覆盖代表性负载）
PPROF_ADDR=127.0.0.1:16060 ./pprof/scripts/pgo-merge.sh collect 5 30
# 3) 合并 → default.pgo → 提交 → make build
./pprof/scripts/pgo-merge.sh merge
```

> 经验：在**业务高峰 / 典型负载**时段采集，覆盖真实的接口分布。避免只采单一接口压测的 profile（会让 PGO 偏向那一个接口）。

### 场景 C：CI/CD 集成

本项目 CI 当前只跑门禁、不构建（见上一节）。两种推进方式：

**方式 1（当前最简单）**：把 `default.pgo` 提交进仓库，构建始终在 `make build`（部署机/手动）执行，自动带 PGO。无需改 CI。

**方式 2（将来加 build 阶段时）**：在 `.gitlab-ci.yml` 增加构建作业，profile 已随仓库存在，`go build` 自动启用。示意（**需按实际补全，勿直接照搬**）：

```yaml
# 仅示意：将来若新增 build stage，PGO 无需额外配置，
# 因为 default.pgo 已在仓库根目录，go build 自动识别。
build:
  stage: build
  image: golang:1.27
  script:
    - make build        # default.pgo 在根目录 → 自动 PGO
  artifacts:
    paths:
      - bin/gin_api
```

**方式 3（进阶，profile 不入库）**：CI 从对象存储 / profile 服务拉取最新生产 profile 放到根目录再构建。适合 profile 更新频繁、不想频繁提交二进制 profile 的团队。本项目当前规模用方式 1 即可。

### 场景 D：没有运行环境，用 benchmark 的 profile 做 PGO

完全没有可施压的运行环境时，可用 benchmark 产出的 CPU profile（代表性弱于真实流量，聊胜于无）：

前提：本仓目前**没有 benchmark**，这条路径要先为目标包补一个 `func BenchmarkXxx`。

```bash
# 先为热点包写好 benchmark，再对该包采样（<包路径> 换成你补了 benchmark 的包）
./pprof/scripts/bench-profile.sh <包路径>
# 把产出的 cpu.pprof 当作 PGO profile
cp pprof/profiles/bench-*/cpu.pprof default.pgo
go build -a -x -tags="sonic" -o /dev/null . 2>&1 | grep pgoprofile
```

> 局限：benchmark 只覆盖被测包，不代表整体请求分布。**优先用真实流量**，benchmark profile 仅作兜底。

### 场景 E：临时禁用 / 显式指定 profile

```bash
# 临时禁用 PGO（即使根目录有 default.pgo 也不用）
go build -pgo=off -tags="sonic" .

# 显式指定某个 profile 文件（不依赖 default.pgo 命名约定）
go build -pgo=/path/to/some.pprof -tags="sonic" .

# 显式启用 auto（默认值，一般无需写）
go build -pgo=auto -tags="sonic" .
```

### 场景 F：profile 该多久更新一次

- 代码热路径**显著变化**后（大重构、新增高频接口）应重新采集。
- 常规迭代：每隔几个版本或每季度刷新一次即可，PGO 对轻微陈旧有鲁棒性（见第 8 节）。
- 把「刷新 default.pgo」作为一个低频例行任务，而非每次发版都做。

### 场景 G：仓库里的其它 main 包

`default.pgo` 只对**它所在目录的 main 包**生效。本仓当前只有根目录一个 main 包
（`main.go`），因此根目录那份 `default.pgo` 就是全部。

将来若新增独立的 main 包（工具、示例程序），它们不会被根目录的 `default.pgo` 影响；
确有需要时各自目录单独放一份。

### 场景 H：多环境差异

不同环境（dev/test/prod）负载特征不同。PGO 应使用**生产**负载的 profile（场景 B）。dev/test 采的 profile 仅用于「试通流程」，不要拿去做生产构建的 PGO 依据。

---

## 7. 度量 PGO 收益

不要凭感觉，用数据验证 PGO 是否真带来收益：

```bash
# —— 方式 1：benchmark 前后对比（最严谨）——
# 1) 不带 PGO 跑基准
go test -pgo=off -tags="sonic" -run='^$' -bench=. -benchmem -count=10 \
  ./internal/pkg/encoding > old.txt
# 2) 带 PGO 跑基准——必须用 -pgo=<绝对路径> 显式指向 default.pgo！
#    注意：go test 跑非 main 子包时，-pgo=auto 只在被构建包目录里找 default.pgo，
#    不会自动捡起项目根目录的那份；写 -pgo=auto 等于没开 PGO，测不出效果。
go test -pgo="$(pwd)/default.pgo" -tags="sonic" -run='^$' -bench=. -benchmem -count=10 \
  ./internal/pkg/encoding > new.txt
# 3) 用 benchstat 比较（go install golang.org/x/perf/cmd/benchstat@latest）
benchstat old.txt new.txt
```

```bash
# —— 方式 2：线上/压测端到端对比 ——
# 分别用「带 PGO」与「-pgo=off」构建的二进制，
# 用相同压测参数（wrk/hey）打同一接口，对比 QPS、P50/P99、CPU%。
```

> 关注指标：CPU 使用率下降、QPS 上升、尾延迟（P99）下降。若多次测量无统计显著差异，说明该服务热点不在可被 PGO 优化的部分，属正常。

---

## 8. 注意事项与坑

1. **profile 必须是 CPU profile**：PGO 只吃 `/debug/pprof/profile`（CPU）。heap/goroutine/block/mutex **不能**用于 PGO。
2. **必须放对位置和名字**：`default.pgo` 必须在 main 包目录（本项目根目录）且文件名精确。放错位置不会报错，但**静默不生效**——务必用 `verify` 确认。
3. **采集要有代表性负载**：idle 期采的 profile 等于没采，甚至误导优化。
4. **profile 陈旧的鲁棒性**：Go 的 PGO 设计为对「profile 与当前源码不完全匹配」鲁棒——函数改名/删除只会让那部分失去优化，不会让构建失败或变慢。可放心使用稍旧的 profile，但显著重构后应刷新。
5. **构建会变慢一点**：PGO 让编译器做更多分析，**编译时间略增**（通常可接受），运行时无额外开销。
6. **可重现**：相同源码 + 相同 `default.pgo` → 相同优化结果，不引入非确定性。
7. **与 `-ldflags "-s -w"`、`upx`、`sonic` 标签都兼容**：本项目现有构建参数与 PGO 不冲突。
8. **不要把 PGO 当银弹**：它是 2%~14% 的增量优化，替代不了算法/架构层面的手动优化。先 pprof 手动优化，再 PGO。
9. **race/功能正确性不受影响**：PGO 不改变程序语义。但发版前仍应按项目规范跑 `make check`（含 `go test -race`）。

---

## 9. 命令速查表

```bash
# ——— 采集 + 合并 + 校验（脚本）———
./pprof/scripts/pgo-merge.sh collect 3 30   # 采 3 份各 30s
./pprof/scripts/pgo-merge.sh merge          # 合并为 项目根/default.pgo
./pprof/scripts/pgo-merge.sh verify         # 校验有效性 + 是否启用
./pprof/scripts/pgo-merge.sh clean          # 清理 pprof/profiles/pgo/ 下的临时 profile

# ——— 合并（原生）———
go tool pprof -proto a.pprof b.pprof c.pprof > default.pgo

# ——— 构建 ———
make build                                  # 自动 PGO（default.pgo 在根目录）
go build -tags="sonic" .                    # 同上，自动 auto
go build -pgo=off -tags="sonic" .           # 临时禁用 PGO
go build -pgo=/path/x.pprof -tags="sonic" . # 显式指定 profile

# ——— 验证启用 ———
go build -a -x -tags="sonic" -o /dev/null . 2>&1 | grep pgoprofile

# ——— 度量收益 ———
go test -pgo=off               -bench=. -count=10 ./pkg > old.txt
go test -pgo="$(pwd)/default.pgo" -bench=. -count=10 ./pkg > new.txt  # 子包须显式指向 default.pgo，勿用 auto
benchstat old.txt new.txt
```

---

至此，pprof（诊断）+ PGO（自动优化）的完整工具链就绪。
日常排查看 `01-pprof性能调优指南.md`，逐条命令含义看 `03-go-tool-pprof命令详解.md`。
