# Makefile 分类拆分：完整方案与实施计划

日期：2026-10-23（Asia/Taipei）  
状态：**已按用户确认收尾。九个实施文件及 P3 静态复核已完成；根 Makefile 从 423 行变为 126 行。P4 三项均通过：隔离接线验证（含夹具 race）、项目初始化定向回归、make lint（全仓及五组脚本均 0 issues）。不再追加 vet、modern、项目全量 race 和覆盖率检查；cmd 既有超时未处理。实际结果见第 17 节，收尾确认见第 19 节。**

## 1. 需求来源与授权边界

用户明确要求：

> 按你的建议，把 Makefile 分类拆分的完整方案和实施计划落盘给我确认，列清各子文件的职责和目标归属，保留现有命令、默认目标、变量覆盖和执行行为。先别改代码，其他受控检查仍不执行，cmd 的既有超时不处理。

最初方案阶段只新增本文并回读。用户后续已明确批准按本文实施，产品文件范围与原要求不变；验证仍须按阶段另行授权。第 14 节保留历史阻断，第 15 节记录本轮实施、静态证据及未验证项。

目标：将根 Makefile 中的构建、开发环境、质量检查、脚手架、迁移、发布规则分到六个明确的子文件；用户继续从项目根执行原来的 make 命令。保留公共变量、默认入口、条件分支、聚合依赖、参数展开、失败传播及已有危险操作的确认方式。

非目标：

- 不修改 Go 业务代码、日志接口、目录分层或依赖版本。
- 不重命名、删除或增加业务命令；不新增 help/all 默认入口。
- 不合并重复端口规则、不修正存量注释、不扩充 .PHONY、不改变质量门禁内容。
- 不改 Dockerfile、Compose、镜像、部署流程或 CI 的调用命令，不连接数据库或创建容器。
- 不处理 cmd 既有超时，不重跑此前任务，不更新日志迁移方案或 OpenSpec 内容。
- lint、脚本 lint、vet、modern、全量 race、覆盖率和包含这些操作的脚本，仍须功能完成后另行授权。

## 2. 当前事实与证据边界

本轮实际读取了完整 Makefile、project.mk、security/toolchain.mk、入口测试脚本、有关的脚本调用方、README、Docker 构建文件和既有错误记录。只读提取得到：

| 项目 | 当前事实 |
|---|---|
| 根 Makefile | 423 行，56 个不同的静态普通目标；run/lint/test 含互斥定义，port/kill-port 各有两处配方 |
| 当前源码指纹 | Makefile SHA256：`3036f338f8bcb889128d53c468ec5b02f3a106669b12bd967ed5de2fe8f4d1fc` |
| 已有 include | project.mk、security/toolchain.mk；二者只含变量和注释，没有普通目标 |
| 默认目标 | 源码中第一个普通目标是 dev；无参数 make 选择 dev。该结论来自源码与 Make 规则，本轮未执行启动命令 |
| 根文件变量 | 20 个赋值：拟保留 17 个公共变量在根文件，3 个专用变量随规则移动；赋值运算符保持原样 |
| 固定版本 | go.mod 为 Go 1.27.3；toolchain.mk 的 GOLANGCI_LINT_VERSION 为 2.15.0；均不变。本轮不执行工具版本查询或升级 |
| 工作区 | 根 Makefile 已有未提交改动，含端口的后置定义和上一任务修正的脚本 lint 循环；必须以当前工作区为基线，不以 HEAD 覆盖 |
| 既有检查 | 上一任务的 make lint 已通过，见日志迁移方案第 17.7 节；这是历史证据，不是拆分后的验证 |
| 尚未验证 | 拆分后的解析、默认目标、变量来源、脚本接线及实际 lint；本轮没有运行这些检查 |

本文所有“原行号”均指上述 423 行基线，实施前若指纹变化，应先核对新增差异并更新映射，不能覆盖其他工作。

## 3. 方案比较与选择

| 方案 | 行为风险与改动面 | 维护成本 | 回滚 | 结论 |
|---|---|---|---|---|
| A. 只给现有单文件增加章节 | 文件位置不变，改动最少 | 仍需在 423 行中维护六类规则 | 恢复单文件 | 未完成用户要求的子文件拆分 |
| B. 六个 .mk，以显式 include 组成同一份 Make 配置 | 共享同一目标图和变量空间；主要风险是定义先后、复制夹具漏文件 | 规则按职责定位；根文件仍保留必要的兼容内容 | 恢复根文件和配套改动，移除本次新增文件 | **采用** |
| C. 每类通过递归 make -f 子文件执行 | 新增进程/调用边界；MAKECMDGOALS、$@、变量传递及失败处理需要另行设计 | 每个子文件需要重复入口或公共配置 | 需要回滚命令编排 | 不符合只改变存放位置、保留当前行为的目标 |

本次没有请求链路、数据模型、存储、一致性或线上容量变化，不进行 QPS、连接数、存储或人日的无依据估算。可核对的规模是六个新增 .mk、九个拟变更文件以及完整的 56 个目标映射。

## 4. 拟实施文件范围

以下九个文件为用户已批准的实施范围。make/ 仅存放分类构建规则，不移动任何 Go 模块或改变依赖方向。方案阶段未创建它们；本轮实施结果见第 15 节。

| 文件 | 动作 | 职责 |
|---|---|---|
| Makefile | 修改 | 保留公共变量、默认/条件入口、聚合目标、端口兼容定义；显式加载六个子文件 |
| make/build.mk | 新增 | 编译、镜像构建、受管产物清理 |
| make/dev.mk | 新增 | 开发依赖、应用 Compose 操作、开发钩子安装 |
| make/quality.mk | 新增 | 原有检查与测试目标、安全扫描入口；不增加检查内容 |
| make/scaffold.mk | 新增 | 模块/项目生成、生成器防腐、应用身份生成和核对 |
| make/migration.mk | 新增 | 迁移命令、动态名称/环境占位及迁移验证 |
| make/release.mk | 新增 | 发布门禁的顺序编排、tag 入口和发布接线验证 |
| scripts/check-make-entries.sh | 修改 | 隔离夹具同时复制六个子文件，并补充拆分兼容场景；保留原清理与集成发现测试 |
| README.md | 修改 | 在命令说明附近增加文件职责和“仍从根 Makefile 调用”的说明；保留既有命令及门禁说明 |

project.mk、security/toolchain.mk、scripts/README.md、其他 scripts 文件、.github/workflows/check.yml、Dockerfile、.dockerignore、.gitignore、.gitattributes、Go 文件和 go.mod/go.sum **不改**。本文在实施阶段只追加授权、状态及实际结果。

## 5. 全部静态目标归属

计数以不同的静态普通目标为单位；同名的条件定义或端口重复定义不重复计数。动态 MIGRATION_NAME 另见第 8 节。

### 5.1 根 Makefile：8 个目标与兼容规则

| 目标 | 原行号 | 保留原因 |
|---|---|---|
| dev | 34–39 | 第一普通目标；原样保留按 migrate-% 切换 server 与空操作的配方 |
| run | 42、46–47 | 普通调用依赖 dev；迁移参数中为空操作，条件块整体保留 |
| lint | 43、46–47 | 普通调用依赖 tool；迁移参数中为空操作，不能成为无条件 lint |
| test | 44、46–47 | 普通调用依赖 test-race；迁移参数中为空操作，不能误跑测试 |
| port | 57–58、412–413 | 两处配方均留根文件；保持前后位置关系及最终以后置版本生效 |
| kill-port | 61–68、416–423 | 同上；不能把最终普通 kill 改成早期 kill -9，也不真实执行它来验证 |
| check | 377 | 原依赖列表和顺序原样保留，继续作为用户与文档的聚合入口 |
| check-ci | 379 | 原样依赖 check 和 check-generator，不增加递归 make |

