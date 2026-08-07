package ratelimit

import "context"

// UnavailableLimiter 是"后端已配置但当前不可用"的执行体。
//
// 每次判定恒报 OutcomeBackendUnavailable，于是**调用方按策略自己的
// FailureMode 决定放行还是拒绝**——语义与"后端在运行期挂掉"完全一致，
// 不静默改变任何策略的限额。
//
// 用途：启动期后端不可达且不存在 fail_closed 策略时顶替真实后端，
// 让进程能启动（fail_open / local_fallback 的意义就是可用性优先），
// 而不是让一次可降级的故障变成起不来。
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

// Check 恒报后端不可用。
func (l *UnavailableLimiter) Check(context.Context, string) (Decision, error) {
	return Decision{Outcome: OutcomeBackendUnavailable, Limit: l.policy.Burst}, nil
}

// Close 无资源可释放。
func (l *UnavailableLimiter) Close() {}
