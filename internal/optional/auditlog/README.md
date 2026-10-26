# 可选写操作审计

接线编译入口：在项目根运行`GOWORK=off go run ./scripts/check-optional-wiring.go -case auditlog`。
工具按manifest中的wiring标记在自有临时副本恢复该能力，主工作区保持注释基线；
只编译主程序和带integration标签的cmd/bootstrap/config测试，不启动业务或执行测试二进制。
完整七例用`make check-optional-wiring`，它是独立目标，不在默认check/check-ci或CI中。
编译通过不等于配置、外部依赖或业务接入通过；已自行修改接线的派生项目须同步维护manifest。

记录器、模型、仓储、中间件、DDL 和迁移都在本目录。
默认没有业务路由、后台任务或审计表迁移接线，主程序不导入本包。

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

```powershell
rg -n AUDITLOG internal/bootstrap cmd/migrate.go
```

1. 去掉 `internal/bootstrap/auditlog.go` 中包围实现的那一对 `/*`、`*/`。
   `buildAuditLog` 使用现有数据库构造仓储和记录器；默认缓冲 1024 条、每批 100 条、
   每 2 秒刷新、关停排空预算 5 秒，可在此按业务调整 Options。
2. 在 `internal/bootstrap/providers.go` 恢复 auditlog import、`infra.auditRecorder` 字段和 `buildAuditLog` 调用。
3. 在 `internal/bootstrap/modules.go` 恢复 auditlog import、`buildWorkers` 中的审计 worker，
   以及 `RegisterModules` 中的 audited 路由组示例。在这个路由组上注册实际业务写路由。
   认证在审计之前执行；需要业务权限时继续挂对应授权中间件。
4. 接入表结构：按下方说明复制 SQL，并在 `cmd/migrate.go` 恢复 auditlog import 和 `&auditlog.Record{}`。
5. 派生项目修改了 module 名时，同步更新注释中的 import。

`auditLogWorker` 是 consumer，HTTP 停止并等待在飞请求结束后才取消记录器，
避免先停记录器、后完成请求导致遗漏。构造记录器只分配队列，不会启动 goroutine；
不要在业务处理器里重复构造或直接另起一个 Run。

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

## 验证与删除

```powershell
gofmt -w internal/bootstrap cmd/migrate.go
go build ./...
go vet ./...
go test -race ./internal/optional/auditlog ./internal/bootstrap
```

默认未接线，可直接删除本目录。
`make build` 构建主程序时不会编译它；`go build ./...` 仍会编译和检查它。
彻底清理时删除 `internal/bootstrap/auditlog.go` 和 providers、modules、migrate 中的 AUDITLOG 注释，
并移除 SCAFFOLD 的文档入口。已经启用后再删除时，业务接线和已经执行的数据库迁移按实际部署处理。
