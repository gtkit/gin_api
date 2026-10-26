# 容量与内存

业务准入按进程统计 `/api/v1` 的实际在途请求。`application.max_inflight_requests` 的 0/省略使用 128，正数显式设置，负数拒绝启动；没有关闭值。满载直接返回统一 503，不排队、不设置固定 Retry-After。`/health`、`/ready` 不占业务名额。名额直到 handler 返回或 panic 展开才释放，取消请求不会提前归还仍在执行的名额。SSE、流式和长轮询应单独分组及确定限额。

密码计算使用 `auth.bcrypt_max_concurrent`（0/省略取有效 CPU 并发数）。`bcrypt_max_waiters: null` 表示最多再等待一批同等人数，0 禁排队，正整数显式指定。`bcrypt_queue_timeout` 的 0/省略为 1s，且不超过父请求预算；只控制等待，不能中断已执行的 bcrypt。等待人数满或排队超时返回 503，调用方取消保持取消语义。四个认证 JSON 路由的正文上限均为 8 KiB。

准入位于本组 Timeout 之前。过载 503 不应被网关无条件重试；只有明确幂等、带抖动及总预算的策略才可重试。

128 是待校准初值。稳定窗口内用 `L ≈ λ × W` 估计平均在途量（吞吐单位为请求/秒，W 为平均端到端秒数，不使用 P99 替代平均值）。DB 并发可初估为吞吐 × 每请求 DB 次数 × 每次占用连接秒数，另计并行调用、事务和其他使用者。DB 默认上限 25 不表示 HTTP 上限也必须为 25；比较池等待、503、P95/P99、CPU 与 RSS 后校准。

## 请求与密码指标

| 名称 | 类型 | 测量点属性 |
|---|---|---|
| http.server.active_requests | UpDownCounter | http.request.method、url.scheme |
| admission.rejections | Counter | http.request.method、url.scheme、http.route、reason=saturated |
| auth.password.active / auth.password.queued | UpDownCounter | 无 |
| auth.password.queue.duration | Histogram，秒 | outcome=acquired/timeout/canceled |
| auth.password.rejections | Counter | reason=queue_full/queue_timeout |

method 为标准方法集合（含 QUERY），其余统一 `_OTHER`；scheme 只按本服务实际 TLS 判断 http/https，不直接信任转发头。在途数没有 outcome 或路由属性；准入拒绝的 route 只取已注册模板。404/405 不进入业务组准入；原全局请求指标仍将未知路径归为 unmatched。只有真正排队的请求记录等待耗时，调用方取消不计为容量拒绝。

## 运行时和连接池指标

| 名称 | 类型/单位 | 属性 |
|---|---|---|
| go.goroutine.count | ObservableUpDownCounter | 无 |
| go.memory.used | ObservableUpDownCounter，By | 无；runtime total 减已释放堆内存，不是 RSS |
| runtime.heap.used | ObservableGauge，By | 无 |
| go.memory.allocated | ObservableCounter，By | 无；用速率观察分配压力 |
| go.cpu.time | ObservableCounter，s | go.cpu.state=gc |
| runtime.gc.pause.count | ObservableCounter | 无；暂停事件累计数，不是 GC 周期数 |
| runtime.gc.pause.quantile | ObservableGauge，s | quantile=0.50/0.95/0.99 |
| db.client.connection.count | ObservableUpDownCounter | Redis 池属性及 db.client.connection.state=used/idle |
| db.client.connection.pending_requests | ObservableUpDownCounter | Redis 池属性 |
| db.client.connection.timeouts / pool.wait.count | ObservableCounter | Redis 池属性 |
| pool.wait.duration | ObservableCounter，s | MySQL 或 Redis 池属性 |

所有池都有 `db.client.connection.pool.name`、`pool.type`、`pool.role`。MySQL 为 `mysql/main`、`mysql`、`main`，不带 Redis 库号。Redis 为 `redis/<db>/<role>`、`redis`、`main/pipeline`，另带整数 `redis.db`。Redis 连接数只发布 used/idle，used 在有符号转换后计算 `max(TotalConns-IdleConns,0)`；字段非原子联合快照，短暂出现 idle>total 时不能据此判定泄漏。旧 MySQL `db.client.connections.*` 名称及 `db.connection.state` 不变。

GC 分位是进程累计分布命中桶的上界估计，无样本或无穷上界时不发布。它仅适合长期基线参考，运行越久越不敏感；不要用来告警近期暂停回归。累计桶无法给出精确总暂停秒数，因此不伪造 duration counter，也不重复把累计值写入直方图。

Redis 计数按捕获的实际客户端身份分代，同代 uint32 使用模差累加，换代加入新实例当前值；独立 pipeline 池独立计数，共享主池时不重复发布。每 DB 只保留当前实例和两个池角色，客户端指针及 generation 不进入标签。两次采集间的增量须小于 2^32，多次整周回绕或替换前尚未采集的末尾增量无法还原。累计纳秒下降时重新建立基线，不产生负值。

等待时长以 float64 秒累加，避免跨代纳秒总和溢出；计数达到 OTel 的 int64 最大值时饱和，不回绕为负值。

采集只读取本机 runtime 和池快照，不发 DB/Redis 网络请求，不新增采样 goroutine。回调通过现有 provider 清理流程注销；关闭遥测导出时沿用 no-op。

## 基线与内存预算

负载工具的命令和边界见 [scripts](../scripts/README.md#load-apigo)。记录真实机器规格、服务与发生器是否同机、代码/Go 版本、数据量、场景比例和 SLO。分别覆盖正常读写、认证、缓存命中/未命中、慢下游、持续运行、滚动退出，保留吞吐、P95/P99、错误率、发生器丢弃率和服务端 CPU/RSS。共享机器的 CPU 争用须标记；目前只有工具功能演练，没有生产容量数字。

`Cache[T]` 的 maxItems 只限制条目数，大对象需要使用方控制单对象大小、总字节或成本预算。T 中的 map/slice/指针共享底层对象，缓存锁不能保护调用方后续修改。写入后及读取期间采用只读约定，需要修改时在边界复制（浅复制仍可能共享嵌套对象）。本次没有反射式大小计算、自动深复制或无调用方的成本框架。

部署时先取得进程/容器的真实内存配额，扣除 Go 外分配、线程栈、共享映射和运行余量，再给 GOMEMLIMIT。它是 Go 运行时软限制，不是整个进程 RSS 硬限制，也不能替代在途数、缓存容量和对象大小限制。对照 RSS 与 go.memory.used，并观察 GC CPU、分配速率和尾延迟；限制过低可能增加 GC 压力。本项目不自动猜测或设置部署内存限额。

启用队列后应由部署方接入积压量、最老任务年龄、重试/失败数，现有每次执行结果和耗时继续复用；本次没有启动可选后台采集器。每个带令牌请求的账号有效性查询、角色查询是否成为瓶颈，应先测量，再按禁用账号/撤权的生效时限设计缓存。
