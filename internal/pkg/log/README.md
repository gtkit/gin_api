# 日志事件门面

本包定义项目日志事件并转发给已安装的 Logger，不创建或关闭 SDK 实例。业务、数据层和
HTTP 中间件沿用 Info/Warn/Error、结构化字段、request_id/trace_id 与告警调用；固定 gtkit/logger 的构造和关闭由 bootstrap 管理。JSON 实现和版本不受此边界影响。

```go
import applog "github.com/gtkit/gin-api/internal/pkg/log"

applog.Info("user created", applog.Int("uid", uid))
applog.Error("db query failed", applog.Err(err))
```

`internal/bootstrap/resources.go` 先加载配置和通知渠道，再调用 `platform/logging.Open`
构造 gtkit/logger/v2 的 main/access 两个实例，通过 Install 绑定事件门面。
main 使用同步文件写入，access 使用 SDK 缓冲写入，路径和保留策略沿用配置及 SDK 原默认值。
access 的路径在 log.path 非空时追加 `_access`，未配置时使用原默认 `./logs/access`。

普通调用方只拿 Log 能力，不取得 SDK 或关闭权。Install 的释放函数校验绑定归属；旧 owner
的清理不会移除后续绑定。正常停止归 Runtime：先关缓存及数据库/Redis，再关闭日志 owner。
同一 owner 的 Close/Drain 共用一次关闭，先拒绝新写入、等待已接收写入，再 Sync access/main。

Runtime.DrainLogs 只用于 worker 存活、共享资源不能关闭的异常路径。并发调用共享结果，
等待者取消只结束自己的等待。默认 SDK 的 Sync 无 context/error 返回，真实刷盘失败由 SDK
输出，无法据 void 返回证明送达，也无法硬中断阻塞的 SDK I/O。回滚和资源停止预算各 30 秒，
取消需要实现合作；不创建超时后遗弃的关闭 goroutine。
