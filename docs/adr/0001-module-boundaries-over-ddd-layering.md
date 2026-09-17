# ADR-0001：按业务模块切分，不改为 DDD 技术分层

- 状态：已采纳
- 日期：2026-08-31
- 影响范围：`internal/` 全部目录结构、`scripts/check-architecture.sh` 判据、模块生成器模板

## 背景

有一类主张认为，Go 服务应当采用领域驱动设计（DDD）的经典四层目录——
`domain/` `application/` `infrastructure/` `interfaces/`，并在此基础上引入
实体、聚合、领域事件等战术模式。同一主张的另一半通常是：
不同的限界上下文应当拆成独立进程，彼此经领域事件通信。

本仓是脚手架母版，结构一旦定型会经 `gonew` 传播到每个派生项目，
因此结构选型需要一次显式决策，而不是在每个派生项目里各自重议。

## 决策

**保持现有的「模块化 + 显式装配」结构：按业务模块（限界上下文）切一级目录，
模块内部再分层。不改为按技术关注点切一级目录的四层结构。**

分层口径的单一来源仍是 [architecture.md](../architecture.md)，本 ADR 只记录选型理由。

## 论证

### 一、限界上下文的两条判据，当前结构已经满足

限界上下文的实质是两条：边界内有自己的模型和语言；边界外只能经显式契约交互。
两条在当前结构里都有代码证据：

| 判据 | 当前状态 | 证据 |
|---|---|---|
| 模块之间无直接引用 | `auth` 与 `ai` 双向零 import | `grep -rn "internal/module/ai" internal/module/auth/` 与其反向皆无匹配 |
| 契约显式且属于消费方 | port 接口定义在 application 侧，实现由装配层注入 | `internal/module/auth/application/ports.go` |
| 模型在边界处翻译 | `Credential` 在 application 层重新声明，不引用仓储包同名类型 | 同上文件内的类型注释 |
| 数据不共享 | 仓储按模块独占：`internal/repository/authcredential/` 归 auth，`internal/repository/aiconversation/` 归 ai。`internal/repository/useraccount/` 不归任何模块，由装配层直接持有——它是鉴权链路的账号状态源，挂到业务模块上会让删那个模块连带打断鉴权 | `internal/runtime/module/` 各模块装配入口 |
| 汇合点唯一 | 模块只被装配层引用，彼此不感知 | `internal/bootstrap/providers.go` |

模块内部的分层方向由传感器强制，不靠自觉：`application imports gin / gorm / repository`
与 `worker imports gorm / repository` 挡住上层下钻，`repository imports gin / module`
与 `internal/pkg imports module` 挡住下层反向引用模块。判据在
`scripts/check-architecture.sh`，`make check` 第一步执行。

这套切法通常叫 package-by-feature。它与四层结构的区别不是「做没做 DDD」，
而是**一级目录切的是业务边界还是技术关注点**——限界上下文属于前者。

### 二、四层结构切分的是技术关注点，边界会随上下文增多而消失

四层结构把所有上下文的实体放进 domain 层的同一个 model 包。单上下文时两者
差别不大；到第二个上下文出现时性质就变了：

- 当前结构下，`auth` 想引用 `ai` 的类型必须写一行 import，评审时看得见，
  且可以加一条传感器判据把它变成硬失败。
- 四层结构下，两个上下文的实体在同一个包内，跨上下文引用连 import 都不需要，
  编译器与静态判据都没有着力点。

也就是说，四层结构提供的是战术模式的**形状**（实体、仓储接口、领域事件三件套），
而上下文边界退回成命名约定。若选型动机是「限界上下文比战术模式重要」，
四层结构恰好在这一点上比当前结构弱。

### 三、迁移成本与收益不成比例

按实测行数（`find . -name '*.go' -not -name '*_test.go'`，2026-09-17 重测）：

| 范围 | 非测试行数 | 占比 |
|---|---|---|
| 业务模块 `internal/module/` | 2396 | 7% |
| 数据访问 `internal/repository/` | 797 | 2% |
| 全仓 | 32178 | 100% |

母版的绝大部分代码是平台能力（配置、可观测性、中间件、门禁、脚手架生成器），
真正会被重排进 `domain/` 的业务逻辑只有 7%。而重排要付的代价是确定的：

- 传感器的每条判据按路径匹配目录（`internal/module/*/application` 这类），
  一级目录改名后全部失效，需重写并重新验证——门禁失效期间是无保护的。
- 模块生成器模板 `internal/scaffold/module_templates.go` 与其防腐测试全部作废。
- 派生项目经 `gonew` 继承的结构与既有派生项目分叉。

至于战术模式中的充血实体：`internal/models/` 当前是 GORM 持久化模型。
改为充血领域模型需要拆成领域实体与持久化模型两套并增加映射层，
按现有实体数量估算，映射层代码量与 1856 行的业务逻辑本身同量级。
对以资料读写为主的模块，这部分代码不承载业务规则。

### 四、进程边界与上下文边界是两件事

限界上下文是模型的边界，不是部署单元的边界——一个进程内可以有多个上下文，
当前仓库就是这种形态。把上下文拆成独立进程解决的是团队独立发布的问题
（组织约束），代价是分布式事务、跨进程调试与运维复杂度。
母版的定位是单进程 Web 服务，派生项目在需要独立发布节奏时，
当前的模块边界正是拆分时的切割线——按模块目录整体搬走即可，
这也是保持模块边界清晰的收益之一。

### 五、领域事件的引入前提

领域事件用于解耦跨上下文通信。当前 `auth` 与 `ai` 双向零引用，
模块间通信不经任何代码路径发生，事件机制没有消费者。

真正出现跨模块通知需求时，异步承载已经就位：`internal/pkg/tasks/` 的
asynq 队列，其 worker 契约与关停顺序见
[workers-and-scheduling.md](../workers-and-scheduling.md)。
届时的决策点是「同步端口注入还是异步事件」，属于另一次决策。

## 后果

**得到的：** 一级目录直接反映业务边界；模块可整体删除或搬迁；
新增模块经 `make module` 生成即符合边界；传感器判据与目录结构同构。

**付出的：** 与四层 DDD 结构的通用词汇不对齐，熟悉那套目录的人需要一次对照说明——
本 ADR 与 `internal/module/ai/doc.go` 承担这个说明。

**当前由约定而非判据维持的部分：** 传感器的横向规则挡的是下层引用模块
（`repository` / `internal/pkg` 不得 import `internal/module/`），
模块之间的横向引用由组合根收口于 `internal/bootstrap/providers.go`。
第三个模块出现且首次需要跨模块调用时，是决定「经 bootstrap 注入端口」
还是「允许直接 import」的时刻，该决策应产生新的 ADR 与配套判据。

## 参考

- [architecture.md](../architecture.md)：分层、依赖方向、装配边界的单一来源
- [quality-gates.md](../quality-gates.md)：判据入口与新规则准入
- `internal/module/ai/doc.go`：模块内分层的具体落地样例
