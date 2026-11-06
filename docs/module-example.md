# 业务模块示例与生成

`internal/module/example` 展示当前项目分层下的记录 CRUD；默认服务不注册这些路由、不创建表。持久化在 `repository/example`，装配在 `runtime/module/example`，DTO 不接收所有者字段，所有者只能由现有鉴权中间件写入请求上下文。

在原项目根目录预览及生成自己的模块：

```sh
go run -trimpath -tags scaffold . project module --name note --dry-run
go run -trimpath -tags scaffold . project module --name note
```

名称为最多 30 字符的小写 Go 标识符，不能是关键字。命令使用当前 go.mod 的 module，先检查全部目标，拒绝覆盖文件和通过符号链接父目录写入；写入失败撤销本次已创建的文件，可能保留空目录。不会更改 Git、默认路由、迁移清单或服务配置。

生成的十个文件包含持久化模型、application、DTO、HTTP、repository、runtime 装配、三层测试和 DDL 草稿。`internal/projectinit/templates/module` 是生成源；示例由同一套模板生成。修改模板后须对照示例同步，生成器不会覆盖人工修改。

接入时由应用已有 bootstrap 创建 `example.New(db)`，将服务、已有 `Auth` 中间件及显式 `Authorizer` 传给 HTTP 层的 `Register`。功能授权使用 `example.access`，缺失依赖拒绝注册；具体权限源由业务接入，不能直接相信 JWT 角色。调用方显式提供版本化路由组。部署方审阅 `database/ddl/create_example_records_table.sql`，将正式受管模型登记到迁移入口，经现有迁移流程建表后再启用路由；不使用启动时 AutoMigrate。完整边界见 [授权约定](authorization.md)。

接口使用 `GET/POST /example`、`GET/PUT/DELETE /example/:id`，以及接收 `ids` 数组的 `DELETE /example/batch`。批量 ID 必须为 1—100 个独立规范 UUID，包含不存在或其他用户的记录时整体回滚并返回 404。标题去首尾空白后为 1—120 个字符，写请求最多 8192 字节；UUID 使用标准库 UUIDv7 并以规范字符串传输。分页默认 page=1、page_size=20，最大分别为 10000、100；按 created_at、id 倒序。大数据业务应按实际查询需求替换为游标分页。

每条 SQL 都限定 owner_id，未找到与他人记录统一返回 404。更新覆盖完整标题，使用最后提交的值；示例不承诺乐观锁或写请求幂等。写入由现有 dbtx 管理事务，删除沿用 GORM 软删除。应用层测试覆盖归属和回滚，HTTP 测试覆盖错误状态，SQL mock 验证生成语句与事务调用；这些证据不替代实际 MySQL 执行计划和并发验收。
