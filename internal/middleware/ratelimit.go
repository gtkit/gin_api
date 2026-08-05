package middleware

import (
	"fmt"
	"strconv"

	ginc "github.com/gtkit/gin-api/internal/pkg/ginc"
	"github.com/gtkit/gin-api/internal/pkg/metrics"
	resp "github.com/gtkit/gin-api/internal/pkg/response"
	"github.com/gtkit/gin-api/pkg/ratelimit"

	"github.com/gin-gonic/gin"
	logger "github.com/gtkit/logger/v2"
	"go.uber.org/zap"
)

// 限流响应头。
//
// 用 X-RateLimit-* 而不是 RFC 9331 的 RateLimit-*：前者是当前生态里
// 客户端与网关实际在读的那一组，换成后者会让现有客户端的退避逻辑全部失效。
//
// 常量写 Go 的规范形式（首字母大写、其余小写）。这不是风格选择：
// net/http 在写入时会把 header 名规范化，所以线上实际发出的就是这个形式；
// 常量写成 X-RateLimit-Limit 只会让代码与线路上的字节不一致。
// HTTP header 名本身大小写不敏感，客户端匹配不受影响。
const (
	headerRateLimit     = "X-Ratelimit-Limit"
	headerRateRemaining = "X-Ratelimit-Remaining"
	headerRetryAfter    = "Retry-After"
)

// RateLimit 按策略对请求限流。
//
// 中间件只做三件事：组键、调用执行体、翻译成响应。限额算法、后端选择与
// 降级语义全在 ratelimit 包内——中间件不认识 Redis，也不认识令牌桶。
//
// **指标记在这里而不是 ratelimit 包内**：那个包是可独立发布的（pkg/），
// 不该依赖本仓的 internal 包，也不该替使用者选一套遥测栈。判定结果在这里
// 已经拿到，记一次的成本就是一次计数器递增。
//
// recorder 为 nil 时不记录。
func RateLimit(
	limiter ratelimit.Limiter, keys *ratelimit.KeyBuilder, recorder *metrics.Recorder,
) gin.HandlerFunc {
	if limiter == nil {
		panic("middleware: RateLimit requires a limiter")
	}
	if keys == nil {
		panic("middleware: RateLimit requires a key builder")
	}

	policy := limiter.Policy()

	return func(c *gin.Context) {
		subject, err := rateLimitSubject(c, policy)
		if err != nil {
			// 取不到主体标识说明装配有误（如 user scope 挂在公开路由上）。
			// 唯一安全的行为是拒绝——放行等于这条路由完全没有限流。
			logger.Error("rate limit subject unavailable; rejecting",
				zap.String("policy", policy.ID), zap.Error(err))
			recorder.RecordRateLimitDecision(c.Request.Context(), policy.ID, metrics.RateLimitFailure)
			rejectRateLimited(c, ratelimit.Decision{Limit: policy.Burst})
			return
		}

		key := keys.Build(policy, c.Request.Method, c.FullPath(), subject)

		decision, checkErr := limiter.Check(c.Request.Context(), key)
		if checkErr != nil {
			// 后端故障已由执行体按 FailureMode 翻译成最终判定，
			// 这里只负责留痕——不留的话，一次长时间的降级不会有任何记录。
			logger.Warn("rate limit backend error; the policy's failure mode decided the outcome",
				zap.String("policy", policy.ID),
				zap.String("outcome", decision.Outcome.String()),
				zap.Error(checkErr))
		}

		// **每次判定都记**，不只记拒绝：没有分母就算不出拒绝率，
		// 而"拒绝了多少次"单独看是没有意义的——流量翻倍时它也会翻倍。
		recorder.RecordRateLimitDecision(c.Request.Context(), policy.ID,
			rateLimitOutcome(decision.Outcome))

		writeRateLimitHeaders(c, decision)

		if decision.Allowed() {
			c.Next()
			return
		}
		rejectRateLimited(c, decision)
	}
}

