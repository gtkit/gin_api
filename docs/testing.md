# 测试与验证

## 统一入口

提交前跑：

```bash
make check
```

它依次执行架构传感器、能力清单判据（含自检）、依赖清单整洁、镜像摘要固定、
发布接线、应用身份零漂移、钩子安装语义、`golangci-lint run ./...`、`go vet ./...`、
race 测试并生成聚合 coverage profile，最后校验全仓覆盖率不低于门禁。

**权威清单是 `Makefile` 的 `check` 目标**，本文只做导读。

单项与补充命令：

```bash
make test-race                          # 只跑 race 测试
make test-integration                   # 真实 MySQL 的集成测试（无 DB 时自动 SKIP；跑在哪个库见下）
make check-coverage COVERAGE_MIN=75     # 临时提高覆盖率门禁试跑
go test -run TestName -count=1 ./internal/...
go test -bench=. -benchmem -count=3 ./...
```

受限环境（沙箱禁止绑定回环监听端口）可排除依赖本地 listener 的测试：

```bash
ALLOW_NO_LOOPBACK=1 make check
```

正常开发与 CI 不设置该变量，始终跑完整测试。

### 覆盖率门禁

以 `Makefile` 的 `COVERAGE_MIN` 为**单一来源**（本文不重复该数字——写死会与实际门禁分叉，
而分叉时读者无从判断哪个是真的）。门禁只升不降，提高之前必须先让代码在受限环境也实际达到目标。

**阶段目标：全仓覆盖率不低于 80%。** 这个目标写在这里而门禁当前值不写，两者不矛盾：
目标是方向，很久不变；门禁值随每次抬升而变，因此只能有一处。门禁值去 `Makefile` 看。

门禁取"任意环境都能复现的可移植地板"：受限环境（`ALLOW_NO_LOOPBACK=1`）的实测值是下界，
loopback 可用的完整跑更高。取下界保证两种环境的 `make check` 都通过。

新增和修改的公开行为必须同步增加测试，不得以当前基线为由扩大测试债务。

### 带 tag 的测试口径

`internal/projectinit` 与 `cmd/project.go` 由 `//go:build scaffold`
门控。**测试与覆盖率入口必须带该 tag**，否则项目初始化相关测试不会被编译，其保护会静默消失
而 `make check` 依然全绿。`make` 的相关目标已封装好，直接跑 `make check` 即可。

## 测试分层

| 层 | 覆盖什么 |
|---|---|
| Unit | application 业务规则、值对象、工具函数 |
| Handler | `httptest` 覆盖绑定、鉴权上下文、错误映射、响应结构 |
| Repository / Integration | GORM 查询、事务、锁、schema 一致性 |
| Contract | 公开 API 响应结构、错误码、分页结构、DTO json tag |
| Regression | 修 bug 先补一个能失败的复现用例 |

集成测试用真实MySQL验证repository与schema一致性（配置取自显式隔离APP_ROOT下的运行配置），
**事务内强制回滚、零持久化**，无 DB 时自动 SKIP。

### 哪些包有集成测试

不维护清单——列表会腐烂，而 grep 不会：

```bash
grep -rl '^//go:build integration' --include='*_test.go' internal pkg cmd
```

`make test-integration` 用的就是这条反查，因此**在新包里加集成测试不需要改 Makefile**，
也不会出现"加了但门禁跑不到"。反查出 0 个包时该目标直接失败：本仓必然有集成测试，
扫到空只可能是枚举坏了，而那种坏法的表现是全绿。

每个集成测试文件的头部都写明三件事：**覆盖的轴**（为什么这条断言只有真实依赖能回答）、
**安全设计**（怎么做到零持久化/零残留）、**运行方式**。看文件头比看测试名快。

### 集成测试跑在哪个库上

`make test-integration` 必须指定项目根之外的独立 `APP_ROOT`，先运行无连接的cmd前置检查，再执行完整真实套件。缺少根、项目根/子目录、配置symlink逃逸均失败；不会回落到项目真实配置。MySQL使用隔离文件的`database.name`，它必须是一个**迁移状态与本仓一致**的专用测试库，
否则失败原因是环境而不是代码：少一张表就报 `Table 'xxx' doesn't exist`，
而那条错误看起来和真实缺陷一模一样。

先复制`config/env.yml.example`到独立临时运行目录的`config/env/env.yml`（文件0600），填写本地测试库与本地凭据，并配置临时JWT密钥。不要复制真实生产配置或密钥。插件套件固定连接127.0.0.1:3306/6379，屏蔽继承的APP_覆盖后从该隔离文件加载，使用生成的临时钥与独占namespace；非标准本地端口尚不支持该套件，须先明确调整夹具。其他仓储集成套件仍按该文件读取连接。

一次性准备。建库这步怎么执行取决于 MySQL 怎么连（本机客户端、`docker exec`、GUI），
因此这里只给 SQL：

```sql
CREATE DATABASE gin_api_itest DEFAULT CHARACTER SET utf8mb4 COLLATE utf8mb4_unicode_ci;
```

