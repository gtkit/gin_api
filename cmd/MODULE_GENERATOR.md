# Module Generator

模块生成器用于创建一个最小可编译、符合当前分层约束的业务模块骨架。

## 使用方式

生成器由 `scaffold` 构建约束门控（它不属于服务二进制——它会重写
`internal/bootstrap/providers.go`）。`make module` 已把 tag 封装好：

```bash
make module NAME=admin ARGS="--access authenticated"
make module NAME=staff ARGS="--field nickname:string --field status:uint8 --access admin"
make module NAME=article_tag ARGS="--field title:string --field enabled:bool --public"
```

裸命令形式（下面的参数说明按这个形式书写）：

```bash
go run -tags scaffold . module admin --access authenticated
go run -tags scaffold . module staff --field nickname:string --field status:uint8 --access admin
go run -tags scaffold . module article_tag --field title:string --field enabled:bool --public
```

> 在 IDE 里改生成器代码时，需要给 gopls 配 `"buildFlags": ["-tags=scaffold"]`，
> 否则这些文件会被排除在索引之外（表现为满屏 undefined）。

可选参数：

- `--field name:type`：可重复指定字段。
- `--force`：覆盖允许更新的生成文件；model 不会被覆盖。
- `--dry-run`：只输出计划，不写文件。
- `--no-bootstrap`：不修改 `internal/bootstrap/providers.go`（默认会自动接线）。

支持字段类型：

```text
string bool
int int8 int16 int32 int64
uint uint8 uint16 uint32 uint64
float32 float64
time.Time *time.Time
```

模块名和字段名使用小写字母、数字、下划线或中划线，并以字母开头。中划线会规范化为下划线包名。

保留字段：`id`、`created_at`、`updated_at`、`deleted_at`。

## 生成内容

以 `admin` 为例：

```text
internal/module/admin/application/ports.go
internal/module/admin/application/service.go
internal/module/admin/application/service_test.go
internal/module/admin/dto/admin.go
internal/module/admin/transport/http/router.go
internal/module/admin/transport/http/handler.go
internal/module/admin/transport/http/handler_test.go
internal/runtime/module/admin/module.go
internal/repository/admin/repository.go
internal/repository/admin/repository_util.go
internal/models/admin.go
database/migrations/<timestamp>_create_admins_table.up.sql
database/migrations/<timestamp>_create_admins_table.down.sql
```

默认还会通过以下稳定标记更新 `internal/bootstrap/providers.go`（分阶段装配结构，四个注入点）：

```go
// scaffold:module-imports        // import 区：注入 app / http / runtime 三个包导入
// scaffold:module-service-fields // services struct：注入 <name>Service *<name>app.Service 字段
// scaffold:module-services       // buildServices：注入 svc.<name>Service = <name>runtime.NewService(inf.mysqlDB)
// scaffold:modules               // buildEngine 的 v1Modules：注入 <name>http.NewModule(svc.<name>Service)
```

标记缺失时生成命令返回错误，不会部分改写 providers 文件。

## 生成 API

默认生成：

- `GET /api/v1/<module>`：分页列表。
- `GET /api/v1/<module>/:id`：详情。

列表参数：

- `page`：默认 1。
- `page_size`：默认 20，最大 100。

分页归一化位于 application 层；repository 查询始终使用显式 `LIMIT` 和 `OFFSET`。响应包含 `list` 和 `paging`。

## Migration

生成器创建成对的 up/down SQL：

- up 文件包含 `CREATE TABLE IF NOT EXISTS`，CHARSET/COLLATE 取自 `database.charset` / `database.collation` 配置（配置缺席时用同一来源的默认值）。
- down 文件包含 `DROP TABLE IF EXISTS`。

SQL 文件是可执行草稿，必须人工检查索引、唯一约束、默认值、comment、数据规模和回滚影响。生成器只落盘、不执行 SQL；执行走 `migrate` 子命令（最小装配：配置 + 日志 + 数据库，经 gtkit/migratex 按 `database/migrations` 目录迁移）。

**迁移文件不可变**：已存在的 migration 与模板产出不一致时一律跳过并提示，`--force` 也不覆盖——迁移是按序执行过的历史，schema 变更请另建 alter migration。

## 验证

生成完成后运行：

```bash
make check
```

该命令检查架构边界、依赖整洁、包级变量认领、镜像摘要、发布接线、应用身份、lint、vet 与 race test（权威清单见 Makefile 的 check 目标）。