// rateLimitSubject 取限流主体。
//
// IP 只经唯一 accessor 取得（架构传感器强制）：两处各自裸调 ClientIP() 时，
// 任何一处加了归一化都会让"日志里的 IP"与"被限流的 IP"指向不同主体。
func rateLimitSubject(c *gin.Context, policy ratelimit.Policy) (ratelimit.Subject, error) {
	switch policy.Scope {
	case ratelimit.ScopeGlobal:
		return ratelimit.Subject{Scope: policy.Scope}, nil

	case ratelimit.ScopeIP:
		return ratelimit.Subject{Scope: policy.Scope, Value: ginc.ClientIP(c)}, nil

	case ratelimit.ScopeUser:
		// 未认证时返回 0。**不能把 0 当成一个合法主体**：那会让所有匿名请求
		// 共用同一个桶，一个用户就能把它打满，而这条路由看起来"限流是生效的"。
		userID := ginc.UserIDFromContext(c)
		if userID == 0 {
			return ratelimit.Subject{}, fmt.Errorf(
				"policy %q is user-scoped but the request carries no authenticated identity", policy.ID)
		}
		return ratelimit.Subject{Scope: policy.Scope, Value: strconv.FormatInt(userID, 10)}, nil

	default:
		return ratelimit.Subject{}, fmt.Errorf("policy %q has an unspecified scope", policy.ID)
	}
}

// writeRateLimitHeaders 写限额与剩余量。
//
// 放行时也写：客户端据此提前退避比撞上 429 再退避更有价值，
// 而只在被拒时写的话，客户端只能靠试错来发现限额。
// writeRateLimitHeaders 写剩余配额，**只在比已有值更紧时覆盖**。
//
// 一条路由可以挂多条策略，而响应头只有一份。多条策略分布在处理链的不同位置——
// 鉴权前的策略执行时，鉴权后那条还没跑，因此无法先聚合再决定写哪个。
//
// "只在更紧时覆盖"让结果**不依赖执行顺序**：每条各自尝试收紧，最终留下的
// 必然是最紧的那条，也就不需要请求级的聚合器。
//
// 报出较宽松的那条会误导客户端：它据此认为还有余量，继续发送并被另一条策略
// 拒绝，退避逻辑因此失效。
func writeRateLimitHeaders(c *gin.Context, decision ratelimit.Decision) {
	h := c.Writer.Header()
	if existing := h.Get(headerRateRemaining); existing != "" {
		if previous, err := strconv.Atoi(existing); err == nil && previous <= decision.Remaining {
			return
		}
	}
	h.Set(headerRateLimit, strconv.Itoa(decision.Limit))
	h.Set(headerRateRemaining, strconv.Itoa(decision.Remaining))
}

// rejectRateLimited 写 429。
//
// Retry-After **向上取整为整秒且下限 1**：HTTP 的 Retry-After 是 delta-seconds
// 整数，写小数或毫秒是非法头；而截断会在不足 1 秒时输出 `Retry-After: 0`，
// 那既非法、语义上又等于"立刻重试"——恰好是限流最不希望客户端做的事。
func rejectRateLimited(c *gin.Context, decision ratelimit.Decision) {
	c.Writer.Header().Set(headerRetryAfter, strconv.Itoa(decision.RetryAfterSeconds()))
	resp.TooManyRequestsErr(c, "请求过于频繁，请稍后再试")
	c.Abort()
}

// rateLimitOutcome 把限流包的判定翻译成指标取值。
//
// 两套枚举而不是共用一套：ratelimit 是可独立发布的包，让它认识本仓的指标
// 类型就是一条反向依赖。翻译函数是这条边界的实际成本，且它很短。
func rateLimitOutcome(outcome ratelimit.Outcome) metrics.RateLimitOutcome {
	switch outcome {
	case ratelimit.OutcomeAllowed:
		return metrics.RateLimitAllowed
	case ratelimit.OutcomeRateLimited:
		return metrics.RateLimitThrottled
	case ratelimit.OutcomeKeyBudgetExhausted:
		return metrics.RateLimitKeyLimit
	case ratelimit.OutcomeBackendUnavailable:
		return metrics.RateLimitFailure
	default:
		// 上游新增了取值而这里没跟上。归到 failure 而不是 allowed：
		// 把未知情况记成"放行"会让一类新的拒绝在监控上完全消失。
		return metrics.RateLimitFailure
	}
}