端口规则保留在根文件是兼容例外，不代表后续鼓励重复定义。它们与动态迁移目标存在先后覆盖关系，本次不能为“分类整齐”改变此关系。

### 5.2 make/build.mk：3 个目标

| 目标 | 原行号 | 配方契约 |
|---|---|---|
| clean | 50–51 | 继续调用现有 clean.go，三个路径/身份参数及安全边界不变 |
| build | 71–73 | mkdir、Linux 编译环境、BUILD_FLAGS、产物名及成功后 upx -9 不变 |
| image | 198–208 | Docker build 参数、GOPROXY 获取方式、镜像 tag 表达式不变 |

### 5.3 make/dev.mk：8 个目标

| 目标 | 原行号 | 配方契约 |
|---|---|---|
| hooks | 244–249 | 仍调用 install-hooks.sh；不自动安装钩子 |
| dev-up | 274–276 | .env 存在性检查和 compose up -d --wait 不变 |
| dev-down | 278–279 | compose down；不增加卷删除 |
| check-compose-app | 281–282 | Python 接线检查及 APP_NAME 参数不变 |
| dev-app-up | 284–287 | 原 build --pull=false 与 up --pull never 的顺序及环境注入不变 |
| dev-app-up-existing | 289–292 | 保留 --no-deps 与原有参数，继续复用依赖 |
| dev-app-down | 294–296 | 依次 stop app、rm -f app，不触碰依赖服务 |
| dev-reset | 298–302 | 提示、yes 确认及 compose down -v 原样保留；本任务不实际执行 |

### 5.4 make/quality.mk：19 个目标

| 目标 | 原行号 | 配方契约 |
|---|---|---|
| check-make-entries | 53–54 | 继续调用同名隔离验证脚本 |
| tool | 117–142 | 固定版本校验、全仓 lint、五组脚本、孤立测试文件拒绝及失败即退出全部保留 |
| vet | 144–145 | 原 tags 和 GO_TARGETS 不变 |
| check-modern | 147–173 | go fix -diff、输出判据、提示和退出逻辑原样保留 |
| test-race | 175–176 | 原 tags、-race、-count=1、-timeout=5m 不变 |
| test-integration | 184–196 | 空发现列表拒绝、PLUGIN_INTEGRATION 注入及两步测试顺序不变 |
| test-coverage | 210–211 | ALLOW_NO_LOOPBACK、GO_BUILD_TAGS、profile 与目标参数不变 |
| check-coverage | 213–214 | 仍先依赖 test-coverage，再校验相同门槛 |
| check-architecture | 216–217 | 同名脚本原样调用 |
| check-hooks | 251–252 | 同名脚本原样调用 |
| check-build-context | 254–258 | 同名脚本原样调用 |
| security-source | 304–305 | security-gate.sh source，不升级工具、不改变扫描范围 |
| security-release | 307–308 | security-gate.sh release，不省略发布扫描 |
| check-package-vars | 310–311 | GOWORK=off，原 internal/cmd 参数不变 |
| check-doc-refs | 313–318 | 自检在前、实检在后，GOWORK 和参数不变 |
| check-tidy | 321–324 | 原 go mod tidy -diff 不变，不替换为写入式 tidy |
| check-security-eval | 326–327 | 原 security-eval.go --self-test 不变 |
| check-image-digests | 329–331 | 自检和实检两步顺序不变 |
| check-restricted | 333–334 | 原脚本不变；验证本次拆分时不运行真实容器 |

security-source/security-release 是扫描目标，放 quality.mk 且在动态迁移占位前定义；make/release.mk 只负责发布顺序编排。这样既保留职责区分，也保留原定义先后。

### 5.5 make/scaffold.mk：7 个目标

| 目标 | 原行号 | 配方契约 |
|---|---|---|
| check-capability-inventory | 219–224 | 自检、实检顺序和原脚本不变 |
| check-scaffold-isolation | 231–232 | 保留不带 tag 的指定 cmd 测试命令；本轮及未授权验收不实际执行 |
| module | 236–238 | NAME 校验、GO_BUILD_TAGS、位置参数及 ARGS 展开不变 |
| project-init | 240–242 | NAME 校验、--name 参数及 ARGS 展开不变 |
| check-generator | 260–263 | --self-test、--verify-scope、正式检查的顺序不变 |
| gen-appmeta | 271–272 | 原 gen-appmeta.sh 调用不变 |
| check-appmeta | 336–337 | 原 gen-appmeta.sh --check 调用不变 |

### 5.6 make/migration.mk：8 个目标

| 目标 | 原行号 | 配方契约 |
|---|---|---|
| migrate-verify | 265–266 | 既有真实迁移验证脚本不变；本次不运行 |
| check-migration-files | 268–269 | 保留 database/migrations 参数 |
| prod | 342–343 | 环境占位空操作原样保留 |
| migrate-migration | 354–356 | 名称缺失失败、位置参数和 --from-model 不变 |
| migrate-lint | 358–359 | MIGRATE migrate lint --strict 不变 |
| migrate-pretend | 361–362 | 原命令不变 |
| migrate-up | 364–372 | dev 走源码；其他环境走原二进制并要求 yes，提示和分支不变 |
| migrate-status | 374–375 | 原命令不变 |

同文件内严格保留：migrate-verify/check-migration-files → MIGRATE → prod → MIGRATION_NAME 条件及 eval → migrate-* 命令的次序。

### 5.7 make/release.mk：3 个目标

| 目标 | 原行号 | 配方契约 |
|---|---|---|
| release-check | 381–398 | 既有六个递归 make 调用、顺序及两项集成开关原样保留 |
| tag | 401–402 | 继续调用 release.sh，不将发布逻辑重新内联到 Makefile |
| check-release-wiring | 404–408 | 既有 no-op 检测和 release.sh --self-test 不变 |

归属数量：根文件 8 + build 3 + dev 8 + quality 19 + scaffold 7 + migration 8 + release 3 = **56**。这是源码清单的归类，不是运行验收结果。

## 6. 根文件结构与加载顺序

根文件按以下顺序组织，不使用通配符 include、-include、生成式 include 或为子文件新增递归 make：

1. 原 .PHONY 声明、project.mk、security/toolchain.mk，顺序不变。
2. 第 7 节的 17 个公共变量及原注释；变量相对顺序、运算符和表达式保持不变。
3. 原 dev/run/lint/test 条件块（原 34–48 行），dev 仍是第一普通目标。
4. 原早期 port/kill-port 配方（原 56–68 行）。
5. 按固定顺序加载前五个子文件：

```make
include make/build.mk
include make/dev.mk
include make/quality.mk
include make/scaffold.mk
include make/migration.mk
```

6. 原 check 与 check-ci 两个聚合目标。
7. 加载发布编排：

```make
include make/release.mk
```

8. 原后置 port/kill-port 配方（原 411–423 行），继续最后生效。

所有子文件共享根 Makefile 的变量与目标空间，只能由根入口包含，不承诺单独执行 make -f 子文件。继续支持从项目根运行命令和 make -C 项目目录；不新增自动 cd，不改变相对路径的解析基点。

