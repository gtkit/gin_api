# 可选写操作审计

统一通过 `-tags optional` 编译接线，按各能力配置启用；默认构建不导入可选包。
验证入口为 `GOWORK=off go run -trimpath scripts/check-optional-wiring.go`，直接在原项目检查
默认/optional 的依赖图和构建，不复制源码、不启动服务。完整说明见 [可选能力接入](../../../docs/optional-capabilities.md)。

记录器、模型、仓储、中间件、DDL 和迁移都在本目录。
默认没有业务路由、后台任务或审计表迁移接线；optional 构建按 auditlog.enabled 创建记录器和 consumer。

| 文件 | 职责 |
|---|---|
| `recorder.go` | 有界队列、批量落库、取消后的限时排空 |
| `repository.go` | NewRepository、WriteBatch、DeleteOlderThan |
| `model.go` | Record 和 operation_audit_logs 表结构 |
| `middleware.go` | Middleware，记录身份、路由模板、状态和时延 |
| `migrations/` | goose 格式的审计表迁移草稿 |
| `ddl/` | 与模型对应的 CREATE TABLE |
| `schema/` | 审计表的预期 schema 片段 |

## 启用接线

使用 `-tags optional` 构建，并配置 auditlog.enabled=true。构造函数创建有界队列，consumer 在 HTTP drain 后排空，默认容量/批量/时间预算沿用 Options。
业务写路由显式挂载 `auditlog.Middleware(inf.auditRecorder.Enqueue)`，先鉴权再审计；未注册的路由不自动审计。
启用后迁移命令的模型登记包含 auditlog.Record，但不会自动生成或执行迁移。必须先审阅并部署下方表结构。

## 表结构与保留期

默认迁移入口只扫描 `database/migrations/`，不会扫描本目录。

启用时将 `migrations/` 的 SQL 草稿复制到 `database/migrations/`，
将 `ddl/` 的对应文件复制到 `database/ddl/`，按项目的迁移流程审阅与执行。
新项目可以保留草稿版本号；已有迁移记录的项目应使用新的未占用版本号。
本目录的 `schema/operation_audit_logs.snapshot` 只包含审计表片段；启用后按项目流程更新
`database/schema/baseline.snapshot`，不要用该片段直接覆盖全库快照。
这里提供文件与接线说明，不会自动执行数据库操作。

`repo.DeleteOlderThan(ctx, cutoff, batchLimit)` 可接普通定时任务。
保留期由业务决定；每批删除量有上限，批间检查 context。

## 记录语义

审计是尽力而为：队列满时拒绝新记录，写入失败时整批丢弃并记录告警，不阻塞请求。
不记录请求体、响应体或 query string；字符串按表列宽截断。
请求 panic 时由外层 recovery 处理，当前中间件不为该请求生成审计记录。
有零丢失要求时需要另行设计同步持久化或可靠日志，不能把这个内存队列当成持久化队列。

## 停用与删除

关闭 auditlog.enabled 后重启可停用记录器，不删除历史记录或表。
物理删除须同步清理 optional 装配、配置、模型登记与业务中间件；迁移和历史审计数据按部署策略处理。
