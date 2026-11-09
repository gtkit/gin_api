# 编译组长提前退出后的清理方案

日期：2026-10-27。状态：实现与技术验收完成；Git 和归档结果以最终命令及本地 delivery.json 留证。

## 来源与事实

用户要求修复 40127fd 的编译主进程提前退出清理缺口，补齐“组长先退出、同组成员仍运行”的回归，先落盘方案，验证后直接提交并 push。当前 develop 为 40127fddd390480e26f57054aa231c306ccfa705，远端 gtkit。

已读源码：newLoadBuildCommand 仅在 context 取消时杀进程组；Cmd.Wait 完成后停止 context 监控，稍后 cancelBuild 不再触发 Cancel。WaitDelay 只限制 I/O 收尾。原有组回归保持组长存活到 deadline，未覆盖提前退出。当前环境和 go.mod 均为 Go 1.27.3，本机 darwin/amd64；POSIX API 延续上一轮已核实用法，无依赖变更。

## 目标、实现与兼容

- 新增同文件私有 waitLoadBuild，等待已成功启动的构造器命令后，无论成功或失败均清理它拥有的进程组；复用既有 Cancel 行为。os.ErrProcessDone 表示组已消失，不作为失败；其他清理错误用 errors.Join 保留原始等待错误并增加清理上下文。
- 实际编译由 CombinedOutput 改为显式绑定同一输出缓冲、Start、waitLoadBuild。启动失败不操作未建立的进程组；正常编译与已有一分钟预算、1s WaitDelay、context 取消行为保持一致。
- 既有进程夹具允许选择等待函数，并暴露已持有的 stdin 写端用于触发组长正常退出；组长、普通成员继续由测试直接拥有和回收。
- 新增表驱动回归：两个同组进程就绪后，分别关闭组长 stdin 使其退出 0，或只对组长发 SIGKILL；context 保持未取消。同组成员的 stdin 始终保持打开，必须由 waitLoadBuild 的组清理终止。3s 内分别收到 Wait，验证组长原始结果和成员 SIGKILL，且备用 10s 保护未触发。
- 原有 deadline 组回归改用同一等待函数；普通成员使用 Cmd.Wait，避免其自行做组清理掩盖缺陷。已有零派发、HTTP、0/1/2 退出码测试保留。

不改 scripts/load-api.go、OpenAPI、业务代码、配置、依赖、目录结构或公共接口。只清理本次命令通过 Setpgid 建立的进程组；主动脱组不在保证内。测试验证 POSIX 组内成员关系，不冒充真实编译器故障或 Linux 实跑。

## 文件与阶段

| 文件 | 用途 |
|---|---|
| 本方案 | 先保存回读，记录实际阶段和验收结果 |
| scripts/load-api_test.go | 编译等待与清理、既有夹具适配、新回归 |
| .harness/error-journal.md（本地忽略） | 缺陷、红灯和处置证据 |
| openspec/changes/cleanup-exited-build-groups（本地忽略） | 当前独立 change 的 artifacts、任务和验收 |

仅提交方案和测试文件。用户已有 docs/adr/0001-module-boundaries-over-ddd-layering.md 修改保持原样，不暂存。沿用本聊天已明确允许跳过无法激活的 workflow.py 会话门禁的方式；不伪造会话 ID 或通过记录，不修改 hook/权限。OpenSpec list 已核对，无匹配活动变更。

| 阶段 | 前置条件 | 具体步骤 | 证据与停止条件 | 状态 |
|---|---|---|---|---|
| 0 | 最新指令、规则、原方案、源码和错误记录已读 | 保存回读本方案；按 CLI instructions 建立独立 change 并补齐 applyRequires | 完整计划、apply ready；事实不符先核对 | 已完成 |
| 1 | 阶段 0 完成 | 提取暂不增加清理的等待入口；添加组长成功/失败提前退出回归，实际复现成员遗留 | 红灯必须对应成员未退出，失败后独立 Kill/Wait 回收 | 已完成 |
| 2 | 红灯原因已确认 | 等待后执行组清理、接入真实编译路径；保留原错误和输出 | 完整独立工具普通回归通过，不能靠取消 context/兜底清理凑绿 | 已完成 |
| 3 | 功能完成 | 局部 race、产品 build、架构检查、最终回读、strict validate；提交、push、远端核对及当前 change 归档 | 记录命令、退出码、未验证项；同问题 3 次修复仍失败则停止报告 | 技术验收完成，交付结果另行留证 |

## 验证、风险与回滚

本轮验证命令如下；日志放在 .cache/build-group-exit-cleanup-20261027/，实际结果见文末：

```sh
GOTOOLCHAIN=local go test -vet=off -count=1 -timeout=3m scripts/load-api.go scripts/load-api_test.go
GOTOOLCHAIN=local go test -vet=off -race -count=1 -timeout=3m scripts/load-api.go scripts/load-api_test.go
GOTOOLCHAIN=local go build ./...
GOTOOLCHAIN=local scripts/check-architecture.sh
```

红灯仅运行新增场景。修复后验证整组；独立进程退出状态必须经 Wait 获取，不用 PID 存在性判断 zombie。清理产生非预期错误必须让测试失败，不能覆盖编译失败。备用清理只在断言结束后收尾，不计作通过。工具/库普通测试和局部 race 不代表全量检查或生产验收。

不自动运行 lint、脚本 lint、vet、modern、全量 race、覆盖率门禁或隐含这些检查的聚合入口。无需安装工具、拉镜像、创建容器或连接生产。回滚使用本次提交的精确 revert，不重置工作区、改写历史或用户文件。

## 实际结果

- 代码修改前已保存并回读方案，OpenSpec apply 状态 ready。
- red-leader-exit.log exit 1：正常退出与被单独 SIGKILL 两个场景均复现组成员遗留；红灯不是启动或编译错误。已登记 ERR-20261027-213。
- tool-tests.log exit 0（5.900s）：完整工具普通测试通过。组长提前成功/失败退出、原有 deadline 回收、零派发及 7 个 main 退出场景均通过。
- tool-race.log exit 0（8.226s）：完整独立工具局部 race 通过。build.log 和 architecture.log 均 exit 0，架构检查含 repository context 检查。
- 最终逐函数及调用方回读完成：实际编译与两种组回归都用 waitLoadBuild；普通成员独立 Wait，不会代替组长触发清理。等待错误保持原对象可通过 errors.AsType 识别；额外清理错误被包装合并。
- git diff --check 无输出；产品源文件、OpenAPI 提取器和依赖无差异。未运行 lint、脚本 lint、vet、modern、全量 race 或覆盖率门禁。本机为 macOS，Linux 未实跑。
- openspec-validate.log：当前 change 严格验证 exit 0，1 通过、0 失败。提交前远端仍为 40127fd，用户 ADR 指纹与原记录一致。最终提交与远端 hash、归档结果写入本地 delivery.json。
