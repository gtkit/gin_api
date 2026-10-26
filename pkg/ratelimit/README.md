# ratelimit

分布式与进程内限流，Redis + Lua 定点整数令牌桶。

```go
import "github.com/gtkit/gin-api/pkg/ratelimit"
```

## 为什么是令牌桶而不是固定窗口

固定窗口（`INCR` + `EXPIRE`）在两个窗口交界处会放过接近两倍的流量：前一窗口末尾打满配额，后一窗口开头再打满一次，两次相隔可能只有一秒。

常见的补救是退回 ZSet 滑动窗口，但那让**每个键的内存随请求数增长**——而限流器本身正是攻击者最容易撑爆的地方。令牌桶每个键只有两个字段，且天然表达"平时限速、允许突发"。

## 用法

```go
backend, err := ratelimit.NewRedisBackend(ctx, redisClient,
    ratelimit.WithKeyPrefix("myservice"),
    ratelimit.WithObserver(myObserver{}),
)
if err != nil {
    return err // 构造期就会验证脚本能加载、ACL 允许 EVALSHA、返回值契约相符
}

policy := ratelimit.Policy{
    ID: "login.attempt", Rate: 1, Burst: 5,
    Scope: ratelimit.ScopeIP,
    Store: ratelimit.StoreRedis,
    FailureMode: ratelimit.FailureClosed,
    MaxKeys: 100_000,
}
if err := policy.Validate(); err != nil {
    return err
}

limiter, err := ratelimit.NewRedisLimiter(backend, policy)
decision, err := limiter.Check(ctx, key)
if !decision.Allowed() {
    // decision.RetryAfter / decision.Limit / decision.Remaining 可直接写响应头
}
```

## 策略字段没有默认值

`Scope`、`Store`、`FailureMode` 的零值都是非法的，`Validate` 会拒绝。

这是刻意的。以 `FailureMode` 为例：后端不可用时该放行还是拒绝，是一个**业务决策**——登录接口放行等于限流形同虚设，而普通查询拒绝会把一次 Redis 抖动放大成一次业务不可用。没有哪个默认值对两者都对。

`MaxKeys` 同理必须显式给出：没有上限时，一个轮换主体标识的客户端可以让限流器的状态无限增长。

Redis 策略还必须满足 `ceil(FixedPointBurst / FixedPointRate) + 60 <= 86400`，单位为秒。
TTL 使用同一份定点补满计算，最大 24h；超出范围会在注册时失败，不会截短一个尚未补满的桶。
例如 `Rate=0.000001, Burst=1` 需要 1,000,000 秒补满，因此不被 Redis 策略接受。本地策略不受此驻留限制。

## 请求取消与扣减重试

已经取消或超时的请求不进入 Redis。执行中的纯 `context.Canceled` 不增加或清零健康计数；
实际依赖超时及混合了网络故障的错误仍计失败。探测凭证绑定健康代次，迟到结果不释放下一轮探测。

EVALSHA 经 `Process` 发送带 `NoRetry` 的命令，响应丢失时不会自动重放可能已经执行的扣减。
仅明确 `NOSCRIPT` 时加载脚本后再执行一次。自定义 runner 需要实现 `ScriptLoad`、`Process`、`Del`；
使用 go-redis 客户端的调用方式不变。失败不代表一定未扣减，调用方不能据此自动补回额度。

## 后端不可用时的三种处置

| FailureMode | 行为 |
|---|---|
| `FailureOpen` | 放行 |
| `FailureClosed` | 拒绝 |
| `FailureLocalFallback` | 切到本地影子桶 |

`FailureLocalFallback` 只能称"算法模型一致"，**不能称"状态连续"**：切换时后端里的计数不会迁移，本地桶从满桶起算，因此瞬时会多放行至多一个 `Burst`。`Observer.FellBackToLocal` 会在首次切换时通知一次。

## 键基数达上限时拒绝新建，不淘汰活跃桶

`MaxKeys` 到顶时新主体直接被拒，而不是 LRU 淘汰旧桶。

LRU 在这里恰好帮了攻击者：轮换超过 `MaxKeys` 个主体就能把旧桶挤掉，旧主体回来时拿到一个满的 `Burst`——限流被完全绕过。

已有键仍使用原桶；IP变化大的流量会先影响新访客。本地桶按进程保存，多副本上不能据单实例计数推断全局配额。
项目的HTTP适配层逐请求记录`ratelimit.decisions`，属性为`ratelimit.policy`和`ratelimit.outcome`；
键预算拒绝的outcome为`key_limit`。建议连续两个1分钟采集窗口均有新增时告警，5分钟无新增后恢复。
导出器实际series名称、属性映射、采集周期、接收人及路由仍须部署核对；该建议不表示告警已经上线。

处置先核对policy、真实主体数、异常来源及副本规模，再限制攻击流量或修正过细键维度；
调整`max_keys`前评估内存/Redis容量，不通过清空或淘汰活跃桶给旧主体重置额度。
项目RateLimit适配层的后端故障WARN按实例限为30秒一条，带`suppressed`并在恢复时记录一次INFO；
这不影响本包每次Check的判定或HTTP适配层逐请求指标，纯客户端取消不计为故障/恢复。

## 本包不写日志

运行期事件经 `WithObserver` 交给调用方：

```go
type myObserver struct{}

func (myObserver) BackendPhaseChanged(from, to ratelimit.HealthPhase) {
    log.Warn("rate limit backend phase changed", "from", from, "to", to)
}

func (myObserver) FellBackToLocal(policyID string) {
    log.Warn("rate limit degraded to local shadow bucket", "policy", policyID)
}
```

不注入时事件被丢弃，包不产生任何输出。

相位迁移事件在解锁后同步通知，可能并发调用；实现应快速记录并保证自身并发安全。
慢观察器会延迟其当前调用者，不持有健康状态锁；一次故障期间可能有多次探测相位迁移。

## 时钟处理

Lua 脚本对时间有两条独立规则：

- **回拨**：传入时间早于上次补充时刻时不倒退虚拟时间，按零流逝处理。倒退会让下一次请求补出一大段本不该有的额度。
- **前向漂移超限**：不推进、不写回任何状态，直接返回故障态，由 `FailureMode` 决定处置。绝不"钳制到上限再继续算"——那样每个请求都把虚拟时间往前推一个上限，于是每个请求白得 `上限 × rate` 个令牌，限流完全失效。

## 全整数运算

Lua 的 `number` 是 IEEE-754 double。浮点令牌在长时间运行与多副本下会漂移，而漂移方向不可预测——可能超发配额也可能误杀。

因此内部换算成 microtokens（1 令牌 = 10⁶）全整数运算，`Validate` 保证换算后不越过 2⁵³ 的精确整数边界。越界之后运算开始丢精度，而表现是**限流悄悄失准**，没有任何报错。

探针DEL失败不改变已验证的EVALSHA能力。首个清理错误由`RedisBackend.ProbeCleanupError()`保留，可用`WithProbeCleanupErrorHandler(func(error))`接入调用方日志；每个后端只通知一次，既有Observer接口不变，包自身不输出日志。未删除探针键仍由创建它们的Lua TTL回收（默认探针策略约61秒），不能宣称立即清理完成。项目bootstrap已将通知接入gtkit日志。
