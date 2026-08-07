package middleware

import (
	"testing"

	"github.com/gtkit/gin-api/internal/pkg/metrics"
	"github.com/gtkit/gin-api/pkg/ratelimit"
)

// TestRateLimitOutcomeNeverDefaultsToAllowed 钉住指标翻译的兜底方向
// （rate-limiting spec）：未指定与未知取值都归故障类，不得记成放行。
//
// 归成放行会让一处漏赋值在监控上看起来是一段正常流量——而那正是最需要
// 被发现的时候。
func TestRateLimitOutcomeNeverDefaultsToAllowed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		outcome ratelimit.Outcome
		want    metrics.RateLimitOutcome
	}{
		{name: "零值归故障", outcome: ratelimit.Outcome(0), want: metrics.RateLimitFailure},
		{name: "未指定归故障", outcome: ratelimit.OutcomeUnspecified, want: metrics.RateLimitFailure},
		{name: "越界值归故障", outcome: ratelimit.Outcome(200), want: metrics.RateLimitFailure},
		{name: "放行", outcome: ratelimit.OutcomeAllowed, want: metrics.RateLimitAllowed},
		{name: "限速", outcome: ratelimit.OutcomeRateLimited, want: metrics.RateLimitThrottled},
		{name: "键基数上限", outcome: ratelimit.OutcomeKeyBudgetExhausted, want: metrics.RateLimitKeyLimit},
		{name: "后端不可用", outcome: ratelimit.OutcomeBackendUnavailable, want: metrics.RateLimitFailure},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := rateLimitOutcome(tt.outcome); got != tt.want {
				t.Fatalf("rateLimitOutcome(%s) = %q, want %q", tt.outcome, got, tt.want)
			}
		})
	}
}