不新增 .DEFAULT_GOAL 赋值：通过保留第一目标 dev 实现原默认行为，避免额外改变变量来源。保留 .PHONY 原清单，不将当前非 phony 目标顺手补成 phony；文件同名时的行为也属于兼容范围。

新增子文件会自然出现在 Make 的 MAKEFILE_LIST 中，错误/警告位置会带新的文件名和行号，这是拆分必然产生的诊断元数据变化；不要求诊断位置字节一致。命令、提示文本、退出码及实际副作用不因此改变。

## 7. 变量归属与覆盖契约

不把 `=` 改成 `:=` 或 `?=`，不引入 override、export、unexport、.ONESHELL、SHELL、.SHELLFLAGS、.NOTPARALLEL 等全局控制。调用方的命令行赋值、环境赋值和原有 make -e 行为继续由相同赋值方式决定。

### 7.1 留在根文件的 17 个公共变量

| 变量 | 原赋值 | 保留的值/表达式 |
|---|---|---|
| PORT | ?= | 8008 |
| ENV | = | `$(firstword $(filter-out $@,$(MAKECMDGOALS)))`，必须继续按当前目标求值 |
| BIN_DIR | := | ./bin |
| Linux | := | CGO_ENABLED=0 GOOS=linux GOARCH=amd64 |
| RUN | := | `go run -tags="sonic"` |
| BUILD_COMMIT | ?= | 原 git rev-parse --verify HEAD 与 unknown 回落表达式 |
| BUILD_TIME | ?= | 原 date -u 与 unknown 回落表达式 |
| BUILD_FLAGS | := | 原 tags/ldflags，包含 BuildCommit 与 BuildTime；展开时机不变 |
| COMPOSE_ENV_FILE | ?= | .env，只保留文件路径引用，不读取凭据 |
| COMPOSE_PROJECT | ?= | `$(APP_NAME)` |
| APP_COMPOSE | = | 原 docker compose 项目名、env 文件和 profile app 表达式 |
| APP_COMPOSE_ENV | = | 原 DEV_APP_NAME、DEV_BUILD_COMMIT、DEV_BUILD_TIME 注入表达式 |
| GO_TARGETS | ?= | ./... |
| LINT_TARGETS | ?= | ./... |
| GO_BUILD_TAGS | ?= | scaffold |
| COVERAGE_PROFILE | ?= | .cache/coverage.out |
| COVERAGE_MIN | ?= | 72.5，连同现有门槛说明留在根文件 |

GO_TARGETS 至 COVERAGE_MIN 可放到根公共变量区，因其间只有目标定义而无改变这些值的赋值；不调整公共变量之间的相对顺序。BUILD_FLAGS 必须仍在 BUILD_COMMIT/BUILD_TIME 之后立即展开。

### 7.2 随职责移动的 3 个变量

| 变量 | 去向 | 原表达式/时机 |
|---|---|---|
| INTEGRATION_PKGS | make/quality.mk，test-integration 之前 | 保留 `=`；原 grep → dir → patsubst → sort 的动态发现表达式完整搬移，仍分别扫描 internal、pkg、cmd 三个目录 |
| MIGRATE | make/migration.mk，prod 之前 | 保留 `=`；dev 使用 RUN，否则 BIN_DIR/APP_NAME，不能固定成某一环境 |
| MIGRATION_NAME | make/migration.mk，原双层 ifneq 内 | 保留 `:=` 和原过滤规则，只在请求 migrate-migration 时求值；随后执行原 eval |

project.mk 的 APP_NAME/APP_DISPLAY_NAME，toolchain.mk 的工具版本、镜像摘要与 SECURITY_CACHE 均原地保留；不复制值到子文件。NAME、ARGS、ALLOW_NO_LOOPBACK 等仅作为现有外部输入使用，不新增默认值。原行内环境变量、单引号/双引号、`$$`、续行与 `&&`/`||` 必须逐字保留。

## 8. 必须保持的特殊执行行为

### 8.1 迁移参数与入口别名

- 当 MAKECMDGOALS 不含 migrate-% 时，run 依赖 dev、lint 依赖 tool、test 依赖 test-race。
- 当含 migrate-% 时，dev 和 run/lint/test 按原条件为空操作，避免把 dev/test 环境名解释成启动或测试命令。
- prod 继续为空操作。MIGRATION_NAME 从原过滤表达式产生，仍通过 eval 增加对应名称目标；不增加名称校验或更改过滤词。
- 原来的多目标命令、目标顺序和 ENV 中的 $@ 保持不变，不使用新一层递归 make 重算参数。

### 8.2 动态名称与同名目标的先后关系

动态迁移名称可能与已有目标重名，不能只验证常见 create_xxx 名称：

| 定义阶段 | 目标范围 | 必须保持的关系 |
|---|---|---|
| 动态 eval 之前 | 入口条件块、早期端口、build/dev/quality/scaffold 规则、迁移验证与 prod | 保留其先定义、eval 后定义的关系 |
| 动态 eval 本身 | MIGRATION_NAME | 表达式及空操作配方不变 |
| 动态 eval 之后 | migrate-* 命令、check/check-ci、release 编排、后置端口 | 继续在 eval 之后定义 |

例如动态名称为 port 时，原最后一处端口配方仍会覆盖动态占位；动态名称为 build 或 security-source 时，原配方与动态占位的先后又不同。**本次保持这些存量关系，不借拆分“修正”碰撞行为。** 第 6 节的根文件位置和 include 顺序即为此而定。

### 8.3 聚合、失败与副作用

- check 的原依赖列表和 check-ci 的两个依赖原样保留；不借声明顺序推断并行执行顺序，也不串行化 make -j。
- release-check 内的六次 `$(MAKE)` 顺序不变：check-ci → check-restricted → check-migration-files → 带 REQUIRE_DB_INTEGRATION=1/PLUGIN_INTEGRATION=1 的 test-integration → migrate-verify → security-release。
- 保留 @、制表符、续行、子 shell 边界及失败即退出。尤其 tool 的主 lint 失败不得继续，五组脚本不得漏掉配套测试或吞掉退出码。
- 保留 build 的成功后压缩、clean 的路径约束、dev-reset/migrate-up 的用户确认，以及现有 Docker 的 --pull=false/--pull never 等参数。
- 两套端口配方均保留：后置 lsof 的“无监听”回落和普通 kill 仍最终生效。既有重复目标警告可以更换文件行号，但本次不静默移除定义。

## 9. 外部耦合点与配套处理

