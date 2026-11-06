# Readiness 测试与存量 lint 修复计划

日期：2026-10-27（Asia/Taipei）
状态：四处修复、全量验收均完成；进入 Git 交付阶段。

## 来源、范围与兼容边界

用户要求修复上一轮遗留的 1 个测试失败与 3 项 lint，验证通过后提交并 push 到 GitHub，无需再次确认。上一轮证据见 `docs/capacity-optimization-plan-2026-10-26.md` 第 13 节。本轮重新执行验证，不把历史结果当作本轮结果。

目标只包括这四处测试代码及其验收记录：

| 文件 | 已核对事实 | 具体修复 |
|---|---|---|
| `internal/router/panic_observability_test.go` | `TestReadinessKeepsPrivateCauseInRateLimitedLogs` 在依赖恢复后立即要求 200；`health.go` 的 `readyCacheTTL` 为 1 秒，缓存过期前不会重新探测 | 将探针请求放入 `synctest.Test`；验证缓存期内仍返回安全的 503，虚拟时间推进到 TTL 后仍失败时重复探测不会重复日志，再恢复依赖并推进 TTL，验证 200 和单次恢复日志 |
| `internal/bootstrap/http_error_log_test.go` | logger 构造失败分支忽略 `output.Close` 的错误 | 关闭错误用 `t.Error` 报告，保留原始构造错误的 `t.Fatal`，不抑制 lint |
| `internal/middleware/routetimeout_test.go` | HTTP/2 测试直接断言 `client.Transport.(*http.Transport)` | 用逗号 ok 检查，类型不符时 `t.Fatalf`，继续保留单连接和兄弟流检查 |
| `internal/bootstrap/validation_test.go` | `newProviders` 只定义未引用 | 删除该私有测试辅助函数及仅为它引入的 context import |

环境已核对：本机 Go 1.27.3 darwin/amd64；golangci-lint 2.15.0；Gin v1.12.0、logger/v2 v2.5.0 与当前 go.mod 保持不变。使用标准库 `testing/synctest`，不新增依赖。

非目标：不改生产代码、探针 TTL、日志文案、运行配置、接口、数据结构、存储、架构与依赖版本；不修改用户原有 ADR；不创建容器、不连接生产。OpenSpec 为本次测试维护建立独立 change，归档使用 `--skip-specs`，不改产品规范。

## 阶段与验收

| 阶段 | 前置条件 | 步骤与检查 | 可复核证据 | 停止条件 | 状态 |
|---|---|---|---|---|---|
| 0 计划与基线 | 已读全局规则、项目规则、原计划、相关错误记录 | 保存并回读本计划；核对当前分支/远端与 ADR 指纹；完成本次 OpenSpec artifacts；运行原失败用例获取本轮红灯 | 本计划、CLI 输出、`baseline-readiness.log` | 基线不符或有范围外冲突则先核对，不猜测修复 | 已完成 |
| 1 修复 | 阶段 0 完成；用户授权覆盖四处修复 | 按上表修改四个测试文件，保留所有原业务断言；运行 router/bootstrap/middleware 整包普通测试 | 四文件 diff 与 `affected-tests-fixed.log` | 同一问题连续修正 3 次仍失败则停下报告 | 已完成 |
| 2 验证 | 四处修改完成；用户已明确授权本次验证 | readiness 用例 race 重复 20 次；产品 build；全量 vet/modern/lint/架构；全量 race 与覆盖率一次运行，不排除 readiness 用例 | 下列命令日志、退出码、覆盖率，modern 空输出、lint 0 issues | 任何检查失败先定位，仅修本次四处范围内问题；不降低门禁或跳过用例 | 已完成 |
| 3 交付 | 所有要求的验证成功 | 回读完整函数与 diff；更新本计划真实结果；仅暂存本任务文件；提交到 develop、push gtkit 并核对远端哈希 | Git commit、push 与 ls-remote 输出，ADR 指纹一致；结果保存在本次提交及本地 delivery.json | push 冲突不强推、不覆盖用户工作 | 验收材料已备，Git 结果以命令留证为准 |

验证命令（日志位于 `.cache/readiness-lint-repair-20261027/`）：

```sh
GOTOOLCHAIN=local go test -vet=off -count=1 -run '^TestReadinessKeepsPrivateCauseInRateLimitedLogs$' ./internal/router
GOTOOLCHAIN=local go test -vet=off -count=1 -timeout=5m ./internal/router ./internal/bootstrap ./internal/middleware
GOTOOLCHAIN=local go test -vet=off -race -count=20 -timeout=5m -run '^TestReadinessKeepsPrivateCauseInRateLimitedLogs$' ./internal/router
GOTOOLCHAIN=local go build ./...
GOTOOLCHAIN=local go vet -tags scaffold ./...
GOTOOLCHAIN=local go fix -diff -tags scaffold ./...
GOTOOLCHAIN=local golangci-lint run ./...
scripts/check-architecture.sh
GOTOOLCHAIN=local go test -vet=off -tags scaffold -race -count=1 -timeout=5m -covermode=atomic -coverprofile=.cache/readiness-lint-repair-20261027/coverage.out ./...
scripts/check-coverage.sh .cache/readiness-lint-repair-20261027/coverage.out 72.5
```

