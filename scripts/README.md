# scripts

项目质量检查脚本目录。

## load-api.go

独立 API 负载工具，使用 `//go:build ignore`，不进入产品 `./...` 或默认覆盖率。先将请求场景保存为 JSON 数组，例如 `[{"method":"GET","path":"/health","weight":1}]`，再显式运行：

```bash
GOTOOLCHAIN=local go run scripts/load-api.go -target http://127.0.0.1:8008 -scenario scenario.json -rate 20 -concurrency 4 -duration 10s -timeout 5s
GOTOOLCHAIN=local go test -vet=off -count=1 scripts/load-api.go scripts/load-api_test.go
GOTOOLCHAIN=local go build -o .cache/load-api scripts/load-api.go
```

默认只允许 loopback，localhost 固定拨号到 127.0.0.1；远程需显式 `-allow-remote`，且应取得目标所有者许可。工具不跟随重定向、不使用环境代理。支持场景 `method/path/weight/headers/body`，不打印正文、凭据或完整 URL。文件≤1 MiB、场景≤64、每正文≤64 KiB、头≤32；并发≤4096、到达率≤100000/s、时长≤24h、单请求超时≤1min，均须为正。超出 1 MiB 的响应按传输失败记录并关闭。

`-duration` 默认 10s，限定发压窗口；窗口结束停止派发，已发出的请求按各自 `-timeout`（默认 5s）继续收尾，额外等待最多为一个请求超时。调用方取消或收到退出信号仍会立即取消请求。`elapsed_seconds` 包含收尾，可能大于 duration。单请求超时仍计为传输失败；自然结束发压不会提前截断请求或响应体。

输出保留 offered/sent/completed/generator_dropped、状态码、传输失败和从计划发送时刻计算的固定桶延迟 P95/P99 上界；越界分位明确标注 overflow。新增 `scheduler_dropped` 记录发生器调度落后漏过的计划发送，`worker_dropped` 记录派发时没有空闲 worker 的请求；二者之和等于 generator_dropped，且 offered = sent + generator_dropped。completed 包括成功和失败的已结束请求，收尾后等于 sent。不建立等待队列；必须结合丢弃率读分位，不能把低延迟当作服务容量。worker 满载也可能由下游变慢造成，不能只归因于并发参数。旧字段保留，但严格拒绝未知 JSON 字段的消费者需接受新增字段。场景权重按固定顺序轮转；超载丢弃可能改变实际发送比例，应以服务端观测核对。

## install-hooks.sh / check-hooks.sh / git-hooks/

`git-hooks/` 下是随仓库分发的 git 钩子（当前只有 `pre-push`，执行 `make check-ci`）。
`install-hooks.sh` 把它们复制到本仓的 git 钩子目录，`check-hooks.sh` 在临时仓库里
实跑验证安装语义与钩子行为。统一入口：

```bash
make hooks        # 安装（幂等；已有非本仓同名钩子时报错中止，不覆盖）
make check-hooks  # 验证（已接入 make check）
```

跳过门禁：`SKIP_GATE=1 git push`，跳过时钩子在 stderr 留痕。

## check-architecture.sh

检查 application、transport/http、repository、worker、internal/pkg 和 GORM context DB 的依赖边界。

```bash
./scripts/check-architecture.sh
```

## check-repository-context.go

架构检查中的 GORM context 子检查，使用 Go AST 和类型信息，加载
`./internal/repository/...` 当前构建条件的生产文件。唯一豁免是本项目底层 `dbtx` 包。

```bash
GOWORK=off go run ./scripts/check-repository-context.go
GOWORK=off go test -vet=off scripts/check-repository-context.go scripts/check-repository-context_test.go
```

核对 `mdbCtx` 的真实实现，并追踪经典 `*gorm.DB` 方法接收者的局部来源：裸字段/参数、
错误 helper、丢失调用方 context、未知来源都失败；有效统一入口、查询链/别名和受管
Transaction 的内联回调句柄可通过。包加载或类型检查失败也会失败，不能当作空结果通过。