| 已读取的耦合点 | 拆分影响 | 明确处理 |
|---|---|---|
| scripts/check-make-entries.sh:27 只复制根 Makefile | 隔离目录会缺少 include 的六个文件 | 在原夹具创建后同步复制整个受管 make/ 规则目录到夹具；保留带空格的 fixture 路径和现有退出清理 |
| 同脚本设置独立 project.mk、toolchain.mk 和 stub PATH | 需要继续证明调用原入口且没有真实外部副作用 | 不把真实 project.mk/配置复制进夹具；保留版本注入、原 Go/lint stub 与真实 cleaner 测试 |
| 同脚本的实际 fixture Go 集成测试 | 改测试入口可能意外绕过原发现/失败断言 | 保留全部原用例与开关；只添加第 11 节所列的拆分兼容用例 |
| README.md 的 check、COVERAGE_MIN 权威位置说明 | 若移走权威配置，文档会失真 | check/check-ci、COVERAGE_MIN 留根；只补文件职责说明 |
| .github/workflows/check.yml 和 install-hooks.sh | 继续调用 make check-ci | 入口不变，不改 workflow 或 hook 安装逻辑，也不安装钩子 |
| internal/projectinit/init.go:runMake | 以项目根为 cwd 执行 gen-appmeta/check-appmeta/check | 目标名、cwd、环境和原文件变换逻辑不变，不改 Go 文件 |
| internal/projectinit/init_test.go 的自有最小 Makefile | 它不是复制真实根 Makefile 的夹具 | 原样保留，不强行要求这个最小夹具包含六个新文件 |
| check-generator.sh:export_worktree、security-gate.sh:export_release_candidate | 导出已跟踪和未忽略的未跟踪文件，并过滤已不存在路径 | 新 make/ 文件不在现有 .gitignore 排除范围，沿用既有导出机制；本轮不运行导出/安全扫描，也不修改这些脚本 |
| Dockerfile 与 .dockerignore | 镜像直接 go build，不调用 Makefile；构建上下文为白名单 | 不将 make/ 加入镜像上下文，不扩大白名单，不构建镜像 |
| check-image-digests.sh 的实际 run_check | 实际扫描固定 toolchain.mk，镜像声明不迁移 | 不改扫描器，不借其泛化注释增加本次工作；子文件不新增镜像字面值 |
| .gitattributes 的 LF 规则 | 新 .mk 应保持 tab 与 LF | 沿用现有规则，不改全局格式配置 |
| scripts/release.sh 的 dry-run 风险说明 | 含 `$(MAKE)` 的配方在 -n 下可能仍被执行 | 不对真实仓库批量执行所有目标的 make -n；危险目标只做配方/依赖静态比较或完全隔离的替身验证 |

本轮只读导航曾把导出函数误当成独立 scripts/export-worktree.sh，工具报告不存在。已按实际文件内容确认函数分别在 check-generator.sh 和 security-gate.sh；未创建替代脚本或据错误路径制定改动。原因是未先核对文件清单；防复发要求是后续按已读定义定位。该编制记录只写入本文，不借纯文档任务修改错误日志或其他文件。

## 10. 分阶段实施计划

P0–P3 已完成，P1.2 的独立工具版本查询保留未执行；P4 获得本轮明确授权后完成，P5 已更新实际结果。仅执行本次批准的三项检查，实际 lint 入口已完成 golangci-lint 2.15.0 版本校验。

| 阶段/步骤 | 前置条件 | 具体工作 | 检查方法与可复核交付物 | 停止条件 | 当前状态 |
|---|---|---|---|---|---|
| P0.1 现状核对 | 仅方案授权 | 读取规则、错误记录、完整源码及必要调用方 | 本文的指纹、56 目标、20 变量与耦合清单；不运行 Make | 来源缺失或与请求冲突 | 已完成只读核对 |
| P0.2 方案落盘 | P0.1 完整 | 新增本文并回读 | 完整方案与实施清单 | 未写全归属、兼容或授权边界 | 已完成，用户已批准实施 |
| P1.1 基线保护 | 用户明确批准本方案实施 | 重读本文和最新规则，核对当前差异；保存根 Makefile、入口测试脚本、README 及完整状态的工作区快照 | 原文件副本、SHA256、原规则/变量清单；不从 HEAD 恢复 | 有未经核对的并行修改或目录已有其他内容 | 已完成；三文件快照回读逐字一致 |
| P1.2 环境和范围 | P1.1 一致 | 只读确认本机 Make/Go/lint 入口及版本、原 make/ 目录是否存在；核对九文件范围 | 版本和范围记录，六个目标路径没有覆盖风险 | 工具不支持当前既有语法，或目标路径冲突 | 九文件及目录已核对；实际工具版本查询未执行，见 15.3 |
| P2.1 原样抽取 | P1 完成 | 按第 5 节建立六个 .mk，连同归属注释逐块搬移；不改配方内容 | 原块到新块的对应 diff，56 目标全覆盖 | 任一配方/参数/赋值需要额外行为设计 | 已完成；六文件已回读 |
| P2.2 根入口接线 | P2.1 完成 | 按第 6 节组织公共变量、条件入口、两套端口和固定 include 顺序；保留 check/check-ci | 根文件与六文件整体的定义先后清单 | 默认目标、eval 前后或聚合依赖改变 | 已完成；根文件 126 行 |
| P2.3 配套夹具和文档 | P2.2 完成 | 入口夹具复制 make/，添加必要兼容场景；README 加文件职责说明 | 只涉及第 4 节九个文件及本文的 diff | 需要改其他检查器、业务代码或 CI | 已完成；新用例尚未运行 |
| P3.1 静态交叉复核 | P2 全部完成 | 逐项核对目标、变量运算符、配方、阶段顺序和原脚本断言 | 第 5–9 节逐项对照记录；属于读文件，不启动目标 | 56 目标漏项、20 变量变化、旧断言被删除 | 已完成源文本对照；不代替运行验证 |
| P3.2 功能实现完成点 | P3.1 一致 | 记录已完成补丁、未运行项并汇报，单独询问接线验证及 lint 授权 | 可评审 diff、范围报告与检查申请 | 未获新的检查授权 | 已完成；P4 获得本轮单独批准 |
| P4.1 接线运行验证 | 功能完成，且用户明确允许包含受控子步骤的入口验证脚本 | 执行 bash scripts/check-make-entries.sh；按第 11 节保留/新增的用例验证 | 完整日志、退出码、实际 argv、故障场景 | 同一问题三次修正仍失败、越界调用或真实外部副作用 | 通过，exit 0，18.611s；补充定向回归也已通过 |
| P4.2 lint 等检查 | 功能完成，且用户逐项明确允许 | 只运行用户选择的检查，如 make lint；不默认执行 check/check-ci | 命令、版本、退出码、范围；失败仅修本次引入的问题 | 出现存量问题或需要扩大范围 | make lint 通过，exit 0，5.586s；全仓及五组脚本均 0 issues |
| P5 结果落盘 | 已授权步骤结束 | 更新本文的实际结果、未验证项、风险与回滚记录 | 不将未运行标为通过，不把静态等价称为真实外部验收 | 证据不足时保留未验证 | 已完成本轮结果落盘；未授权项见第 17 节 |

实现期间不提交、不推送、不发布。需要新增范围时先更新方案并取得对应授权，不能先改后补。

## 11. 拟验收矩阵与运行边界

### 11.1 静态验收（后续实施完成后读文件核对）

- 56 个静态目标各有明确归属，原条件定义和两组端口定义完整保留。
- .PHONY 原集合、check/check-ci 原依赖列表、release-check 六个调用顺序不变。
- 20 个变量的值、表达式、赋值运算符和求值阶段与第 7 节一致。
- 原配方及 shell 文本逐块对应；仅新增 include 和必要的夹具复制/兼容断言。
- 六个子文件存在、使用 tab/LF，根文件显式 include；不添加忽略规则或改变工具版本。
- 原有未提交改动与本文范围外文件保持原状；README 和 root 权威位置一致。

### 11.2 运行验收（仅在 P4 对应授权之后）