库默认值取 `utf8mb4` / `utf8mb4_unicode_ci`。迁移建的表**不依赖**它——那些表把
`ENGINE/CHARSET/COLLATE` 逐张写死，刻意不继承服务器默认值。库默认值影响的是
没有显式声明的表：goose 的版本表，以及集成测试自己建的临时表。让两者一致，
就不会出现"同一个库里两种排序规则"这种只在跨表比较时才暴露的差异。

```bash
# 灌迁移（migrate 命令同样认 APP_ 覆盖）
APP_ROOT=/absolute/path/to/isolated-runtime make migrate-up dev

# 每次跑
APP_ROOT=/absolute/path/to/isolated-runtime REQUIRE_DB_INTEGRATION=1 make test-integration
```

**必须是新库**：基线迁移 `20260731000001` 自己 `CREATE TABLE users`，
灌进一个已经有 `users` 的库会直接撞表。一个只迁移过一部分的旧库同样不行——
它比空库更难查，因为前几个用例会通过。

`REQUIRE_DB_INTEGRATION=1` 把 SKIP 变成失败。不加它，一个连不上库的环境会给出全绿，
而"绿灯里藏 SKIP"与"真的验过了"在输出上无法区分。

判断一条断言该不该放这一层，看它**能不能在别处成立**：列名与真实 schema 一致、
影响行数、锁与事务的可见性、真实 collation 下的比较——这些 mock 与 DryRun 都回答不了。
反过来，能用 mock 断言的就不要占用这一层，它需要环境、也跑得慢。

### 真实 Redis 的那一批

Redis 侧不走 `config.Load()`，用环境变量，默认值即可直接跑：

| 变量 | 默认 | 说明 |
|---|---|---|
| `REDIS_ITEST_ADDR` | `127.0.0.1:6379` | 地址 |
| `REDIS_ITEST_PASSWORD` | 空 | 密码 |
| `REDIS_ITEST_DB` | `15` | **专用 db**，不借业务库 |
| `ALLOW_REDIS_SCRIPT_FLUSH` | 未设 | 见下 |

两条不同于 MySQL 那批的理由：`pkg/ratelimit` 对外发布、禁止 import 本仓 internal，
它取不到应用配置；而这批测试需要一个专用 db，应用配置的 `redis.dbs` 只声明业务用到的那几个。
借业务 db 跑测试等于往真实数据里写测试键。

键都带本次运行独占前缀，结束只删自己的键——**不用 `FLUSHDB`**：一次误配的 db 序号
会让它清掉别人的数据，而那不可逆。

`ALLOW_REDIS_SCRIPT_FLUSH=1` 打开的是「脚本缓存被清空后能否自愈」那几条用例。
默认跳过，因为 `SCRIPT FLUSH` 是**服务器级**操作，会清掉同一实例上其它应用的脚本缓存。
它用的是普通 `Skip` 而不是「环境不满足」那种——后者在 `REQUIRE_DB_INTEGRATION=1`
下会变成失败，而这里是刻意的 opt-in，不是环境坏了。

这几条值得单独说：仓内四个包各自实现了 `EVALSHA` + `NOSCRIPT` 重载，
而那条分支此前只对着**手写替身**验过——替身返回的错误字符串是测试自己编的，
证明不了真实 Redis 会那样回复。主从切换与 `SCRIPT FLUSH` 之后脚本缓存就是空的，
那时这条分支是唯一的恢复通道。

需要建表的用例优先用**会话级临时表**（`CREATE TEMPORARY TABLE`）：它随连接消亡，
对目标库零写入、不需要迁移、也不必清理。代价是临时表只在本连接可见，
因此必须把连接池钉成 1 条连接、并解除连接寿命上限，否则语句可能落到另一条
连接上，表就"不存在"了。

## Table-Driven 的适用边界

**存在多组输入时**必须 table-driven：

```go
func TestValidateCreateUser(t *testing.T) {
    tests := []struct {
        name    string
        input   CreateUserInput
        wantErr error
    }{
        {name: "missing email", input: CreateUserInput{Name: "A"}, wantErr: ErrEmailRequired},
        {name: "ok", input: CreateUserInput{Name: "A", Email: "a@example.com"}},
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            err := ValidateCreateUser(tt.input)
            if tt.wantErr != nil && !errors.Is(err, tt.wantErr) {
                t.Fatalf("want %v, got %v", tt.wantErr, err)
            }
            if tt.wantErr == nil && err != nil {
                t.Fatalf("unexpected error: %v", err)
            }
        })
    }
}
```

**单一场景的测试直接写断言即可**（如「启动时缺少某项配置必须失败」）——硬套 table 结构
只增加噪音，不增加覆盖。规范要可满足：一条长期无法满足的规则会让人连能满足的部分也一起忽略。

case 名说明业务场景；断言具体；覆盖 success、error、edge 三类。

## 契约声明必须有反证测试

