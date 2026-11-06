# 压测测试零派发与编译进程组回收方案

日期：2026-10-27。状态：实现与技术验收完成；提交、远端引用与归档结果以最终命令和本地 delivery.json 留证。

## 需求来源与当前事实

用户要求修复 83ccbe0 审查发现的两处测试可靠性问题，先落盘方案，再实现并补齐零派发、编译进程组超时回收的回归；验证后直接提交并 push。当前 develop 为 83ccbe0f2a454957d85a30126a0979aa23edd7a2，远端 gtkit。

- TestLoadMainExitCodes 用 200ms 窗口且统一要求 Sent>0；发生器允许零派发正常完成，旧断言会误报。
- 编译使用 CommandContext，默认只杀 go 进程，不杀其编译进程组。本机 Go 源码已核实 Process.Kill、CommandContext、SysProcAttr.Setpgid/Pgid、Kill 和 Getpgid 的定义。
- 本机及 go.mod 均为 Go 1.27.3，运行环境 darwin/amd64；本轮使用已核实的 POSIX 进程组能力，Linux 定义也已核对，但未实跑 Linux。无新依赖。
- 用户已有 docs/adr/0001-module-boundaries-over-ddd-layering.md 修改，保持原样，不暂存或提交。

## 目标、非目标与兼容

1. 普通 completed 结果允许 Sent=0，仍检查退出码、JSON、状态与计数守恒。把结果断言抽到同文件私有测试函数，原生 main 测试和零派发回归共用。
2. 零派发回归使用已存在的 runLoadWithClock，让第二次取时超过窗口，驱动真实发生器返回 completed、Sent=Completed=0，再经过与原生 main 相同的结果断言；不靠真实调度时间或伪造统计值。
3. 需要真实 HTTP 503 的原生场景保留 Sent>0 和状态断言，窗口延长至 2s，并等待 handler 的就绪通知。普通退出不强求网络派发；SIGINT/SIGTERM 场景仍等待请求就绪后发信号。
4. 同文件增加仅用于编译及回归的私有命令构造函数：CommandContext + Setpgid=true；Cancel 向 -PID 的自有进程组发 SIGKILL，ESRCH 视为已经结束，其余错误保留；WaitDelay=1s 限制异常 I/O 收尾。正常本机编译仍使用一分钟预算。
5. 进程组回归在临时测试进程中建立一个独立组，把第二个由测试直接持有的进程加入该组，模拟 go 和编译器的组成员关系。通过管道就绪事件、Getpgid 核对组归属；就绪后启动短 deadline 并向编译命令传播取消，在 2s 收尾预算内对两个进程分别 Wait，断言均被 SIGKILL 终止。两个成员均由测试直接启动和回收，不依赖系统 init 收割孤儿，也不把 zombie 的 PID 存在误当成仍执行。
6. 每个夹具有独立的 10s 故障保护，失败时逐个 Kill/Wait 并关管道。保护逻辑与待验证的组取消分开，不能用保护动作制造通过。夹具只经子进程环境标记启用，不修改父进程环境。

不修改 scripts/load-api.go、业务或 OpenAPI 解析、配置、依赖、目录结构、公开 API。进程组保证只覆盖继承/加入该组的成员，不承诺终止主动脱离进程组的程序。本轮不是跨平台移植或生产性能测试。

## 实施范围

| 文件 | 阶段 | 内容 |
|---|---|---|
| 本方案 | 0–3 | 先保存回读，逐阶段追加真实证据 |
| scripts/load-api_test.go | 1–2 | 共享结果断言、零派发回归、组命令及真实进程夹具 |
| .harness/error-journal.md（本地忽略） | 1–3 | 记录两处问题、红灯与处置 |
| openspec/changes/stabilize-load-test-processes（本地忽略） | 0–3 | 本次独立 change 的 artifacts、任务和验收 |

仅提交方案与 scripts/load-api_test.go。沿用本聊天已明确允许跳过不可激活 workflow.py 会话门禁的方式；不伪造会话或检查结果，不改 hook/权限。OpenSpec list 已读，无匹配的活动变更。

## 阶段计划