| 用例组 | 方法及必须保留的结果 |
|---|---|
| 默认入口 | 在原带空格路径的隔离夹具中执行无目标 make；只通过原 Go stub 记录一次 server 调用，默认目标仍为 dev；不启动真实服务 |
| 普通别名 | 保留原 run/lint/test 用例，核对 server、全仓 lint、test-race 的原 argv；lint 不省略固定版本校验与脚本分组 |
| 迁移环境 | 保留 migrate-status dev/test 用例，并补齐 prod、run/lint 环境占位；不得误启动 server 或普通测试 |
| 动态名称 | 保留普通名称、空名称失败；比较 name 与 build、port、check、release-check 等同名时的目标/配方元数据。只读元数据，不运行这些碰撞目标的危险操作 |
| 变量覆盖 | 在隔离夹具用固定 BUILD_COMMIT/BUILD_TIME，核对命令行 GO_TARGETS/LINT_TARGETS/GO_BUILD_TAGS/NAME/ARGS/BIN_DIR 等；用只读查询目标比较 20 变量的 origin、flavor、value/展开结果；覆盖环境赋值、命令行赋值、空字符串及原有 -e 行为 |
| 条件与工作目录 | 比较普通/迁移 MAKECMDGOALS 的目标依赖与 ENV 展开；保持 make -C 与根目录调用，不新增自动切目录 |
| include 必需性 | 在隔离副本中缺失一个必需 .mk，解析必须失败，不静默跳过；不删除真实工作区文件 |
| 失败传播 | stub 注入主 lint 非零、脚本非零；主 lint 失败不得继续脚本，脚本失败不得输出成功标记；make 最终非零 |
| 端口规则 | 只比较原两套配方和最终有效配方；如验证 lsof 调用，仅使用返回空 PID 的替身，绝不真实 kill |
| 既有测试 | 保留真实 cleaner 的受管路径、symlink 与拒绝越界断言；保留 fixture 集成发现/显式开关/强制失败场景，不修改真实 cmd 代码或执行其超时用例 |
| 全仓 lint | 另行获准后执行原 make lint，包括五组脚本；报告真实结果，不用 stub 结果代替 |

原 check-make-entries.sh 虽使用 Go/lint stub，仍会经 make 调用 lint/test 入口，并有使用真实本机 Go 的 fixture 集成测试及 -race 参数。因此把整个脚本放在 P4.1 单独授权，不能借“只是夹具”提前执行。它的 cmd 文件由脚本在临时模块中生成，不是项目 cmd 既有超时测试。

不对真实仓库运行所有目标的 make -n：带 `$(MAKE)` 的配方存在实际执行风险。build/image/dev-up/dev-reset/migrate-up/tag/release-check 等真实副作用不作为本次拆分的运行验收动作；以原配方、目标图、变量与隔离替身证明接线兼容，明确不宣称 Docker、数据库、部署或发布端到端验证。

除非用户另行明确授权，不执行 make check、make check-ci、独立 vet、modern、全量 race、覆盖率、构建、集成依赖、生成器或容器验证。本轮不请求这些检查授权。

## 12. 风险、失败处理与回滚

| 风险/触发条件 | 影响 | 处理方式 |
|---|---|---|
| include 或根规则顺序变化 | 默认目标或动态同名规则生效变化 | 固定第 6 节阶段顺序；有差异即停止，不通过删旧定义掩盖 |
| 变量赋值/展开时机改变 | 参数、环境、构建身份或覆盖率门槛漂移 | 运算符和表达式逐项核对，失败只恢复本次迁移块 |
| 夹具漏复制子文件 | 原入口测试无法解析 Makefile | 同阶段修改复制接线；缺文件反例验证，不允许 -include |
| 在真实仓库验证危险目标 | 误启动服务、创建容器、删除数据或发布 | 只读比较或隔离替身；不把 -n 视为无条件安全 |
| 整理时顺带“修复”历史问题 | 偏离保留行为的范围 | 原重复端口、注释和门禁保持；另记未处理，不扩大修改 |
| 其他任务并行改动 | 覆盖用户代码或旧日志迁移成果 | 实施前/交付前对比工作区快照；冲突先说明，不整仓还原 |

回滚以 P1 的当前工作区副本为准：

1. 停止后续步骤；记录失败命令、退出码及失败时已修改的文件。
2. 确认九个文件没有新的外部并行改动后，只撤销本次 Makefile、入口脚本、README 差异。
3. 只删除本次创建且未被其他工作修改的六个 .mk；make/ 有其他内容时不得删除整个目录。
4. 回读恢复后的根 Makefile、脚本和 README；已有端口改动、日志迁移及本轮之前的工作必须保留。
5. 本文保留实际回滚记录。回滚不代表检查已通过，也不触发未经授权的补跑。

不使用 git reset --hard、git clean 或从 HEAD 覆盖当前工作区；无数据库、存储、依赖或线上配置迁移需要回滚。

## 13. 确认项与本轮交付

用户已确认的完整实施方案包含：

1. 新增 make/ 下六个 .mk，并按第 5 节归属搬移。
2. 根文件继续保留默认/条件入口、公共变量、聚合目标和前后两套端口兼容定义。
3. 允许同步修改入口测试脚本的复制与兼容用例，以及 README 的职责说明。
4. 功能实施与受控运行验收分阶段授权；不处理 cmd 超时和其他存量问题。

方案阶段交付为本文，源码与耦合点的只读梳理已完成。第 14 节保留工作流准备历史；**本轮产品修改已完成，全部运行验收仍未执行**，详见第 15 节。不将上一任务 lint 通过作为本方案已验证的证据。

## 14. 本轮实施授权及工作流准备

用户已明确批准本方案实施，产品文件范围仍为第 4 节九个文件，受控检查仍未授权。当前运行端提供的真实会话为 `01a166ec-68c6-7b50-add3-73ccf788d436`。workflow.py status 实际返回 mode=plan、implementation=false、plan=null；读取原计划又返回缺少契约标记。源码核对表明，当前意图分类器把“只改方案列出的文件”匹配为 plan_only，覆盖了同一句的“实施”。这不是用户未批准，不能据此让用户重新设计或扩大范围。

依本轮运行端要求，新建独立 change `modularize-makefile-targets`，不使用任何现有会话的 change。新增的 proposal/design/spec/tasks 为工作流文档，不是业务实现文件；没有修改分类器、运行器、用户配置或原生状态。以下登记只提供精确范围与候选检查，不授予检查执行权。

运行器目前要求至少一个 functional 条目，因此登记既有 `TestRunStandardInstantiation` 作为项目初始化调用方的补充回归，执行仍放在 P4 单独确认之后。该测试使用自有夹具，不能替代第 11 节的真实 Makefile 接线验证。原计划 Bash 入口暂不受当前 argv 运行器支持，未运行，也不借换名、改 kind 或解释器包裹绕过；这一限制保留在 P4 的未验证项中。

