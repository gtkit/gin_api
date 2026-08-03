package ratelimit

import (
	"math"
	"time"

	"github.com/gtkit/gin-api/internal/router/routespec"
)

// Outcome 是一次限流判定的结果。
type Outcome uint8

const (
	// OutcomeAllowed 放行。
	OutcomeAllowed Outcome = iota
	// OutcomeRateLimited 被限速。
	OutcomeRateLimited
	// OutcomeKeyBudgetExhausted 键基数达上限而被拒绝。
	//
	// 与限速分开是有意的：它说明"主体数量"异常（通常是键轮换攻击或
	// 键设计出错），而不是"某个主体请求太快"。两者混在一起会让
	// 一次键设计缺陷长期被当成正常的限速噪音。
	OutcomeKeyBudgetExhausted
	// OutcomeBackendUnavailable 后端不可用，调用方按 FailureMode 处理。
	OutcomeBackendUnavailable
)

func (o Outcome) String() string {
	switch o {
	case OutcomeAllowed:
		return "allowed"
	case OutcomeRateLimited:
		return "rate_limited"
	case OutcomeKeyBudgetExhausted:
		return "key_budget_exhausted"
	case OutcomeBackendUnavailable:
		return "backend_unavailable"
	default:
		return "allowed"
	}
}

// Decision 是限流判定的完整输出，够中间件写全部响应头。
type Decision struct {
	Outcome Outcome
	// Limit 是桶容量（X-RateLimit-Limit）。
	Limit int
	// Remaining 是剩余令牌数（X-RateLimit-Remaining），向下取整。
	Remaining int
	// RetryAfter 是建议的重试等待时长。
	RetryAfter time.Duration
	// Degraded 表示本次判定走的是降级路径（本地影子桶）。
	Degraded bool
}

// Allowed 报告本次是否放行。
func (d Decision) Allowed() bool { return d.Outcome == OutcomeAllowed }

// RetryAfterSeconds 把等待时长向上取整为整秒，下限 1。
//
// HTTP 的 `Retry-After` 是 **delta-seconds 整数**：写小数或毫秒值是非法头。
// 而**截断**（如 `int(d.Seconds())`）会在不足 1 秒时输出 `Retry-After: 0`，
// 那同样非法，且语义上等于"立刻重试"——恰好是限流最不希望客户端做的事。
func (d Decision) RetryAfterSeconds() int {
	if d.RetryAfter <= 0 {
		return 1
	}
	seconds := int(math.Ceil(d.RetryAfter.Seconds()))
	if seconds < 1 {
		return 1
	}
	return seconds
}

// resolveFailure 把"后端不可用"按 FailureMode 翻译成最终判定。
//
// local_fallback 不在这里处理：它需要一个真实的本地桶，由持有该桶的
// backend 自行完成切换。
func resolveFailure(policy routespec.RateLimitPolicy, limit int) Decision {
	switch policy.FailureMode {
	case FailureOpenMode:
		return Decision{Outcome: OutcomeAllowed, Limit: limit, Remaining: limit, Degraded: true}
	case FailureClosedMode:
		return Decision{
			Outcome:    OutcomeRateLimited,
			Limit:      limit,
			RetryAfter: backendUnavailableRetryAfter,
			Degraded:   true,
		}
	default:
		// 未知模式一律拒绝：限流的默认必须是保守方向。
		return Decision{
			Outcome:    OutcomeRateLimited,
			Limit:      limit,
			RetryAfter: backendUnavailableRetryAfter,
			Degraded:   true,
		}
	}
}

// 便于在本包内引用的别名，避免每处都写完整包路径。
const (
	FailureOpenMode     = routespec.FailureOpen
	FailureClosedMode   = routespec.FailureClosed
	FailureFallbackMode = routespec.FailureLocalFallback
)

// backendUnavailableRetryAfter 是 fail_closed 降级时给客户端的重试建议。
//
// 取一个固定的短值而不是策略的补充周期：后端不可用与"配额用完"是两件事，
// 用配额周期会让客户端等一个与实际恢复时间无关的时长。
const backendUnavailableRetryAfter = 5 * time.Second
