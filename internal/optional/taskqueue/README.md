# 可选任务队列

统一通过 `-tags optional` 编译接线，按各能力配置启用；默认构建不导入可选包。
验证入口为 `GOWORK=off go run -trimpath scripts/check-optional-wiring.go`，直接在原项目检查
默认/optional 的依赖图和构建，不复制源码、不启动服务。完整说明见 [可选能力接入](../../../docs/optional-capabilities.md)。

这里集中保存 asynqx 的生产者、消费者、周期调度、配置校验、执行指标和包内测试。
当前没有业务任务；需 optional 构建、tasks.enabled 与实际任务注册共同启用。

| 文件 | 职责 |
|---|---|
| `producer.go` | 普通业务入队：`NewProducer`、`Producer.Enqueue`、`Shutdown` |
| `consumer.go` | 注册处理器并消费任务 |
| `scheduler.go` | 按 cron 定时投递任务 |
| `config.go` | 配置、默认值与校验 |

## 普通业务入队

完成下面的启用步骤后，将装配好的 `inf.tasksProducer` 注入业务服务并复用，
在 HTTP 请求或业务方法中调用 `Enqueue`：

```go
if err := producer.Enqueue(ctx, "email:send", map[string]any{
    "user_id": 123,
}); err != nil {
    return err
}
```

这会把任务立即放入队列，随后由注册了 `email:send` 的消费者处理。
`Enqueue` 返回 nil 表示入队成功，任务的业务处理结果由消费者负责。
普通入队不需要启用 scheduler，也不需要声明 `PeriodicTask`。
不要在每次请求中重新构造 Producer；队列关闭时，`Enqueue` 返回 `ErrDisabled`。

默认进入 `default` 队列；需要指定队列时，传入投递选项，并让消费者配置包含该队列：

```go
err := producer.Enqueue(ctx, "email:send", payload,
    asynqx.WithTaskQueue("emails"),
)
```

## 默认构建范围

- 主程序没有导入本包。构建主程序时，本包及 asynqx/asynq 不在依赖图中。
- `make build` 在 `make/build.mk` 中构建的是主程序 `.`，因此默认不会编译或打包本包。
- `go build ./...`、`go test ./...` 显式包含仓库的所有 Go 包，所以仍会编译本包。
- `go mod tidy` 也会扫描本包，所以目录还在时会保留 SDK 依赖。
- optional 构建标签仅控制装配；显式全仓测试仍会包含本包。
- 默认构建忽略 tasks 配置并通过 doctor 报告键名；optional 构建会校验启用配置。

可在项目根目录查看默认依赖图：

```powershell
go list -deps .
```

输出不应包含 `internal/optional/taskqueue`、`github.com/gtkit/asynqx` 或 `github.com/hibiken/asynq`。

## 启用步骤

1. 使用 `-tags optional` 构建，将 `config/optional.yml.example` 的 tasks 段合并到运行配置。
2. 配置 Redis 地址，确认 tasks.redis_db 在 redis.dbs 内，再启用 tasks.enabled。
3. 在 `buildTaskQueueWorkers` 的 NewObservedConsumer 中传入 Registrar，NewScheduler 中传入 PeriodicTask；没有注册时不创建相应 worker。
4. 在模块装配中把 inf.tasksProducer 注入 application service。先注册处理器再开始投递；调度器还需 tasks.scheduler.enabled。

生产者的关闭已登记；调度器先停，consumer 在 HTTP drain 后停止。无需恢复注释或维护接线清单。

## 停用与删除

停止投递后关闭 tasks.enabled，并按业务规则处理未消费任务；关闭 scheduler 只停止本进程调度。
不使用可选能力时使用默认构建。若要物理删除包，还必须同步删除 optional 构建的引用、配置字段和测试；不能只删目录后声称所有构建仍可用。