<!-- harness-contract:v1 -->
```json
{
  "schema_version": 1,
  "stage": "makefile-split-implementation",
  "change": "modularize-makefile-targets",
  "allowed_files": [
    {"path": "Makefile", "kind": "code"},
    {"path": "make/build.mk", "kind": "code"},
    {"path": "make/dev.mk", "kind": "code"},
    {"path": "make/quality.mk", "kind": "code"},
    {"path": "make/scaffold.mk", "kind": "code"},
    {"path": "make/migration.mk", "kind": "code"},
    {"path": "make/release.mk", "kind": "code"},
    {"path": "scripts/check-make-entries.sh", "kind": "code"},
    {"path": "README.md", "kind": "document"},
    {"path": ".cache/makefile-split-20261023-01a166ec/baseline.json", "kind": "document"},
    {"path": "docs/makefile-modularization-plan-2026-10-23.md", "kind": "document"},
    {"path": "openspec/changes/modularize-makefile-targets/proposal.md", "kind": "document"},
    {"path": "openspec/changes/modularize-makefile-targets/design.md", "kind": "document"},
    {"path": "openspec/changes/modularize-makefile-targets/specs/makefile-modular-layout/spec.md", "kind": "document"},
    {"path": "openspec/changes/modularize-makefile-targets/tasks.md", "kind": "document"}
  ],
  "required_context": [
    {"path": "AGENTS.md", "kind": "rules", "name": "project-rules"},
    {"path": ".harness/guides/workflow-contract.md", "kind": "guide", "name": "workflow-contract"},
    {"path": ".harness/guides/ci-sensors.md", "kind": "guide", "name": "ci-sensors"},
    {"path": ".agents/skills/harness-implement/SKILL.md", "kind": "skill", "name": "harness-implement"},
    {"path": "/Users/xiaozhaofu/.agents/skills/shell-scripting/SKILL.md", "kind": "skill", "name": "shell-scripting"},
    {"path": "/Users/xiaozhaofu/.agents/skills/go-engineering-governance/SKILL.md", "kind": "skill", "name": "go-engineering-governance"}
  ],
  "checks": [
    {
      "id": "project-init-caller-regression",
      "kind": "functional",
      "argv": ["go", "test", "-vet=off", "-tags", "scaffold", "-count=1", "-timeout=3m", "-run", "^TestRunStandardInstantiation$", "./internal/projectinit"],
      "timeout_seconds": 240
    },
    {
      "id": "make-lint",
      "kind": "lint",
      "argv": ["make", "lint"],
      "timeout_seconds": 300
    },
    {
      "id": "change-schema",
      "kind": "schema",
      "argv": ["python3", ".openspec-auto/tools/run_openspec.py", "validate", "modularize-makefile-targets", "--type", "change", "--strict", "--json", "--no-interactive"],
      "timeout_seconds": 120
    }
  ],
  "regressions": []
}
```

上面的新增文档登记和补充检查条目均不改变原实施步骤、九个产品文件范围或后置授权边界。

### 14.1 本轮实际结果与停止点

记录时间：2026-10-23 14:43（Asia/Taipei）。

| 实际操作 | 结果 | 证据与限制 |
|---|---|---|
| 使用当前会话 status 查询 | exit 0；mode=plan、implementation=false、plan=null | 使用运行端提供的会话 ID，未使用其他会话状态 |
| 新建 modularize-makefile-targets 并准备 proposal/design/spec/tasks | 已完成文档准备 | 独立 change 的 status 显示四类制品 done；仅表示制品准备齐全，不表示产品实现或检查通过 |
| 按 workflow.py read 完整读取本文及六项 required_context | 全部 exit 0；读取回执已记录 | 后续本文只更新结果文字；恢复实施时须重新读取当前文本 |
| `python3 .harness/scripts/workflow.py --session 01a166ec-68c6-7b50-add3-73ccf788d436 activate --plan docs/makefile-modularization-plan-2026-10-23.md` | **exit 1** | 原文：`[workflow] current user request has not authorized implementation` |
| 激活失败后 status 查询 | exit 0；仍为 plan=null、implementation=false，edits=[]、results={} | 没有产品代码编辑或检查执行回执 |
| 当前会话 checkpoint | exit 0；complete=false | 已保存阻断原因和未完成状态，未调用 ready/finish |
| P1–P3 | 未执行 | 用户批准有效；停止原因是本地请求分类和激活限制，不能写成用户未授权 |
| P4、构建、cmd 回归 | 未执行 | 受控检查继续等待功能完成后的单独授权；cmd 既有超时未处理 |

本次停在激活前，不调用 ready/finish，不手改状态、不重放请求、不修改分类器或用其他执行入口绕过。为刷新原生请求记录，需要用户发送不含歧义片段的新指令，例如：“开始实施 Makefile 分类拆分，按已批准文件清单执行；受控检查稍后单独确认，cmd 超时不处理。”这只解决运行端请求记录，不要求重新设计或重新选择方案。恢复后仍从 P1 开始，不能跳过基线保护。

本轮准备过程中发生的命令格式拒绝、缺失路径、契约标记缺失与本次激活失败，按实际输出记录于 `.harness/error-journal.md` 的 ERR-20261023-135、ERR-20261023-136；不作为任何验收通过证据。

## 15. 恢复实施及本轮结果

用户已重新明确指令“开始实施 Makefile 分类拆分”，本轮实际请求状态为 code / implementation=true，granted_checks=[]。按当前会话读取原计划及 required_context 后，activate 已返回 exit 0，绑定独立 change `modularize-makefile-targets`。第 14 节保留上一轮的历史失败，不代表本轮仍被阻断。

P1 按原计划保存精确工作区快照；为避免将备份当成第二套可执行入口，使用单个 `.cache/makefile-split-20261023-01a166ec/baseline.json` 保存三份文件的原始 UTF-8 内容、SHA256 与 git status。该文件仅为 P1 已批准的回滚证据，不增加产品改动范围。其路径登记到工作流契约，不改变九个实施文件或检查权限。

实施前读取结果：Makefile 423 行，SHA256 与第 2 节一致；scripts/check-make-entries.sh 120 行；README.md 295 行；make/ 尚不存在。脚本 SHA256 为 `0c5837dc0a6a437fd7f9a511eae04088a18daf52320c997bdffab05c7b3e273f`，README SHA256 为 `53f4755dd7cfdceac84ddac4ce5f5b53c2d067bf12b0e3832336b23f2b7daf3b`。快照文件已写入并回读，三份内容与实际原件逐字一致。

### 15.1 已完成的修改

| 文件 | 实际结果 |
|---|---|
| Makefile | 126 行；保留 17 个公共变量、dev 首目标、run/lint/test 条件分支、两套端口定义、check/check-ci；按第 6 节显式 include |
| make/build.mk | 24 行，clean/build/image 共 3 个目标 |
| make/dev.mk | 36 行，开发及 hooks 共 8 个目标 |
| make/quality.mk | 133 行，检查与安全扫描共 19 个目标；INTEGRATION_PKGS 随规则保留 |
| make/scaffold.mk | 35 行，生成器及身份操作共 7 个目标 |
| make/migration.mk | 43 行，共 8 个目标；MIGRATE、MIGRATION_NAME 及 eval 先后关系不变 |
| make/release.mk | 29 行，共 3 个目标；六次递归调用和两项集成开关不变 |
| scripts/check-make-entries.sh | 242 行；复制 make/，新增默认入口、变量来源/展开、迁移名称碰撞、缺少 include、lint 失败传播用例；原 120 行保持原顺序，包含原 cleaner 和真实 fixture 集成测试 |
| README.md | 新增 14 行，列明根入口与六文件职责；原文字保持原顺序 |

第 5.7 节原行号 404–408 少计了第 409 行的 release.sh --self-test；本轮按真实源码复制至第 409 行，原配方完整保留，没有删去该命令。除此之外没有重排已批准的目标归属或实施阶段。

产品修改限于上述九个文件。方案、当前独立 change 文档和 P1 回滚快照属于相应记录；没有修改 Go 文件、工具版本、CI、Docker、其他脚本或 cmd 超时逻辑，没有执行容器、数据库、服务启动或发布操作。

### 15.2 静态复核的真实结果

本节仅为读取文件和源文本对照，不是 Make/Go/Bash 测试执行结果。