| 阶段 | 前置 | 步骤与检查 | 证据及停止条件 | 状态 |
|---|---|---|---|---|
| 0 | 最新请求、规则、原实现、错误记录已读 | 保存回读本方案，完成独立 OpenSpec artifacts 与 applyRequires | 方案原文、apply ready；事实不符则先核对 | 已完成 |
| 1 | 阶段 0 完成 | 抽出旧结果断言与命令构造；先隔离进程组但保留默认单进程 Kill；补零派发与组成员超时回归并确认红灯 | 失败须对应目标断言，夹具失败也必须回收进程 | 已完成 |
| 2 | 红灯已核对 | 放开普通完成的零派发，保留实际请求场景断言；实现整组取消和 I/O 收尾上限 | 独立工具全部普通测试通过，不能删弱失败条件凑绿 | 已完成 |
| 3 | 功能完成 | 独立工具局部 race、产品 build、架构检查、最终回读、strict validate；仅提交本轮文件并 push/核对远端，归档当前 change | 记录每条真实命令、退出码和未运行项 | 技术验收完成，Git/归档结果另行留证 |

同一问题连续修复 3 次仍失败则停止并报告。进程组 ID 必须来自已启动、仍由测试管理的组长，不使用外部 PID；出现启动、超时或清理错误时先保留失败，再由独立兜底回收，不标记通过。

## 验证命令与边界

日志放在 .cache/load-test-reliability-20261027/；本轮实际命令如下：

```sh
GOTOOLCHAIN=local go test -vet=off -count=1 -timeout=3m scripts/load-api.go scripts/load-api_test.go
GOTOOLCHAIN=local go test -vet=off -race -count=1 -timeout=3m scripts/load-api.go scripts/load-api_test.go
GOTOOLCHAIN=local go build ./...
GOTOOLCHAIN=local scripts/check-architecture.sh
```

红灯时只选新增回归；修复后跑完整独立工具测试。真实 main 的 0/1/2、信号和输出失败场景继续保留。局部 race 只证明执行到的测试并发路径，夹具只证明 POSIX 组回收，不冒充真实编译器卡死或 Linux 实跑证据。

不自动运行 lint、脚本 lint、vet、modern、全量 race、覆盖率门禁或隐含这些检查的聚合入口。不以旧结果冒充本轮结果。Git 若遇到需要额外检查的 hook，先遵守当前授权边界，不擅自修改 hook。

回滚用本次提交的精确 revert，不重置工作区、改历史或用户文件。只操作临时文件和本轮启动的进程，无数据库、容器、生产状态或依赖升级需要回滚。

## 实际结果

- OpenSpec apply ready 后才修改代码。
- red-tests.log exit 1：零派发结果被拒绝；组成员在 build deadline 后存活超过 2s，均为目标断言失败。已记录 ERR-20261027-212，失败后的独立清理完成。
- tool-tests.log exit 0（5.237s）：首次完整普通回归通过，保留 7 个原生 main 场景。
- 最终回读补充明确检查：10s 备用保护和 2s 收尾预算只要已到期就不能算组退出成功。最终版本将继续跑整组普通与局部 race。
- final-tool-tests.log：最终整组普通测试 exit 0（6.281s）；零派发与进程组超时回归、原有 7 个原生 main 场景均通过。HTTP 503 场景实际等待并收到请求。
- tool-race.log：最终整组局部 race exit 0（6.751s）。组回收测试分别收到两个子进程的 Wait 结果，并断言 SIGKILL；未依赖 10s 备用保护获取成功。
- build.log：产品 build exit 0。architecture.log：架构检查 exit 0，repository context check 和 architecture check 均通过。
- 完整函数及调用链回读完成：编译入口与进程回归共用 newLoadBuildCommand；标准完成和零派发共用 assertLoadMainSummary；管道创建后立即登记清理，启动后关闭父进程持有的子端，Kill/Wait 在剩余管道关闭前执行。
- scripts/load-api.go、OpenAPI 解析及 go.mod/go.sum 无差异。用户 ADR 不纳入提交。未运行 lint、脚本 lint、vet、modern、全量 race 或覆盖率门禁。
- openspec-validate.log：当前 change 严格验证 exit 0，1 通过、0 失败。提交前远端仍为 83ccbe0，ADR SHA256 与基线一致。Git 推送及归档将在本轮验证后执行，以本地 delivery.json 记录最终结果。
