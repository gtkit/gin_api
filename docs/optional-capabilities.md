# 可选能力的显式装配

默认服务保持不导入 `internal/optional/*`。需要这些能力时，在构建或运行命令增加 `-tags optional`；与开发命令组合时使用 `-tags scaffold,optional`。不再修改四十处注释，不维护源码恢复清单。

配置取自 [完整示例](../config/optional.yml.example)，把需要的根级段合并到实际运行配置。默认构建不会启用这些字段，doctor 会把它们作为未使用键报告；构建标签本身也不会启用业务能力。

| 能力 | 运行开关 | 构造产物 | 业务必须补齐 |
|---|---|---|---|
| 任务队列 | tasks.enabled | 共享 Producer；有 Registrar/PeriodicTask 才创建对应 worker | 处理器、幂等、重试和死信处理、入队调用 |
| 出站 HTTP | outbound.clients 非空 | 按名字隔离的客户端与熔断入口 | 实际下游、调用契约；写调用不擅自重试 |
| 上传 | upload.enabled | 有明确目录、大小、类型约束的共享 Uploader | 归属授权、元数据、路由和安全回源 |
| 审计 | auditlog.enabled | 有界尽力记录器及 consumer | 表迁移、要审计的写路由、保留策略 |

`bootstrap/optional_enabled.go` 只聚合现有构造函数，资源关闭仍进入 providerCleanup，启动中途失败按取得逆序撤销。tasks 启用让 Redis 成为必需资源。审计 worker 在 HTTP drain 后停止；上传关闭沿用 SDK Close；这些所有权不转交给业务服务。

审计开关启用时，迁移命令的模型列表包含审计表，便于显式生成 DDL；没有自动迁移。默认构建和关闭开关均不登记它。示例业务模块也不因 optional 标签被自动注册。

直接在原项目验证：

```sh
GOWORK=off go run -trimpath scripts/check-optional-wiring.go
go test -vet=off -trimpath -tags optional ./internal/bootstrap ./internal/platform/config
```

第一个命令仅检查默认/optional 的依赖图、主程序和 integration 测试编译，不执行测试二进制、不连接真实依赖，不创建项目副本。可用 `-case default` 或 `-case optional` 限定配置；旧的逐包恢复注释模式已移除。构建使用本机 Go，输出到空设备，超时或失败返回非零。

第二个命令执行包内测试，其中 16 种能力开关组合使用文件夹具、内存 Redis 和数据库替身，验证创建、关闭、借用资源不被误关及中途失败。它不证明真实 Asynq 消费、数据库审计写入、分布式调度或业务文件授权已经可用。

停用单项先处理业务在途请求与存量任务，再关闭配置并发布。物理删除某个包须同时清理 optional 版本的装配、配置、模型登记、测试和文档；默认依赖图隔离不代表删包后 optional 构建仍能通过。