- 根文件和六个子文件回读内容，与按原文件区段生成的预期内容逐字一致；原 423 行中每一条非空行均恰好归属一个输出文件，没有丢失或重复搬移。
- 静态目标集合仍为 56 个：8 + 3 + 8 + 19 + 7 + 8 + 3，与原集合一致。
- 20 条变量赋值的完整文本集合一致；赋值运算符、表达式和字符串未修改。17 条公共变量留根文件，3 条专用变量按计划归属。
- .PHONY 整行不变；dev 仍是首个普通目标；project.mk、toolchain.mk 及六个分类 include 顺序符合第 6 节。
- 两个 port 与两个 kill-port 定义均在根文件，位置分处分类 include 前后；动态 eval 与后置聚合/发布规则的先后关系保留。
- release-check 六条递归调用逐行保留，包含 REQUIRE_DB_INTEGRATION=1、PLUGIN_INTEGRATION=1；check/check-ci 原行原样保留。
- 原脚本 120 行与 README 295 行均仍按原顺序存在；原断言未删除，新用例使用独立夹具和替身。
- 迁移目标元数据用例仅执行夹具新增的 __inspect 目标，通过 MAKECMDGOALS 变量触发待观察的解析分支；不对危险目标运行 -n/-q。真实执行的迁移调用由原 Go/binary stub 记录，不连接数据库。

### 15.3 未执行项与交付限制

| 项目 | 当前结果 |
|---|---|
| Make 默认入口、变量 origin/flavor、动态碰撞和失败传播 | 已添加对应夹具断言，**尚未运行** |
| bash scripts/check-make-entries.sh | **未执行**；包含真实 fixture Go 集成与 -race，仍按 P4.1 单独授权 |
| 项目初始化定向回归 | **未执行**，仍按 P4 授权；不以它替代 Make 接线验证 |
| make lint（含脚本 lint） | **未执行**；没有复用上一任务通过结果 |
| vet、modern、全量 race、覆盖率、make check/check-ci | **未执行**，不推导权限 |
| Make/Go/golangci-lint 实际版本查询 | **未执行**；当前工作流命令白名单未开放独立版本查询，不用其他执行入口绕过；仅核对项目锁定的 Go 1.27.3 / golangci-lint 2.15.0 声明 |
| Bash 语法检查、构建、OpenSpec strict validate | **未执行**；不将静态源文本对照描述成这些检查通过 |
| cmd 既有超时 | 未处理，未执行其回归 |

当前交付状态为“实现及静态复核完成，运行验收待授权”。由于 P4 功能检查尚未运行，未调用 workflow ready/finish，也不归档当前 change；通过 checkpoint 保存这个未完成验证状态。

回滚仍按第 12 节进行：从 baseline.json 提取三份原始 text，核对没有新的外部改动后仅恢复本次差异、移除本次六个 .mk。快照保留原 Makefile 的未提交改动；本轮未发生回滚。

## 16. P4 首轮检查的实际结果

记录时间：2026-10-23 14:57（Asia/Taipei）。用户已明确授权隔离接线验证（含夹具 race）、项目初始化定向回归和 make lint（含脚本 lint），只修复本次拆分引入的问题，cmd 既有超时及其他受控检查继续不处理。用户授权与运行端是否登记许可分开记录，不能把后者的缺失描述成用户未同意。

### 16.1 已执行并通过：项目初始化定向回归

完整入口：

```sh
GOWORK=off GOTOOLCHAIN=local GOPROXY=off python3 .harness/scripts/workflow.py --session 01a166ec-68c6-7b50-add3-73ccf788d436 run --check project-init-caller-regression
```

运行器实际执行：

```sh
go test -vet=off -tags scaffold -count=1 -timeout=3m -run '^TestRunStandardInstantiation$' ./internal/projectinit
```

- 结果：exit 0，status=passed；实际 stdout 为 `ok github.com/gtkit/gin-api/internal/projectinit 0.841s`，stderr 为空。
- run_id：`291485a9-9362-46cd-b78e-9c3ec1a90061`。
- stdout：`.openspec-auto/state/workflow-279a505de7d4361c1c00865e30fbb91066e7b57b6adc1b08344f03f3ec866d90.291485a9-9362-46cd-b78e-9c3ec1a90061.stdout`，已实际回读。
- stderr：同前缀 `.stderr`；运行器记录空内容 SHA256。
- 该测试使用自有最小 Makefile 夹具，真实 Git 仅作用于临时目录，生成器和自检为既有替身。此通过结果仅证明该调用方契约，不能替代真实拆分 Makefile 的接线和 lint 验证。

### 16.2 原生许可与入口限制

本轮最初 status 实际为 mode=code、implementation=true、checks_requested=[lint, race, script_lint]，但 granted_checks=[]、ready=null。原因是上一轮只完成静态复核，没有先登记运行器要求的功能完成回执。本轮重新读取方案和 required_context、activate 成功，定向回归通过后，调用 workflow ready 已返回 exit 0，记录 revision=3 和当前代码指纹 `874392e3d536b71aed69022eb98b13ba1455e0f8a6d033b68784f208ea78f859`。

ready 后再次 status 仍为 granted_checks=[]。源码 record_request 只在后续新请求进入时，根据先前 ready 回执登记新的检查许可，不会回溯授予本条请求。因此没有运行 make-lint，也没有改写状态或重放请求。

另一独立限制：当前 classify_argv 将 bash/sh 等入口归为 unsupported；合同只登记了定向 Go 回归、make-lint 和 schema，没有可运行的 Bash 接线检查条目。没有把 Bash 脚本改名、包装成 Go/Python 功能检查，也没有修改运行器或扩大实现文件范围。

用户已明确回复“允许，仅本次跳过会话工作流入口限制”。按此一次性例外继续执行已经批准的两条原命令 `bash scripts/check-make-entries.sh` 和 `make lint`，保留本机进程的原始 stdout/stderr、退出码和时间；不改工作流、安全 hook、用户配置，不扩大无关检查权限。后续实际结果见第 17 节。

### 16.3 尚未执行与未完成范围

| 项目 | 实际结果 |
|---|---|
| 隔离接线脚本及其中的 fixture race | 未执行，入口限制待处理 |
| make lint 及脚本 lint | 未执行，原生检查许可尚未登记 |
| vet、modern、全量 race、覆盖率、聚合门禁 | 未执行，用户未授权本轮执行 |
| cmd 既有超时回归 | 未执行、未修改 |
| 本次拆分缺陷修复 | 本轮未修改代码；尚无运行失败证据支持修复 |

本轮 P4 尚未完成，不将上述未执行项标为通过，不调用 finish 或归档 change；后续继续时复用本轮仍有效的定向回归证据，不无故重复执行。

以上为进入一次性入口例外前的阶段记录；例外批准后的最终结果见第 17 节。

## 17. P4 入口例外后的执行记录

本次例外只用于完成第 16 节已批准的隔离接线验证及 make lint；项目初始化定向回归已通过，不重复运行。检查使用项目根目录、本机 PATH 和 `GOWORK=off GOTOOLCHAIN=local GOPROXY=off`，不下载工具、创建容器或连接真实依赖。先运行隔离脚本，读取失败原文；只修复本次拆分引入的问题，再完成已授权的 make lint。修复仍限定第 4 节文件并保留测试原义；本段是执行记录，不伪造或补写原生通过回执。

最终记录时间：2026-10-23 15:00（Asia/Taipei）。两条检查均已实际运行并通过，项目初始化定向回归沿用本轮第 16.1 节的有效通过记录；本轮没有代码修复、没有重跑其他受控检查。

