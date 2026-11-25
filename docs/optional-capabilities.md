# 可选能力的显式装配

默认服务保持不导入 `internal/optional/*`。需要这些能力时，在构建或运行命令增加 `-tags optional`；与开发命令组合时使用 `-tags scaffold,optional`。不再修改四十处注释，不维护源码恢复清单。

配置取自 [完整示例](../config/optional.yml.example)，把需要的根级段合并到实际运行配置。默认构建不会启用这些字段，doctor 会把它们作为未使用键报告；构建标签本身也不会启用业务能力。

| 能力 | 运行开关 | 构造产物 | 业务必须补齐 |
|---|---|---|---|
| 任务队列 | tasks.enabled | 共享 Producer；有 Registrar/PeriodicTask 才创建对应 worker | 处理器、幂等、重试和死信处理、入队调用 |
| 出站 HTTP | outbound.clients 非空 | 按名字隔离的客户端与熔断入口 | 实际下游、调用契约；写调用不擅自重试 |
| 上传 | upload.enabled | 单活存储驱动（local/s3）+ Redis 分片会话的共享 Uploader；file 模块提供上传/下载路由与归属记录 | 云凭据经 env 注入、files 迁移审阅、分片目录容量 |
| 审计 | auditlog.enabled | 有界尽力记录器及 consumer | 表迁移、要审计的写路由、保留策略 |

`bootstrap/optional_enabled.go` 只聚合现有构造函数，资源关闭仍进入 providerCleanup，启动中途失败按取得逆序撤销。tasks 与 upload 启用都让 Redis 成为必需资源（upload.redis_db 必须在 redis.dbs 内）。审计 worker 在 HTTP drain 后停止；上传关闭沿用 SDK Close，级联关闭注入的 Redis 会话存储（不关借用的客户端）；这些所有权不转交给业务服务。代理路径的分片数据仍在 chunk_dir 本地磁盘，多实例部署需共享挂载或粘性路由。

### 文件操作与请求预算

启用新版上传前先应用 `file_operations` 迁移，再排空旧版写实例。新会话绑定其存储位置，旧版未完成会话应先在旧位置完成，否则重新初始化。Redis 保存会话，`chunk_dir` 保存代理分片。需要副本接管时也要共享对应持久卷。local 最终对象目录同样要能被接管副本访问。

删除先隐藏文件。对象回收暂时失败时，DELETE 可返回 503 且 GET 返回 404。原账号可重试，删除完成后保留 7 天成功重放窗口。恢复只在物理位置和内容证据可核验时发布上传，待核对记录保留供排查。完成操作的写入尝试明细在 7 天后分批清理，主记录仍保存来源与合并防复活信息。存在未决操作时保留迁移和新版恢复 worker。

普通上传的请求上限为 `min(application.max_body_bytes, upload.max_file_size + 1 MiB)`，代理分片为 `min(application.max_body_bytes, 21 MiB)`。全局未设置时使用 10 MiB。普通文件还受 `upload.max_file_size` 约束，单个分片还受实际会话和 SDK 20 MiB 限制。multipart 头、字段和边界需要占用请求预算。较大请求在 `chunk_dir` 同级的 `.staging` 路径暂存，按实例的传输并发上限和最大文件预算规划磁盘。

连续翻页可先请求 `GET /api/v1/files?cursor=&page_size=50`，再原样带回 `next_cursor`。空游标响应表示已读完。游标只适用于签发它的账号，历史 NULL 时间按数据库排序继续读取。需要精确总数时使用 `?page=1&page_size=50`。

审计开关启用时，迁移命令的模型列表包含审计表，便于显式生成 DDL；没有自动迁移。默认构建和关闭开关均不登记它。示例业务模块也不因 optional 标签被自动注册。

直接在原项目验证：

```sh
GOWORK=off go run -trimpath scripts/check-optional-wiring.go
go test -vet=off -trimpath -tags optional ./internal/bootstrap ./internal/platform/config
```

第一个命令仅检查默认/optional 的依赖图、主程序和 integration 测试编译，不执行测试二进制、不连接真实依赖，不创建项目副本。可用 `-case default` 或 `-case optional` 限定配置；旧的逐包恢复注释模式已移除。构建使用本机 Go，输出到空设备，超时或失败返回非零。

第二个命令执行包内测试，其中 16 种能力开关组合使用文件夹具、内存 Redis 和数据库替身，验证创建、关闭、借用资源不被误关及中途失败。它不证明真实 Asynq 消费、数据库审计写入、分布式调度或业务文件授权已经可用。

停用单项先处理业务在途请求与存量任务，再关闭配置并发布。物理删除某个包须同时清理 optional 版本的装配、配置、模型登记、测试和文档；默认依赖图隔离不代表删包后 optional 构建仍能通过。
