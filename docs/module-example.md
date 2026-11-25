# 业务模块示例与生成

`internal/module/example` 展示当前项目分层下的记录 CRUD；默认服务不注册这些路由、不创建表。持久化在 `repository/example`，装配在 `runtime/module/example`，DTO 不接收所有者字段，所有者只能由现有鉴权中间件写入请求上下文。

在原项目根目录预览及生成自己的模块：

```sh
GOWORK=off go run -trimpath -tags scaffold . project module --name note --dry-run
GOWORK=off go run -trimpath -tags scaffold . project module --name note
```

名称为最多 30 字符的小写 Go 标识符，不能是关键字。命令使用当前 go.mod 的 module，先检查全部目标，拒绝覆盖文件和通过符号链接父目录写入；写入失败撤销本次已创建的文件，可能保留空目录。不会更改 Git、默认路由、迁移清单或服务配置。

生成的 12 个文件包括持久化模型、具名命令与应用用例、DTO、HTTP handler 与路由、repository、runtime 装配、三层测试和 DDL 草稿。`internal/projectinit/templates/module` 是生成源；示例由同一套模板生成。修改模板后须对照示例同步，生成器不会覆盖人工修改。

接入时由应用已有 bootstrap 创建 `example.New(db)`，将服务、已有 `Auth` 中间件及显式 `Authorizer` 传给 HTTP 层的 `Register`。功能授权使用 `example.access`，缺失依赖拒绝注册；具体权限源由业务接入，不能直接相信 JWT 角色。调用方显式提供版本化路由组。部署方审阅 `database/ddl/create_example_records_table.sql`，将正式受管模型登记到迁移入口，经现有迁移流程建表后再启用路由；不使用启动时 AutoMigrate。完整边界见 [授权约定](authorization.md)。

接口使用 `GET/POST /example`、`GET/PUT/DELETE /example/:id`，以及接收 `ids` 数组的 `DELETE /example/batch`。批量 ID 必须为 1—100 个独立规范 UUID，包含不存在或其他用户的记录时整体回滚并返回 404。标题去首尾空白后为 1—120 个字符，写请求最多 8192 字节；UUID 使用标准库 UUIDv7 并以规范字符串传输。分页默认 page=1、page_size=20，最大分别为 10000、100；按 created_at、id 倒序。大数据业务应按实际查询需求替换为游标分页。

每条 SQL 都限定 `owner_id`，未找到与他人记录统一返回 404。创建和单条软删各执行一条写语句。更新先在短事务中锁定有效的归属行，同值更新直接返回持久化记录，标题变化时使用最后一次成功提交的值。批删在同一事务内检查受影响行数，一条记录缺失或无权操作就整批回滚。派生模块通过现有授权中间件和应用组显式挂载，普通 CRUD 模板只包含记录接口。

## 认证与文件示例

- **auth** 将请求绑定、受信身份提取和对外错误映射放在 transport。application 分别处理注册、登录、刷新、当前会话退出、全部退出和改密。runtime 构造哈希门、凭据仓储、会话校验和 Redis 令牌适配器。启用会话生命周期时，访问和刷新都校验主库账号状态与原会话版本，改密和全部退出递增版本。密码计算受独立并发及等待预算约束，等待容量耗尽返回 503，账号限额返回 429，无效凭据返回既有 401。其他业务通过鉴权中间件取得已验证的 UID。
- **file** 的 HTTP 层绑定显式 DTO，分片与文件路由分别按现有预算读取数据。application 按受信 owner 编排文件元数据、分片会话和文件操作意图。runtime 将 owner 传入上传 SDK 的会话校验并翻译错误。下载流由 HTTP 请求负责关闭，带恢复能力的对象写入、合并与删除由进程级装配维护。账号隔离同时覆盖文件表、分片会话、存储位置和恢复记录。

## 新模块的开发规则

1. 通过 `bootstrap → runtime/module → application` 装配，HTTP 和后台任务只调用应用端口。与其他模块协作时，由调用方定义所需的最小能力，传递 context 与已验证的主体。
2. HTTP DTO 和业务命令分层。owner 来自已鉴权上下文，既用于功能许可，也用于约束每条资源查询、写入和幂等键。
3. 由应用用例持有事务边界。单条写入沿用数据库原子性，多行修改与受影响行数检查同事务完成。外部 I/O 在事务外执行，跨介质写入定义重试和恢复规则。
4. 按路由配置请求体、等待和调用预算，查询限定数量并保持稳定顺序。进程级所有者管理连接和后台任务的生命周期。
5. 改动同时核对模块文档和渲染产物。验收覆盖并发、越权、故障、SQL 条件与对外错误。测试夹具显式注册示例，默认服务保持现有路由集合。
