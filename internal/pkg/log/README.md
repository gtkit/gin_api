# 日志系统

项目使用双 Logger 架构，将 HTTP 访问日志与业务日志分离，各自使用不同的写入策略。

## 架构总览

```
┌────────────────────────────────────────────────────────────────┐
│                        日志系统                                 │
├───────────────────────────┬────────────────────────────────────┤
│       globalLog (v1)      │         accessLog (v2)             │
│  github.com/gtkit/logger     │  github.com/gtkit/logger/v2            │
├───────────────────────────┼────────────────────────────────────┤
│  WriteSyncer（同步写入）    │  BufferedWriteSyncer（缓冲写入）    │
│  每条日志立即落盘           │  缓冲区 256KB / 30s 定时刷盘        │
├───────────────────────────┼────────────────────────────────────┤
│  业务逻辑、数据层、Worker   │  仅 HTTP 中间件访问日志              │
│  错误处理、通知推送         │  ZapLogger() / GinLogger()         │
│  Channel 日志              │                                    │
├───────────────────────────┼────────────────────────────────────┤
│  ./logs/app_client-*.log  │  ./logs/app_client_access-*.log    │
└───────────────────────────┴────────────────────────────────────┘
```

## 设计边界

### globalLog — 业务日志（默认 WriteSyncer）

- **写入方式**: 同步写入，每条日志立即 syscall 落盘
- **适用场景**: 业务逻辑、错误处理、数据层、Worker、通知推送、Channel 日志
- **可靠性**: 进程崩溃不丢日志
- **使用方式**: 通过 `github.com/gtkit/logger` v1 的包级函数直接调用

```go
import "github.com/gtkit/logger"

logger.Info("user created", zap.Int("uid", uid))
logger.Error("db query failed", zap.Error(err))
logger.Warn("rate limit hit", zap.String("ip", ip))
```

### accessLog — HTTP 访问日志（BufferedWriteSyncer）

- **写入方式**: 缓冲写入，日志先写入 256KB 内存缓冲区，满或每 30s 批量刷盘
- **适用场景**: 仅用于 `internal/middleware/logger.go` 中的 `ZapLogger()` 和 `GinLogger()`
- **性能收益**: 高 QPS 下显著减少 syscall 次数，降低请求延迟
- **代价**: 进程被 `kill -9` 时可能丢失缓冲区中未刷写的访问日志（最多 256KB）
- **使用方式**: 通过 `internal/pkg/log.AccessLog()` 获取 `*loggerv2.Logger` 实例

```go
import applog "gin_api/internal/pkg/log"

accessLogger := applog.AccessLog()
accessLogger.Info("/api/v1/users", zap.Int("status", 200))
accessLogger.Warn("/api/v1/orders 404", zap.Duration("latency", d))
```

## 初始化与生命周期

初始化顺序在 `internal/bootstrap/providers.go` 的 `initLegacyRuntime` 中：

```
1. initLogWithConfig(cfg)      → 初始化 globalLog (v1)
2. legacylog.InitAccessLog(cfg) → 初始化 accessLog (v2, BufferedWriteSyncer)
```

关闭顺序在 `closeLegacyRuntime` 中：

```
1. legacylog.SyncAccessLog()   → 先 flush accessLog 缓冲区并关闭文件
2. legacydao.DBClose()         → 关闭数据库连接
3. logger.Sync()               → flush globalLog
```

> accessLog 必须在 globalLog 之前 Sync，确保缓冲区数据完整落盘。

## 日志文件

| Logger    | 文件路径示例                          | 切割方式 |
|-----------|--------------------------------------|---------|
| globalLog | `./logs/app_client-info.log`         | daily   |
| accessLog | `./logs/app_client_access-info.log`  | daily   |

路径前缀由配置 `log.path` 决定，accessLog 自动追加 `_access` 后缀。

## 注意事项

- 除 middleware 访问日志外，**所有其他地方**（包括 Channel）都使用 globalLog（同步写入）
- accessLog 创建后会调用 `Undo()` 撤销 `zap.ReplaceGlobals`，确保全局 logger 始终由 v1 管理
- 不要在 middleware 以外的地方使用 `applog.AccessLog()`
