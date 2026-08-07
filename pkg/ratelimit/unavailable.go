package ratelimit

import "context"

// UnavailableLimiter 是"后端已配置但当前不可用"的执行体。
//
// 每次判定都按策略自己的 FailureMode 给出**最终判定**（Degraded 置位），
// 语义与"后端在运行期挂掉"完全一致，不静默改变任何策略的限额。
//
// 翻译必须在这里完成，不能把 OutcomeBackendUnavailable 原样交出去：
// Limiter 的契约是返回最终判定，调用方只看 Decision.Allowed()。原样返回
// 会让 fail_open 策略在后端不可达时拒绝每一个请求——与该模式的语义相反。
//
// 用途：启动期后端不可达且不存在 fail_closed 策略时顶替真实后端，
// 让进程能启动（fail_open 的意义就是可用性优先），而不是让一次可降级的
// 故障变成起不来。
//
// **local_fallback 策略不该用它**：那个模式要求一个真实的本地桶，装配层
// 应当直接给出本地 backend；落到这里只会取保守方向（拒绝）。
//
// 它**不会自行恢复**：没有后端连接可探测。恢复靠进程重启，这一点必须
// 由构造它的装配层在日志里说清楚。
type UnavailableLimiter struct {
	policy Policy
}

// NewUnavailableLimiter 构造恒不可用的执行体。
func NewUnavailableLimiter(policy Policy) *UnavailableLimiter {
	return &UnavailableLimiter{policy: policy}
}

// Policy 返回它代表的策略（限额值不变，只是当下无法执行）。
func (l *UnavailableLimiter) Policy() Policy { return l.policy }

// Check 按策略的 FailureMode 给出最终判定。
func (l *UnavailableLimiter) Check(context.Context, string) (Decision, error) {
	return resolveFailure(l.policy, l.policy.Burst), nil
}

// Close 无资源可释放。
func (l *UnavailableLimiter) Close() {}
