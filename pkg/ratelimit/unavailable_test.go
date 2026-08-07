package ratelimit

import (
	"context"
	"testing"
)

// TestUnavailableLimiterAppliesFailureMode 钉住恒不可用执行体**自己**完成
// FailureMode 翻译（rate-limiting spec）。
//
// Limiter 的契约是"返回最终判定"：调用方只看 Decision.Allowed()，没有任何
// 下游环节会把 OutcomeBackendUnavailable 再翻译一次。把该 Outcome 原样返回
// 会让 fail_open 策略在后端不可达时**拒绝每一个请求**——与该模式的语义相反，
// 也比"启动失败"更坏：进程看起来是健康的，流量却全被 429 掉。
func TestUnavailableLimiterAppliesFailureMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		mode        FailureMode
		wantOutcome Outcome
		wantAllowed bool
	}{
		{
			name:        "fail_open 放行",
			mode:        FailureOpen,
			wantOutcome: OutcomeAllowed,
			wantAllowed: true,
		},
		{
			name:        "fail_closed 拒绝",
			mode:        FailureClosed,
			wantOutcome: OutcomeRateLimited,
			wantAllowed: false,
		},
		{
			// local_fallback 需要一个真实的本地桶，不由本执行体承担；
			// 落到这里只能取保守方向（见 resolveFailure）。装配层必须把
			// 这类策略路由到本地 backend，而不是构造本执行体。
			name:        "local_fallback 取保守方向",
			mode:        FailureLocalFallback,
			wantOutcome: OutcomeRateLimited,
			wantAllowed: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			policy := Policy{
				ID: "cfg.but.down", Rate: 5, Burst: 20, Scope: ScopeIP,
				Store: StoreRedis, FailureMode: tt.mode, MaxKeys: 100,
			}
			l := NewUnavailableLimiter(policy)
			defer l.Close()

			if got := l.Policy(); got.ID != policy.ID || got.Burst != policy.Burst {
				t.Fatalf("Policy() = %+v, want the configured policy unchanged", got)
			}

			// 多次判定结果必须一致：它没有任何随请求变化的状态。
			for range 3 {
				d, err := l.Check(context.Background(), "any-key")
				if err != nil {
					t.Fatalf("Check returned an error: %v (the decision carries the verdict)", err)
				}
				if d.Outcome != tt.wantOutcome {
					t.Fatalf("Outcome = %s, want %s", d.Outcome, tt.wantOutcome)
				}
				if d.Allowed() != tt.wantAllowed {
					t.Fatalf("Allowed() = %v, want %v", d.Allowed(), tt.wantAllowed)
				}
				if !d.Degraded {
					t.Fatal("Degraded = false, want true: the verdict came from a failure mode, not from the backend")
				}
				if d.Limit != policy.Burst {
					t.Fatalf("Limit = %d, want the configured burst %d (limits must not change silently)",
						d.Limit, policy.Burst)
				}
			}
		})
	}
}
