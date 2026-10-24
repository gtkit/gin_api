# 质量门禁

## 入口

```bash
make check      # 提交前
make check-ci   # 推送前，与 check 使用同一组门禁
```

**权威清单是 `Makefile` 的 `check` 目标**，本文只做导读。改动门禁时以 Makefile 为准。

`make check` 依次执行：

1. 架构边界检查（`check-architecture`）
2. 预置能力清单穷尽（`check-capability-inventory`，含 `--self-test`）
3. 开发期工具隔离（`check-scaffold-isolation`）
4. 依赖清单整洁（`check-tidy`）、包级变量重绑定认领（`check-package-vars`）、
   文档引用存在性（`check-doc-refs`）
5. 镜像摘要固定、安全判定自检、发布接线、应用身份零漂移、钩子安装语义、构建上下文
6. `golangci-lint run ./...` 与 `go vet ./...`
7. race 测试并生成聚合 coverage profile
8. 全仓覆盖率不低于 `COVERAGE_MIN`

## 两条自动执行点

**门禁存在不等于门禁被执行。** 因此有两条自动执行点，覆盖前提不同，互不替代：

| 执行点 | 何时反馈 | 覆盖前提 |
|---|---|---|
| 本地 pre-push 钩子 | 推送**前**拦截 | 需先执行一次 `make hooks` |
| GitHub Actions | 推送**后**反馈 | 无前提，克隆即生效 |

钩子在问题进入共享历史之前就挡住它，但要人记得装；CI 不需要任何本地步骤，
但反馈发生在推送之后。两者跑的是**同一条命令**（`make check-ci`），不存在只在一侧执行的检查项。

### 装上钩子（克隆后第一件事）

```bash
make hooks
```

它把 `scripts/git-hooks/pre-push` 装到 `.git/hooks/`，推送前自动跑 `make check-ci`。

挂 pre-push 而不是 pre-commit：门禁是分钟级的，挂在每次提交上的结果必然是被
`--no-verify` 长期绕过。确需跳过用 `SKIP_GATE=1 git push`，跳过时会在错误输出留痕。

`make hooks` 幂等。若 `.git/hooks/` 下已有**非本仓**的同名钩子，它报错中止而不覆盖——
那是你自己的自动化，本仓不替你决定怎么合并。

### CI（`.github/workflows/check.yml`）

在 push 与 pull_request 上执行 `make check-ci`，不需要任何本地安装步骤。

工具版本不在 workflow 里写死：Go 版本取自 `go.mod`，`golangci-lint` 版本从
`security/toolchain.mk` 解析，二进制经**同版本**的官方 checksums 校验。
当前固定 golangci-lint 2.14.0，其 x/tools v0.50.0 支持 Go 1.27 的 export data v5；
2.13.2 的读取器只支持到 v4，清理缓存或用新版 Go 重建旧依赖不能修复格式不兼容。
本地安装同一官方版本后运行 `make tool`；版本不符时门禁直接失败。
解析不到版本时中止而非回落默认值——静默用另一个版本，检查的就是另一套规则集。

第三方 action 按 commit SHA 固定，理由与 `toolchain.mk` 钉镜像摘要相同：tag 可变，
「昨天通过、今天失败」无从复现。

安全扫描**不并入 CI**：它依赖网络与 Docker，并入会让门禁在离线环境失败，
最终逼人绕过整个门禁。发版路径上由 `make tag` 强制执行。

## 架构传感器覆盖什么

`scripts/check-architecture.sh`：

- application 禁 import Gin / GORM / repository
- transport/http 禁 import GORM / repository
- repository 禁 import Gin / module
- internal/pkg 禁反向 import module
- worker 禁直接 import GORM / repository
- GORM repo 数据访问统一经 `mdbCtx(ctx)`
- application / dto / models 禁 import 基础设施型 paginator
- 根 `pkg/` 禁 import 本仓 internal、禁绑定日志栈
- 禁 `ShouldBind` / `ShouldBindJSON` / `ShouldBindWith`
- 客户端 IP 只经唯一 accessor 取值
- 禁 `http.DefaultClient` / `http.Get` / `http.Post` / `http.Head`
- 数据访问层（repository / runtime）禁裸 SQL `.Raw(` / `.Exec(`
- 开发期工具不进默认构建的依赖闭包

测试文件单独豁免，避免集成测试误判生产代码。

模块路径从 `go.mod` 读取，**绝不硬编码**：派生项目经 `gonew` 复制时只重写 `.go` 文件里的
import 路径，不碰 shell 脚本。硬编码母版路径的正则在派生项目里永远匹配不到任何东西，
传感器会静默全通过——**那比没有传感器更糟**，因为它给出的是虚假的合规保证。

## 判据自身也需要被验证

一条永远为真的判据和一条正确的判据，在日常运行中表现完全一样。因此部分门禁脚本带自检：

| 脚本 | 自检入口 | 证明什么 |
|---|---|---|
| `check-capability-inventory.sh` | `--self-test` | 每个声明的 scope 真的在扫；缺失条目确实触发失败 |
| `check-capability-inventory.sh` | `--print-scope` | 覆盖范围与扫描行为同源 |
| `check-doc-refs.go` | `--self-test` | 已知的死引用确实被抓到，且一批良性形态（符号引用、外部模块名、glob、端点路径）零误报 |
| `check-doc-refs.go` | `--print-scope` | 覆盖范围与扫描行为同源 |

新增门禁时，如果它的失效不会有任何症状，就必须配自检。

## 新规则准入

1. 先核实现存代码是否已有违例。
2. 有违例则标注存量债务或先整改——**不要让新规则和现状无声冲突**。
3. 能脚本化的规则同步进传感器；无法脚本化的写进 [`review-checklist.md`](review-checklist.md)
   并说明为什么不能自动化。

## 供应链门禁

```bash
make security-source     # 日常：发布候选快照的漏洞与密钥扫描
make security-release    # 发版前：追加全历史密钥扫描，禁止跳过漏洞扫描
```

刻意**不并入 `make check`**：安全扫描依赖网络与 Docker，并入会让 `make check` 在离线环境失败，
最终逼人绕过整个门禁。`make tag` 会强制执行 `security-release`。

## 集成测试

```bash
make test-integration
```

用真实 MySQL 验证 repository 与 schema 一致性，事务内强制回滚、零持久化，无 DB 时自动 SKIP。
不设 CI 强制 job；需要时设 `REQUIRE_DB_INTEGRATION=1` 可把 SKIP 变为失败。
