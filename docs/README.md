# 工程规范

本目录是本项目工程约定的**单一来源**。派生项目经 `gonew` 复制后会一并继承。

每条硬约束尽量对应一条可执行判据（`make check`）。只写在文档里的约束会被无声违反，
因此本目录的定位是解释**为什么**——判据回答「违反了没有」，文档回答「为什么这么定」。

## 目录

| 文档 | 覆盖范围 |
|---|---|
| [architecture.md](architecture.md) | 分层、依赖方向、装配边界、禁止规则。**所有代码任务都适用** |
| [api-conventions.md](api-conventions.md) | 响应信封、handler 流程、错误映射、路由、中间件、流式响应 |
| [db-patterns.md](db-patterns.md) | Repository 模式、GORM 规则、事务、错误跨层 |
| [migration.md](migration.md) | 迁移变更流程与脚本要求 |
| [workers-and-scheduling.md](workers-and-scheduling.md) | Worker 契约、关停顺序、单实例执行、任务队列 |
| [cache.md](cache.md) | 响应缓存与进程内缓存的边界 |
| [outbound.md](outbound.md) | 出站 HTTP 客户端、熔断、重试 |
| [observability.md](observability.md) | 日志、指标、追踪、告警 |
| [internal-pkg.md](internal-pkg.md) | `internal/pkg` 的分类与准入 |
| [pkg-design.md](pkg-design.md) | 根 `pkg/` 可复用包的设计规范 |
| [testing.md](testing.md) | 测试分层、table-driven 边界、反证测试 |
| [quality-gates.md](quality-gates.md) | 门禁入口、传感器覆盖、新规则准入 |
| [review-checklist.md](review-checklist.md) | 提交前逐项自审 |

其他入库文档：

- [`../README.md`](../README.md)：项目概览、快速开始、部署
- [`../SCAFFOLD.md`](../SCAFFOLD.md)：作为脚手架母版孵化新项目、**预置能力清单**
- [`../database/README.md`](../database/README.md)：迁移目录布局、运行环境要求、schema 快照
- [`../config/README.md`](../config/README.md)：配置字段说明
- [`../scripts/README.md`](../scripts/README.md)：门禁脚本用法

## 按任务查

| 任务 | 先读 |
|---|---|
| 新增 API | architecture + api-conventions |
| 数据库操作 | db-patterns |
| 数据库迁移 | migration + `../database/README.md` |
| 后台任务 / 定时任务 / 队列 | workers-and-scheduling |
| 加缓存 | cache |
| 调用外部服务 | outbound |
| 加日志 / 指标 / 告警 | observability |
| 新增 `internal/pkg` 包 | internal-pkg |
| 新增对外可复用包 | pkg-design |
| 写测试 | testing |
| 提交前 | review-checklist + quality-gates |

## 文档约定

本目录只描述**已实现**的能力、用法与契约。不写「不支持的特性」「计划中的功能」
「本项目不内置 X」这类内容——不做的事不写进文档。

可以写的是已实现能力的**使用约束**（如「某回调必须监听 ctx」「某字段构造后不得修改」）：
那是在说明如何正确使用已有的东西。

需要说明某项职责归属别处时用正向表述（「鉴权由业务注入，方式如下」），
不写否定式（「本包不内置鉴权」）。
