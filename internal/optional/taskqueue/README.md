# 可选任务队列

接线编译入口：在项目根运行`GOWORK=off go run ./scripts/check-optional-wiring.go -case taskqueue`。
工具按manifest中的wiring标记在自有临时副本恢复该能力，主工作区保持注释基线；
只编译主程序和带integration标签的cmd/bootstrap/config测试，不启动业务或执行测试二进制。
完整七例用`make check-optional-wiring`，它是独立目标，不在默认check/check-ci或CI中。
编译通过不等于配置、外部依赖或业务接入通过；已自行修改接线的派生项目须同步维护manifest。

这里集中保存 asynqx 的生产者、消费者、周期调度、配置校验、执行指标和包内测试。
当前没有业务任务，目录外的接线默认全部注释；仅设置环境变量不能启用。

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
- 没有增加构建标签。若需要连全仓扫描也跳过本包，需要另行使用构建标签或独立 Go module。
- 未恢复配置字段时，`tasks:` 和 `APP_TASKS__...` 会被严格配置校验拒绝。

可在项目根目录查看默认依赖图：

```powershell
go list -deps .
```

输出不应包含 `internal/optional/taskqueue`、`github.com/gtkit/asynqx` 或 `github.com/hibiken/asynq`。

## 启用步骤

所有需要恢复的位置均标有 `TASKQUEUE`。从项目根目录定位：

```powershell
rg -n TASKQUEUE internal/bootstrap internal/platform/config config/env.yml.example
```

派生项目若已修改 `go.mod` 的 module 名，恢复注释时需把示例 import 中的
`github.com/gtkit/gin-api` 换成当前 module 名（可执行 `go list -m` 查看）。
注释中的 import 不参与 Go 语法解析，不能依赖导入重写工具自动更新它。

1. **恢复配置类型和校验。**
   - `internal/platform/config/types.go`：取消 taskqueue import 和 `Tasks taskqueue.Config` 字段的注释。
   - `internal/platform/config/validate.go`：取消 `c.Tasks.Validate(c.Redis.Addr, c.Redis.DBs)` 的错误处理块，以及 `validateRedisCapabilities` 中的 tasks 判断块。
2. **恢复装配。**
   - `internal/bootstrap/taskqueue.go`：删除包围 import 和实现的那一对 `/*`、`*/`；顶部说明注释保留。
   - `internal/bootstrap/providers.go`：取消 taskqueue import、`infra.tasksProducer` 字段、`buildTaskQueueProducer` 调用及其错误处理的注释。生产者在 service 之前构造，关闭登记已经包含在 helper 中。
   - `internal/bootstrap/modules.go`：取消 `buildTaskQueueWorkers` 调用、错误处理和追加 workers 的注释。
   - `internal/bootstrap/requirements.go`：取消 `cfg.Tasks.Enabled` 判断块，让启用的队列把 Redis 声明为必需资源。
3. **注册实际任务并注入入队端。**
   - 在 `buildTaskQueueWorkers` 的 `NewObservedConsumer` 调用末尾追加 `Registrar`。
   - 在 `buildServices` 中将 `inf.tasksProducer` 注入需要发任务的业务服务。
   - 需要 cron 时，在 `NewScheduler` 调用末尾追加 `PeriodicTask`；没有条目时不会创建调度器。
   - 没有处理器时不会创建消费者。先完成处理器注册，再向队列发送业务任务。
4. **恢复配置示例。**
   - 在 `config/env.yml.example` 的 TASKQUEUE-BEGIN/END 之间，移除每行最前面的 `# `，保留原本的 YAML 注释。
   - 同步到实际使用的配置文件，配置 `redis.addr`，并将 `tasks.redis_db` 加入 `redis.dbs`。
   - 需要运行队列时设置 `tasks.enabled: true`；需要周期调度时另设 `tasks.scheduler.enabled: true`。
5. **恢复接线回归并验证。**
   - `internal/bootstrap/taskqueue_test.go`：删除包围 import 和测试的那一对 `/*`、`*/`。
   - 根配置和资源清理测试会根据 `Tasks` 字段是否存在验证对应行为，无须修改。
   - 若运行 Linux integration 用例，恢复 `cmd/plugins_integration_test.go` 中标注 TASKQUEUE 的 import 和配置清空语句，避免测试读取的业务配置意外启用队列。

```powershell
gofmt -w internal/bootstrap internal/platform/config internal/optional/taskqueue
go build ./...
go vet ./...
go test -race ./internal/optional/taskqueue ./internal/bootstrap
go test -race ./internal/platform/config -run 'Test(TaskQueue|ShippedExample|ExampleCoversConfigStruct|OptionalRedisConfiguration)'
```

业务处理器通过 `asynqx.Handle` 注册，处理失败返回 error，由队列重试。
处理器仍需保证业务幂等；队列持久化、重试和去重不等于业务恰好执行一次。
周期任务只在当前进程内调度，不包含跨副本选主。

## 停用与删除

仅临时停用：已接线的项目可以设置 `tasks.enabled: false`；这会停止队列装配，但包仍在编译依赖中。

需要恢复成当前默认状态时，重新注释上述接线，并移除实际配置中的 `tasks` 段和 `APP_TASKS__...` 环境变量。

当前默认状态下，可以直接删除本目录，再执行 `go mod tidy`。
目录外保留的 TASKQUEUE 注释不影响编译；需要彻底清理时，一并删除这些注释、
`internal/bootstrap/taskqueue.go` 和 `internal/bootstrap/taskqueue_test.go`。
若还要清理文档引用，移除 `SCAFFOLD.md`、`docs/workers-and-scheduling.md`、
`docs/adr/0001-module-boundaries-over-ddd-layering.md` 中指向本目录的说明和链接。

已经接入业务的项目，删除前还需处理服务里的入队调用及遗留待处理任务。
通用 WorkerManager、认证、缓存和 Redis 资源管理不属于本包，继续保留。