### 17.1 命令、环境与结果

工作目录：`/Users/xiaozhaofu/go/src/Project/gin_api`。两条命令均通过本机进程执行，显式环境为 `GOWORK=off GOTOOLCHAIN=local GOPROXY=off`，其他本机环境保持继承；未下载工具、依赖或镜像。

| 检查 | 实际命令 | 实际结果 |
|---|---|---|
| 隔离 Make 接线 | `bash scripts/check-make-entries.sh` | exit 0；18.611s；2026-10-23 06:59:40.409–06:59:59.020 UTC |
| 项目初始化定向回归 | `go test -vet=off -tags scaffold -count=1 -timeout=3m -run '^TestRunStandardInstantiation$' ./internal/projectinit` | exit 0；Go 输出 0.841s；完整证据见 16.1 |
| 全仓及脚本 lint | `make lint` | exit 0；5.586s；2026-10-23 07:00:01.745–07:00:07.331 UTC；6 次 `0 issues.` |

证据目录：`.cache/makefile-p4-66CKwA/`。

- `commands.json`：本机实际 argv、工作目录、显式环境、起止时间、退出码与日志 SHA256；已回读确认两条记录均 passed / exit 0。
- `1-make-entries.stdout`、`1-make-entries.stderr`：完整隔离接线输出；stdout SHA256 为 `1d9519d6939121726230d0a4655a27eede7e0ac220bb1554d6a029780214e057`。
- `2-make-lint.stdout`、`2-make-lint.stderr`：完整真实 lint 输出；stdout SHA256 为 `ad7b8685a58a4fe61cda99a9ce2e4a0e7cf5aa64fc6d88c04e11900d9212fd69`。

以上是本次入口例外下的真实进程证据，没有写入或伪造 workflow run 回执。第 16.1 节定向回归继续保留原运行器回执，两类证据不混用。

### 17.2 接线验证覆盖的实际结果

- 新增默认目标、普通别名、20 变量的来源/赋值方式/原值及展开值、环境与命令行覆盖、空值、`-e`、迁移分支的断言均执行完成。
- 动态迁移名称与 build、port、check、release-check 的碰撞断言，以及普通名称、空名称拒绝、prod/run/lint 环境占位断言均执行完成。
- 缺失必需 include、主 lint 失败后不继续脚本、脚本 lint 失败后不输出成功标记，均由隔离反例验证。
- 原 cleaner 对受管文件、symlink 与越界路径的断言保留并执行完成。
- 临时模块 `example.com/fixture` 的 cmd 与 internal/fixture 实际带 `-race` 运行并通过；强制 cmd 夹具失败被 Make 门禁拒绝，删除 integration 文件后 cmd 不再出现在 race 发现列表。最终输出为 `OK: Make entry/clean boundaries; real cmd integration discovery and forced opt-in failure`。
- 所有真实 Go 集成执行均针对临时夹具，不是项目 cmd 既有超时用例；启动及迁移命令使用原 Go/binary 替身，没有启动真实服务、连接数据库或执行 kill。

### 17.3 lint 实际结果及保留项

真实 `make lint` 完成项目 golangci-lint 2.15.0 版本校验、一次全仓 `golangci-lint run ./...` 和五组独立脚本 errcheck，六次均为 `0 issues.`，最终输出 `✅ golangci-lint run`。五组源文件为 clean.go、check-doc-refs.go、check-package-vars.go、check-repository-context.go、security-eval.go；check-repository-context_test.go 按原循环与源文件一起传入。

stderr 中保留了方案要求不变的 port/kill-port 重复目标警告，以及 golangci-lint 对未命中排除规则的提示。它们没有导致退出失败，不为消除提示而改动原规则或无关代码。隔离迁移碰撞用例的覆盖警告也符合预期。

### 17.4 范围与完成状态

本次批准的三项检查已完成且通过，未发现需要修复的拆分问题，因此没有追加代码改动。本轮仅更新本方案、当前 change 的任务/状态文档和真实执行证据。

独立 vet、modern、项目全量 race、覆盖率、make check/check-ci、构建、OpenSpec strict validate、容器及真实依赖验收均未执行；cmd 既有超时没有处理。Make/Go 的独立版本查询仍未运行；仅能报告实际 lint 版本门禁通过，不能补写其他工具版本实测。

此次只跳过已明确批准的会话工作流入口限制，未修改分类器、runner、安全 hook 或用户配置。不补写原生通过回执，不调用要求额外 schema 回执的 finish，也不归档 change；用 checkpoint 保存本轮已通过的实际证据位置。上述流程状态不改变三项检查已经真实通过的事实，也不代表其他门禁已通过。

## 18. 本次是否追加完整门禁的评估

本节回应用户关于必要性和重复执行的询问，不把问句视为新增受控检查授权。本轮只更新本方案并回读，没有运行新检查、修改代码或处理 cmd 既有超时。

结论：**本次 Makefile 分类搬移不建议再重复执行独立 vet、modern、项目全量 race 和覆盖率检查。** 本次没有修改 Go 源码，检查配方原样搬移；第 15 节完成源文本对照，第 17 节已实际验证默认入口、变量覆盖、动态目标、失败传播与原有夹具，并通过真实 make lint。这些证据对应本次改动风险，不把它们扩大为全项目运行行为或生产验收证明。

| 项目 | 本次处理及理由 |
|---|---|
| 独立 vet | 不追加；本次没有新增 Go 源码或修改其检查配方 |
| modern | 不追加；没有现代 Go 写法变更，原 check-modern 配方保持不变 |
| 项目全量 race | 不追加；P4 的 race 已在临时夹具中执行，不能冒充全项目 race，但本次也没有新增 Go 并发行为 |
| 覆盖率 | 不追加；没有新增 Go 分支，本项目 test-coverage.sh 已包含全量 -race；若以后确需该门禁，不应再机械地先后完整运行同一范围的 race 与 race+coverage |

源码核对：scripts/test-coverage.sh 的两条 go test 分支均包含 `-race -count=1 -timeout=5m -covermode=atomic -coverprofile`。既有 ERR-20261023-091、101、115 记录了主工作区 cmd/TestServerUsesDefaultConfigurationLoader 五分钟超时；这些是历史结果，本轮没有重跑或修复，也不把历史失败改写为通过。

实际执行并非每次重复全套：本次拆分的 P4 只执行了一次隔离接线脚本、一次项目初始化定向回归和一次 make lint，三项均 exit 0；详细命令、日志和范围见第 16–17 节。本轮没有再次执行它们，也没有执行上述四项。后续只有出现相关新改动、失败或尚未解决的验证疑点时才重新选择对应检查，受控检查仍须另行明确授权。

当前保留结果：P4 三项通过；本轮未发现需修复的拆分问题，代码不变；独立 vet、modern、项目全量 race、覆盖率标为未执行。此评估只针对本次拆分，不修改项目永久规则或默认门禁。

## 19. 用户确认收尾

2026-10-23，用户明确确认：“本次按已通过的 P4 结果收尾，不再追加 vet、modern、全量 race 和覆盖率检查。”本次 Makefile 分类拆分据此收尾，保留第 16–17 节的真实通过记录；未执行项继续标为未执行，不补写通过结论，也不自动续跑。

本轮只更新本文的收尾状态并回读，没有运行检查、修改代码、处理 cmd 既有超时、归档 OpenSpec 或执行提交/发布操作。无关代码保持不动。