所有赋值都参与保守判断；不支持的跨函数来源、动态 Session/Scopes 需保留可直接追踪的
写法。测试文件、其他构建条件、GORM 泛型 API、反射/unsafe 及驱动实际取消行为不在此检查
证明范围内。完整约定见 [数据库规范](../docs/db-patterns.md#数据访问统一经-mdbctxctx)。

## check-capability-inventory.sh

校验 `SCAFFOLD.md` 的预置能力清单是否穷尽。覆盖四处：`internal/pkg/*/`、`pkg/*/`、
`internal/middleware/*.go`、`Config` 结构体的顶层 `mapstructure` 段。

```bash
make check-capability-inventory   # 自检 + 实检（已接入 make check）
./scripts/check-capability-inventory.sh --print-scope   # 打印覆盖范围
./scripts/check-capability-inventory.sh --self-test     # 只跑自检
```

覆盖范围由脚本内的 `SCOPES` 单点定义：`--print-scope` 打印它，扫描循环遍历它，
`--self-test` 也遍历它来为每个 scope 构造一个未登记条目并断言判据失败。三者同源，
因此"声明覆盖 A、实际只扫 B"不会发生。新增 scope 需同时实现 `scope_describe` /
`scope_entries` / `scope_match_kind` / `scope_fixture` 四处，缺一则 `--self-test` 失败。

这是一条**存在性**判据：它只回答"这个名字在清单里出现过吗"，检测不到归类错误或
说明与行为不符。扩大覆盖范围不等于提高检查强度。

## check-doc-refs.go

校验被 git 跟踪的 `*.md` 里的引用是否指向真实路径。两类引用都查：Markdown 行内链接
`[文字](路径)`，以及反引号里的仓内路径（如 `docs/architecture.md`）——后者是本仓真正会腐烂的那一类
（文档表格里的路径大多是裸路径而非链接）。

```bash
make check-doc-refs                                   # 自检 + 实检（已接入 make check）
GOWORK=off go run ./scripts/check-doc-refs.go --print-scope   # 打印覆盖范围
GOWORK=off go run ./scripts/check-doc-refs.go --self-test     # 只跑自检
```

解析不到的引用要么修好，要么在 `doc-refs-allowlist.txt` 里登记并写明所有者与理由；
一条不再命中的登记同样使门禁失败——豁免不得比现实活得久。

这是一条**存在性**判据。它检测不到：引用指向了一个存在但错误的文件、锚点是否真的存在、
不在反引号里的散文式路径提及、以及符号引用（`包路径.符号`）是否仍然存在。
判定"是不是仓内路径"的规则刻意保守（须含内部 `/`，末段带已知扩展名或以 `/` 结尾，
排除绝对路径 / `~` / `$` / 花括号 / glob），因此外部模块名与符号引用不参与判定。

## check-optional-wiring.go

独立验证注释中的可选接线；不属于默认`check`、`check-ci`或CI。工具仅用Go标准库：

```bash
make check-optional-wiring
GOWORK=off go run ./scripts/check-optional-wiring.go -case taskqueue
GOWORK=off go test -vet=off -count=1 -run '^TestOptionalWiring' ./scripts/check-optional-wiring.go ./scripts/check-optional-wiring_test.go
```

固定样例为default、taskqueue、upload、outbound、auditlog、all、removed。每例创建自有临时副本，
只复制白名单源文件和示例配置，按manifest恢复40处wiring标记；主工作区不改接线。
副本不包含实际运行配置、pem、go.work、.git或缓存；白名单内符号链接拒绝，清理核对目录身份。
每例用`-trimpath`构建主程序，再用`go test -c -tags=integration -vet=off`编译cmd/bootstrap/config测试，
不执行任何测试二进制、服务或迁移。子进程固定离线Go环境、清除APP_并设置自有APP_ROOT；
单子进程120秒、单例300秒。结果以JSON记录命令、耗时和退出码；这只是编译证据。

`-case all -negative`仅在副本中损坏一个已恢复的调用，预期构建失败并返回非零，用于检验错误接线不会被放过；不要将它作为正常构建成功入口。

工具单元与真实七例矩阵是两类证据。成本评估要对同一源码做三组冷/热缓存配对，使用自有GOCACHE，
不清用户缓存；没有成本结果和新决定前保持独立入口。当前实现面向本机/POSIX验证，Windows未验收。

## test-coverage.sh / check-coverage.sh

`test-coverage.sh` 运行 race 测试并生成聚合 coverage profile，`check-coverage.sh` 校验最低覆盖率。统一入口：

```bash
make check-coverage
```

当前门禁以 `Makefile` 的 `COVERAGE_MIN` 为单一来源（取值理由见其注释），可通过 `COVERAGE_MIN` 提高但不应降低：

```bash
make check-coverage COVERAGE_MIN=65
```

受限沙箱禁止绑定回环监听端口时，可以显式排除仓库中已确认依赖本地 listener 的测试；正常开发和 CI 不设置此变量，始终运行完整测试：

```bash
ALLOW_NO_LOOPBACK=1 make check-coverage
```

