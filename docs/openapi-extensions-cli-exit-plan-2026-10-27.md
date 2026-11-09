# OpenAPI 扩展与压测进程退出回归方案

日期：2026-10-27。状态：实现与技术验收已完成；Git 交付结果以最终命令与本地 delivery.json 留证。

## 需求与事实

用户要求修复 719ce99 中 OpenAPI 对 x- 扩展的误拒绝，补充相关回归和 main 退出码子进程测试；先落盘方案，验证后直接提交并 push，无需再次确认。起点为 719ce99a9aac021c499c998b61b7fca19d61f62d，当前分支 develop，远端 gtkit。

已读取源码：openAPIExpectedRoutes 将 paths 每一项都解码成 Path Item，随后拒绝 null 和 $ref，导致合法的 Paths Object 扩展被误拒绝；字符串、数组等扩展还会提前解码失败。OpenAPI 3.1.0 的 Paths Object 允许 x- 扩展，扩展值可为任何 JSON 值。依据：https://spec.openapis.org/oas/v3.1.0.html#paths-object 和 https://spec.openapis.org/oas/v3.1.0.html#specification-extensions 。

load-api 的 main 已在 loadCLI 返回错误时退出 1，stderr 写入失败退出 2；loadCLI 先写 JSON，再返回中止错误。现有测试直接调用 loadCLI，原生 SIGTERM 验收仅保存在本机上一轮材料中，尚无仓库内的 main 子进程回归。

版本：本机 Go 1.27.3（/usr/local/go），go.mod 为 1.27.3；Gin v1.12.0。无依赖、配置、接口、数据库、业务运行代码或目录层级变更。已有用户修改 docs/adr/0001-module-boundaries-over-ddd-layering.md 不纳入任务，其 SHA256 为 0c5ee44595ca4847380c73ea8bd70a716a98d2219175f7a87fb35e9033e0dbe1。

## 目标、兼容与非目标

- paths 先解码为 map[string]json.RawMessage；仅跳过键以 x- 开头的扩展，再解码其余路径项。扩展内的 get、null 或 $ref 不参与路由提取。
- 继续拒绝真实路径的 null、非对象值和任何路径级 $ref；允许空 Path Item、路径级普通元数据和 schema 引用。保留 x-auth-mode 过滤及双向生产路由对照。
- 使用本机 Go 将 scripts/load-api.go 编译到测试临时目录，执行真实 main；不以调用 loadCLI 或 go run 包装器代替进程退出测试。
- 子进程覆盖正常完成退出 0、HTTP 失败但过程完成仍退出 0、参数错误退出 1 且无结果 JSON、SIGINT/SIGTERM 中止退出 1 且有 canceled JSON、stdout 写失败退出 1、stderr 写失败退出 2。
- 信号测试通过 loopback handler 通知确认请求已开始再发信号；不靠固定 sleep 猜测进程就绪。子进程有超时，失败也取消、等待回收，关闭服务器和文件。只在临时目录编译，不安装工具或启动容器。
- 不改 scripts/load-api.go、调度边界、取消分类、指标、业务 API、docs/openapi.json 或既有 ADR；不实现引用解析或完整 OpenAPI 校验器。

## 文件与依赖范围

| 文件 | 阶段 | 改动 |
|---|---|---|
| 本方案 | 0–3 | 保存计划、回读、追加实际结果 |
| internal/bootstrap/openapi_contract_test.go | 1 | RawMessage 提取、扩展回归、真实路径拒绝反例 |
| scripts/load-api_test.go | 2 | 本机编译和原生进程退出/JSON 回归 |
| .harness/error-journal.md（本地忽略） | 1–3 | 记录确认的缺陷、红灯及处置证据 |
| openspec/changes/fix-openapi-extensions-cli-exit-tests（本地忽略） | 0–3 | 当前独立 change 的 artifacts、tasks、验收与归档 |

只提交前 3 个文件。复用既有标准库和项目依赖；不修改 go.mod/go.sum。OpenSpec list 已核对，没有匹配的活动 change。沿用本聊天已明确允许跳过无法激活的 workflow.py 会话门禁的执行方式，不伪造会话 ID 或通过凭据，不修改 hook、其他会话状态或权限。

## 实施阶段与停止条件