任何契约声明（幂等、有界、线程安全、原子、不阻塞、最多一次）都必须配一个
**「若该声明为假就会失败」的测试**，否则不许写进文档或注释。

写下「重复调用是安全的」却没有测试，等于凭空承诺。同理，声称某个竞态窗口「已关闭」之前，
必须有一个「移除该机制就会失败」的测试；没有这种测试只能说「已缩窄」。

这条对**判据本身**同样适用。`make check` 里的门禁脚本有几个带 `--self-test`：
它们在临时工作树里构造一个应当失败的场景并断言判据确实失败——因为一条永远为真的判据
和一条正确的判据，在日常运行中表现完全一样。

## Mock 与外部依赖

- mock 经接口注入，不 monkey patch。
- service 测试 mock repository / provider。
- repository 测试用真实 DB（集成测试）。
- HTTP 外部依赖用 `httptest.Server`。
- 时间、随机数、ID 生成器尽量可注入——否则测试要么不确定，要么只能靠 sleep。

## 并发与 race

- 声明并发安全的类型必须有并发测试。
- goroutine 必须能经 context 或 channel 退出，测试要覆盖退出路径。
- 幂等、状态流转、回调覆盖并发重复请求。
- 涉及共享状态跑 `-race`（`make check` 已默认带上）。

## API 与错误契约

- Handler 测试断言 HTTP 状态、业务 code、客户端文案、data 结构。
- 未知错误统一为内部错误，不暴露底层字符串。
- 鉴权和限流覆盖未登录、权限不足、命中限流、正常通过四种。
- DTO / 响应变更同步文档与调用方。

## 回归要求

- 修 bug 先写失败测试或最小复现步骤。
- 修复后重跑受影响包的全量测试，再跑 `make check`。
- **测试失败不能只改断言凑绿。**
- 交付说明实际跑过的命令；没跑要说明原因。

### 验收整改入口与工具失败

`make test`实际调用test-race；迁移目标与test/dev/prod组合时仍保留环境占位。`make check-make-entries`在临时fixture执行Make接线（Go/lint stub只证明调用），clean本体使用真实Go和os.Root，检查symlink/越界及相邻哨兵；日常check包含该自检。真实受影响Go测试必须另外跑，不能把stub当业务通过。

`make lint`和`make tool`除常规包外，还逐个检查scripts目录的独立Go工具；这些build-ignore程序不会进入普通`./...`集合。errcheck开启blank/type-assertion检查并关闭内置函数豁免，测试没有整类errcheck豁免。工具的输出、诊断和self-test清理失败须非零退出；三个诊断工具在stderr自身也不可写时退出2，clean保持既有非零退出1。正常参数和成功输出不变；0 issues只证明实际扫描集合和启用规则，不代表所有运行路径都已实测。

project init在变更文件或移走旧Git前，将文件类型、存在性、mode、原始字节与symlink目标写入根目录同级的 `.projectinit-snapshot-*/recovery.json`（目录0700、文件0600；content是JSON的base64字节字段）。根root与entries的相对path共同定位原文件；kind依次为0不存在、1普通文件、2目录、3symlink。回滚失败时错误给出确切恢复目录，原Git保存在其git-dir；panic/非正常返回同样保留副本并继续传播原panic，不声称已回滚成功。恢复需维护者核对路径和原始字节后处理，不存在自动重放命令；恢复副本可能含敏感文件，不可公开或直接提交。正常完成/成功恢复后清理，清理失败返回非零错误并保留路径。

CLI的stdout/stderr写出、文件close/remove及生成锁释放均处理失败；doctor探针收尾失败会返回退出1并保留原报告/失败原因，报告自身0/2/1语义不变。JSON/人类报告写出失败也返回非零，不把空输出当作通过。

`make test-integration`显式设置PLUGIN_INTEGRATION=1；强制模式缺少该opt-in时直接运行cmd套件会失败，不能静默跳过。`make release-check`同时传REQUIRE_DB_INTEGRATION=1和PLUGIN_INTEGRATION=1。`make check-make-entries`的临时Go模块真实验证cmd集成fixture存在时可使门禁失败，移除后恢复；这证明接线，不替代真实MySQL/Redis套件。共享Redis不得执行SCRIPT FLUSH，相关专用实例测试仍保留单独授权条件。

Compose应用验证用`make check-compose-app`（Python标准库+Docker Compose CLI，只解析合成配置，不连daemon创建容器）；该目标不加入日常check的工具前置。它验证默认活跃集合mysql/redis、显式app profile、挂载/地址/显式空密码、健康/退出参数及宿主shell入口fixture。容器PID 1、实际UID映射、私钥可读性、API/ready故障恢复和SIGTERM必须在获准的临时应用容器中另验。优先用`dev-app-up-existing`复用已启动依赖，不通过停止共享DB/Redis做故障注入；独占app可断开自己的网络后从容器内部检查/health与/ready，再恢复网络。停止/删除只指向本次app和无使用者的独占网络，不删除依赖数据卷。
