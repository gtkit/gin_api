package bootstrap

import (
	"context"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/platform/config"
)

// TestBuildRateLimitsClosesLimitersOnFailure 钉住装配失败的资源回收：
// 中途失败时已建执行体（含注册失败、未进注册表的孤儿）必须被关闭，
// 其后台清理 goroutine 随之退出。
//
// 不并行：断言依据是 goroutine 数收敛回装配前的水位。
func TestBuildRateLimitsClosesLimitersOnFailure(t *testing.T) {
	cases := []struct {
		name     string
		policies []config.RateLimitPolicyConfig
		wantErr  string
	}{
		{
			name: "后续策略非法时已建执行体被关闭",
			policies: []config.RateLimitPolicyConfig{
				policyConfig(nil),
				policyConfig(func(p *config.RateLimitPolicyConfig) {
					p.ID = "bad.policy"
					p.Scope = "bogus"
				}),
			},
			wantErr: "unknown scope",
		},
		{
			name: "注册失败的孤儿执行体被关闭",
			policies: []config.RateLimitPolicyConfig{
				policyConfig(nil),
				policyConfig(nil), // 同 ID 重复注册
			},
			wantErr: "register rate limit policy",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			before := runtime.NumGoroutine()

			cfg := &config.Config{
				App: config.AppConfig{Name: "svc"},
				RateLimit: config.RateLimitConfig{
					KeyHMACSecret: strings.Repeat("s", 32),
					Policies:      tc.policies,
				},
			}
			_, err := buildRateLimits(context.Background(), cfg, nil)
			if err == nil {
				t.Fatal("buildRateLimits accepted an invalid policy set")
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tc.wantErr)
			}

			eventually(t, 3*time.Second, func() bool { return runtime.NumGoroutine() <= before },
				"limiter goroutines were not released after assembly failure")
		})
	}
}
