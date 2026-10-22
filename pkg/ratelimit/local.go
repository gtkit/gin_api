package ratelimit

import (
	"context"
	"fmt"

	"github.com/gtkit/golimit/v2"
)

// LocalBackend 是进程内令牌桶。
//
// **多副本下有效限额 = 配置值 × 副本数**。它适合"高位防护上限"（挡住明显异常
// 的调用量），不适合精确业务限额——"每账号每分钟 5 次登录"在 3 个副本上
// 实际是 15 次，而那正是攻击者需要的那点余量。
type LocalBackend struct {
	policy  Policy
	limiter *golimit.Limiter
}

// NewLocalBackend 按策略构造一个独立的本地限流器。
//
// 每条策略一个实例，不共享：共享会让两条限额不同的策略互相消耗对方的 token，
// 而这种串扰在压测里表现为"限额比配置的更严"，极难定位。
func NewLocalBackend(policy Policy) (*LocalBackend, error) {
	if err := policy.Validate(); err != nil {
		return nil, fmt.Errorf("build local rate limit backend: %w", err)
	}

	limiter := golimit.New(policy.Rate,
		golimit.WithBurst(policy.Burst),
		// 达上限**拒绝新建**，不淘汰活跃桶。
		//
		// LRU 淘汰是一个看起来更"友好"的选择，实际是限流绕过：
		// 攻击者轮换超过 MaxKeys 个主体就能反复把旧桶挤掉，
		// 旧主体回来时拿到一个满桶，等于完全没有限流。
		golimit.WithMaxKeys(policy.MaxKeys),
	)

	return &LocalBackend{policy: policy, limiter: limiter}, nil
}

// Check 判定一次请求。
//
// 用 Check 而不是 Allow：只有 Check 返回 Result（Limit/Remaining/RetryAfter/Reason），
// Allow 只返回 bool，拿不到写响应头所需的任何信息，也无法区分
// "限速"与"键基数达上限"。
func (b *LocalBackend) Check(_ context.Context, key string) (Decision, error) {
	result := b.limiter.Check(key)

	decision := Decision{
		Limit:      result.Limit,
		Remaining:  clampRemaining(result.Remaining),
		RetryAfter: result.RetryAfter,
	}

	switch {
	case result.Allowed:
		decision.Outcome = OutcomeAllowed
	case result.Reason == golimit.RejectMaxKeys:
		decision.Outcome = OutcomeKeyBudgetExhausted
	default:
		decision.Outcome = OutcomeRateLimited
	}

	return decision, nil
}

// Close 释放后台清理 goroutine。
func (b *LocalBackend) Close() {
	if b == nil || b.limiter == nil {
		return
	}
	b.limiter.Close()
}

// Policy 返回该 backend 的策略。
func (b *LocalBackend) Policy() Policy { return b.policy }

// clampRemaining 把剩余令牌数向下取整并夹到非负。
//
// 响应头里出现负数会让客户端的退避逻辑得到一个无意义的输入。
func clampRemaining(remaining float64) int {
	if remaining <= 0 {
		return 0
	}
	return int(remaining)
}
