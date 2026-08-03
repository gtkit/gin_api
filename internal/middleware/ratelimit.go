package middleware

import (
	"errors"
	"fmt"
	"strconv"

	ginc "github.com/gtkit/gin-api/internal/pkg/ginc"
	resp "github.com/gtkit/gin-api/internal/pkg/response"
	"github.com/gtkit/gin-api/internal/ratelimit"
	"github.com/gtkit/gin-api/internal/router/routespec"

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
func RateLimit(limiter ratelimit.Limiter, keys *ratelimit.KeyBuilder) (gin.HandlerFunc, error) {
	if limiter == nil {
		return nil, errors.New("rate limit middleware requires a limiter")
	}
	if keys == nil {
		return nil, errors.New("rate limit middleware requires a key builder")
	}

	policy := limiter.Policy()

	return func(c *gin.Context) {
		subject, err := rateLimitSubject(c, policy)
		if err != nil {
			// 取不到主体标识说明装配有误（如 user scope 挂在公开路由上）。
			// 唯一安全的行为是拒绝——放行等于这条路由完全没有限流。
			logger.Error("rate limit subject unavailable; rejecting",
				zap.String("policy", policy.ID), zap.Error(err))
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

		writeRateLimitHeaders(c, decision)

		if decision.Allowed() {
			c.Next()
			return
		}
		rejectRateLimited(c, decision)
	}, nil
}

// rateLimitSubject 取限流主体。
//
// IP 只经唯一 accessor 取得（架构传感器强制）：两处各自裸调 ClientIP() 时，
// 任何一处加了归一化都会让"日志里的 IP"与"被限流的 IP"指向不同主体。
func rateLimitSubject(c *gin.Context, policy routespec.RateLimitPolicy) (ratelimit.Subject, error) {
	switch policy.Scope {
	case routespec.ScopeGlobal:
		return ratelimit.Subject{Scope: policy.Scope}, nil

	case routespec.ScopeIP:
		return ratelimit.Subject{Scope: policy.Scope, Value: ginc.ClientIP(c)}, nil

	case routespec.ScopeUser:
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
func writeRateLimitHeaders(c *gin.Context, decision ratelimit.Decision) {
	h := c.Writer.Header()
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