| 阶段 | 前置条件 | 步骤 | 验收证据 | 状态 |
|---|---|---|---|---|
| 0 | 最新授权、规则、源码与错误记录已读 | 保存并回读本方案；创建独立 OpenSpec change，按 CLI instructions 补齐 applyRequires | 计划原文与 apply-ready 状态 | 已完成 |
| 1 | 阶段 0 完成 | 先加入扩展表驱动回归，实际确认红灯；记录缺陷；改提取器；检查真实路径反例 | 红灯日志、bootstrap 整包回归 | 已完成 |
| 2 | 阶段 1 完成 | 加入 main 子进程退出测试，保留已有 loadCLI、取消及调度回归 | 独立工具全部测试，真实退出码与 JSON 断言 | 已完成 |
| 3 | 全部功能完成 | 受影响范围普通测试和局部 race、产品 build、架构检查；回读完整函数与 diff；strict validate/归档；仅提交本次文件并 push gtkit/develop，核对远端 hash | 命令、退出码、日志、提交与远端引用 | 技术验收完成；Git/归档结果以交付记录为准 |

每阶段逐项记录实际结果。相同问题连续修复 3 次仍失败则停止并报告；失败不标通过。未知事实先查证；不添加本轮范围外修复。子进程启动或通信失败时先取消并等待回收；拒绝通过跳过用例换取成功。

## 验证与交付

本轮验证命令如下，日志置于 .cache/openapi-extensions-cli-exit-20261027/；实际结果见文末：

```sh
GOTOOLCHAIN=local go test -vet=off -count=1 -timeout=5m ./internal/bootstrap
GOTOOLCHAIN=local go test -vet=off -count=1 -timeout=2m scripts/load-api.go scripts/load-api_test.go
GOTOOLCHAIN=local go test -vet=off -race -count=1 -timeout=5m ./internal/bootstrap
GOTOOLCHAIN=local go test -vet=off -race -count=1 -timeout=2m scripts/load-api.go scripts/load-api_test.go
GOTOOLCHAIN=local go build ./...
scripts/check-architecture.sh
```

本次不自动执行需另行授权的 lint、脚本 lint、vet、modern、全量 race 或覆盖率门禁，不通过 make/check hook 间接执行。为完成用户已授权的 push，若本地 pre-push 会触发上述检查，使用仓库已有的 SKIP_GATE=1 并如实记录；不修改 hook。局部测试不能冒充全量质量检查。

验收标准：所有扩展 JSON 类型被忽略且路由集合准确；真实路径拒绝边界不放宽；真实进程 0/1/2 分支和中止 JSON 通过；所有子进程有回收路径；scripts/load-api.go、配置、依赖及用户 ADR 不变；远端 hash 与本次提交一致。契约/工具测试不代表生产负载或服务容量验证。

回滚：对本次提交做精确 revert；不重置工作区、不强推、不改历史或用户文件。提交前失败保留真实日志和当前差异，修复仅限本次范围。无生产数据、迁移或外部运行状态需要回滚。

## 实际结果

- OpenSpec apply 状态为 ready。方案及前置规则已回读。
- red-openapi.log：新增扩展测试在旧实现实际失败，exit 1；覆盖 local/external 的 null、标量、数组、引用对象和伪操作对象。已记录 ERR-20261027-211。
- bootstrap-tests.log：RawMessage 修复后，bootstrap 整包普通回归 exit 0（5.880s），保留 /items 与 /x-extra 的非法路径反例。
- tool-tests.log：全部独立工具普通测试 exit 0（3.243s），包含 7 组真实 main 子进程场景。信号在首个请求进入后发送，取消数按 completed 断言，兼容既定派发边界；最终版本随后继续执行局部 race。
- bootstrap-race.log：bootstrap 整包局部 race exit 0（10.418s）。tool-race.log：最终版本的独立工具全部局部 race exit 0（5.406s），含 7 个原生进程场景。
- build.log：产品 go build ./... exit 0。architecture.log：架构检查 exit 0，repository context check 与 architecture check 均通过。git diff --check 无输出。
- 最后逐段回读路径提取、所有新测试、生产 main/loadCLI 和 README 既有契约。子进程等待完成后才读 buffer；失败清理先取消再 Wait；真实网络用于跨进程通信。此次结论只覆盖契约与工具，不涉及生产性能。
- 本轮未运行 lint、脚本 lint、vet、modern、全量 race、覆盖率门禁；没有通过聚合命令执行。实际本地 pre-commit 引用的检查器不存在，pre-push 不存在，不需设置 SKIP_GATE 或绕过 hook。
- 普通文档目录受 gitignore 忽略；本方案是用户明确要求交付的文件，提交时仅对本文件使用 git add -f，不修改忽略规则。其余 OpenSpec/日志继续依既有约定留在本机。
- openspec-validate.log：当前 change 严格验证 exit 0，1 项通过、0 项失败。代码与测试需求已完成，Git 交付后再记录远端 hash、勾选交付任务并归档当前 change。
