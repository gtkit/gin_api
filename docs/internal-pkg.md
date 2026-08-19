# internal/pkg 边界

`internal/pkg` 是项目内部共享包，**不等同于对外发布的库**。
对外可复用包（根 `pkg/`）的规范见 [`pkg-design.md`](pkg-design.md)。

两者的区别见 [`architecture.md`](architecture.md#internalpkg-与根-pkg-的区别)——
简单说：`internal/pkg` 可以 import 本仓 `internal/`、可以写日志；根 `pkg/` 两者都禁止。

## 分类

| 类别 | 判据 | 谁可以 import |
|---|---|---|
| 纯基础包 | 无业务、无 HTTP 协议、无 GORM/Redis 依赖 | 任何层 |
| 基础设施型包 | 依赖 GORM/Redis/文件系统/外部 SDK/config | repository、runtime/module、platform |
| 协议适配包 | 依赖 Gin、HTTP 语义、响应信封 | transport/http、router、middleware |

分类不是标签，它决定了这个包能被谁引用。放错类别的后果是依赖方向倒流——
一个协议适配包被 application import，application 就间接依赖了 Gin，
而传感器只检查直接 import，这类间接依赖不会被抓到。

## 反向依赖红线

`internal/pkg` 禁止 import：

- `internal/module/*`
- `internal/module/*/application`
- `internal/module/*/dto`

通用能力不得反向知道业务模块。有可执行判据。

## 新增包准入

按这个顺序判断，命中即停：

1. **只有一个模块使用** → 放模块内部，不进 `internal/pkg`。
2. **跨模块复用但有业务语义** → 放 `internal/shared/<domain>`。
3. **无业务语义且可复用** → 放 `internal/pkg`。
4. **面向仓库外发布** → 按 [`pkg-design.md`](pkg-design.md) 放根 `pkg/`。

新增 `internal/pkg` 包或配置顶层段时，**必须同步登记进 `SCAFFOLD.md` 的预置能力清单**，
由 `scripts/check-capability-inventory.sh` 强制。该判据同样覆盖 `internal/middleware/`
与根 `pkg/`。

它是**存在性**检查：检测不到「提及但未分类」「归错类」「说明与行为不符」——
不要把它当成清单正确性的完整保证。

## 分页包约定

- `internal/pkg/paging`：纯分页类型——查询入参 `Query`（页码、页大小、排序键）与
  结果元信息 `Paging`，不依赖 gorm，**任何层可 import**。
- `github.com/gtkit/ormx/paginator`：GORM 分页查询执行器（外部库），
  **application / dto / models 禁止 import**（传感器禁 `gtkit/ormx` 整个模块）。

分开的理由：分页入参与结果是业务语义（第几页、按什么排、共多少条），
分页执行是数据访问细节。合成一个包会让 application 为了分页类型而 import gorm。
`Query.Sort` 是外部排序键，必须由数据访问层经白名单映射后才进入 SQL。

## 日志

`internal/pkg` 的包可以写日志，但**application 层禁止 import 任何 logger 包**——
需要记录时定义 port，由 runtime 层注入实现。

进程级日志初始化在 `internal/pkg/log`：主实例（不缓冲）+ access 实例（缓冲）。
日志栈统一用 `github.com/gtkit/logger`。