Go vet 已作为独立门禁运行，测试命令中的 `-vet=off` 只避免重复执行。独立 Go 工具未修改，本轮不重复其全部验证。覆盖率必须等待测试成功完成后读取，不使用 `-skip`、不关闭 lint 规则。

## 风险、失败与回滚

虚拟时钟只包围探针和同步请求。初轮验证已发现：即使在外层创建文件日志 Owner，首次写入仍会让轮转 WaitGroup 在 bubble 内 Add、全局 worker 在外部 Done，触发 synctest 隔离错误。改用项目既有 `http_error_log_test.go` 的 console-only logger 捕获模式：创建时把 stdout 绑定临时文件，立即恢复 stdout；关闭文件轮转，在 bubble 外 Sync/Close。保留真实日志、安全响应、限频及恢复日志的精确断言；重复失败探测之间推进 TTL，但间隔短于 30 秒日志限频，确保实际覆盖限频而非仅命中缓存。

只删除静态查询确认零调用的测试 helper。只给测试类型断言加错误分支，不更换 HTTP Transport。没有对外行为、调用方或存储兼容变化，无需数据回滚；如要回滚，按本次提交精确反向补丁恢复四文件，不重置工作区、不覆盖 ADR。

## 实际结果

- 阶段 0：当前分支 develop，起点 9fbb0fc；原有 ADR SHA-256 为 `0c5ee44595ca4847380c73ea8bd70a716a98d2219175f7a87fb35e9033e0dbe1`。本轮原 readiness 用例 exit 1，`recovered readiness = 503`；OpenSpec 本次独立 change `fix-readiness-test-and-lint` 的 apply 状态 ready。其余检查待执行。
- 阶段 1 首轮普通整包命令 exit 1：bootstrap、middleware 通过，router 文件日志轮转触发上述 synctest 边界错误。日志为 `affected-tests.log`；正在按已核对的既有日志捕获方式修正测试夹具，未改生产代码。
- 阶段 1 最终普通整包命令 exit 0：router 7.059s、bootstrap 8.437s、middleware 4.541s，日志 `affected-tests-fixed.log`。夹具修正完成，四处改动仅涉及测试文件。
- 阶段 2：下表各命令均在本轮实际执行，退出码均为 0；没有跳过原失败 readiness 用例。

| 检查 | 本轮结果 | 日志 |
|---|---|---|
| readiness 单例 race 重复 20 次 | 通过，6.570s | `readiness-race.log` |
| 产品 `go build ./...` | 通过 | `build.log` |
| 全量 `go vet -tags scaffold ./...` | 通过 | `vet.log` |
| 全量 `go fix -diff -tags scaffold ./...` | 输出为空 | `modern.log` |
| 全量 `golangci-lint run ./...` | 0 issues；原配置中未匹配排除规则的提示保留，未更改规则 | `lint.log` |
| 架构检查 | repository context、architecture 均通过 | `architecture.log` |
| 全量 race + coverage | 48 个测试包通过，0 失败、0 数据竞争；bootstrap 85.3%、middleware 88.6%、router 95.7% | `race-coverage.log` |
| 总覆盖率门禁 | 84.7% ≥ 72.5% | `coverage-check.log`、`coverage.out` |
| OpenSpec strict validate | 1 passed / 0 failed | `openspec-validate.log` |

最终 artifacts 再次 strict validate 通过（`openspec-validate-final.log`）。归档输出确认 tasks Complete，且跳过产品 spec 更新；本地归档为 `openspec/changes/archive/2026-10-27-fix-readiness-test-and-lint`，见 `openspec-archive.log`。

已回读完整修改函数与 diff；生产代码未改动，不涉及调用方签名、错误返回或运行时资源生命周期。测试仍通过真实路由请求验证 503/200、响应脱敏、跨 TTL 日志限频和单次恢复日志；HTTP/2 单连接与兄弟流断言保留。依赖与配置不变，用户原有 ADR 指纹不变。本次是测试维护及静态检查修复，不构成生产容量或真实外部依赖的准入结论。

交付限定这四个测试文件与本计划；OpenSpec 和错误记录沿用仓库忽略约定留在本地。Git 最终提交号及远端引用核对通过命令留证，保存在 `.cache/readiness-lint-repair-20261027/delivery.json`，避免在文档内循环引用自身提交号。
