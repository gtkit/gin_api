package ratelimit

import (
	"strings"
	"testing"
	"time"
)

// TestZeroDecisionIsNotAllowed 钉住判定的零值落在拒绝方向（rate-limiting spec）。
//
// Go 的零值无处不在——结构体字面量漏填、切片扩容、map 取不到键都会产出它。
// 让 0 位是"放行"，意味着任何一次疏忽都朝不设防的方向失败，而这类疏忽在
// 编译期与测试里都不显形。
func TestZeroDecisionIsNotAllowed(t *testing.T) {
	t.Parallel()

	var zero Decision
	if zero.Allowed() {
		t.Fatal("Decision{}.Allowed() = true, want false: 未赋值的判定不得放行")
	}
	if zero.Outcome != OutcomeUnspecified {
		t.Fatalf("Decision{}.Outcome = %d, want OutcomeUnspecified (它必须占住零值)", zero.Outcome)
	}

	// map 取不到键、切片扩容都会走到这条路径，不只是显式的字面量。
	byPolicy := map[string]Decision{}
	if byPolicy["missing"].Allowed() {
		t.Fatal("map 取不到键得到的判定放行了")
	}
}

// TestOutcomeStringDistinguishesUnspecified 钉住未指定值在文本上可辨识。
//
// 回落到 "allowed" 会把证据抹掉：日志里看到 unspecified 才能定位到
// "有人返回了未赋值或越界的判定"。
func TestOutcomeStringDistinguishesUnspecified(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		outcome Outcome
		want    string
	}{
		{name: "零值", outcome: Outcome(0), want: "unspecified"},
		{name: "未指定", outcome: OutcomeUnspecified, want: "unspecified"},
		{name: "放行", outcome: OutcomeAllowed, want: "allowed"},
		{name: "限速", outcome: OutcomeRateLimited, want: "rate_limited"},
		{name: "键基数上限", outcome: OutcomeKeyBudgetExhausted, want: "key_budget_exhausted"},
		{name: "后端不可用", outcome: OutcomeBackendUnavailable, want: "backend_unavailable"},
		{name: "越界值", outcome: Outcome(200), want: "unspecified"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.outcome.String(); got != tt.want {
				t.Fatalf("String() = %q, want %q", got, tt.want)
			}
		})
	}
}

func testPolicy(mutate func(*Policy)) Policy {
	p := Policy{
		ID: "test.policy", Rate: 10, Burst: 5,
		Scope: ScopeIP, Store: StoreLocal,
		FailureMode: FailureClosed, MaxKeys: 100,
	}
	if mutate != nil {
		mutate(&p)
	}
	return p
}

// TestPolicyValidation 覆盖策略校验的全部规则。
//
// 限流配置错误的表现是"限额不是你以为的那个值"——它在生产上只有被打穿
// 或被误杀时才会发现，因此全部判定必须在启动期完成。
func TestPolicyValidation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		mutate   func(*Policy)
		wantErr  bool
		wantHint string
	}{
		{name: "valid policy", mutate: nil},
		{
			name:   "fractional rate is valid",
			mutate: func(p *Policy) { p.Rate = 0.5 },
		},
		{
			name:    "id charset enforced",
			mutate:  func(p *Policy) { p.ID = "Bad ID/with slash" },
			wantErr: true, wantHint: "policy id",
		},
		{
			name:    "empty id rejected",
			mutate:  func(p *Policy) { p.ID = "" },
			wantErr: true, wantHint: "policy id",
		},
		{
			name:    "zero rate rejected",
			mutate:  func(p *Policy) { p.Rate = 0 },
			wantErr: true, wantHint: "positive finite rate",
		},
		{
			name:    "zero burst rejected",
			mutate:  func(p *Policy) { p.Burst = 0 },
			wantErr: true, wantHint: "positive burst",
		},
		{
			name:    "zero max keys rejected",
			mutate:  func(p *Policy) { p.MaxKeys = 0 },
			wantErr: true, wantHint: "max keys",
		},
		{
			name:    "unspecified scope rejected",
			mutate:  func(p *Policy) { p.Scope = ScopeUnspecified },
			wantErr: true, wantHint: "must declare a scope",
		},
		{
			name:    "unspecified store rejected",
			mutate:  func(p *Policy) { p.Store = StoreUnspecified },
			wantErr: true, wantHint: "must declare a store",
		},
		{
			name:    "unspecified failure mode rejected",
			mutate:  func(p *Policy) { p.FailureMode = FailureUnspecified },
			wantErr: true, wantHint: "must declare a failure mode",
		},
		{
			// local store 降级到 local 没有意义——降级目标就是它自己。
			name: "local store with local fallback rejected",
			mutate: func(p *Policy) {
				p.Store = StoreLocal
				p.FailureMode = FailureLocalFallback
			},
			wantErr: true, wantHint: "nothing to fall back from",
		},
		{
			// 越过精确整数范围之后限流会**悄悄失准**，没有任何报错。
			name:    "burst overflowing the exact integer range rejected",
			mutate:  func(p *Policy) { p.Burst = 1 << 40 },
			wantErr: true, wantHint: "exact-integer range",
		},
		{
			name:    "rate below the smallest representable value rejected",
			mutate:  func(p *Policy) { p.Rate = 1e-9 },
			wantErr: true, wantHint: "smallest representable rate",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := testPolicy(tc.mutate).Validate()
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("Validate() = nil, want error")
			}
			if !strings.Contains(err.Error(), tc.wantHint) {
				t.Fatalf("error %q does not mention %q", err, tc.wantHint)
			}
		})
	}
}

// TestRetryAfterSecondsCeilsWithFloor 钉住 Retry-After 的取整规则。
//
// 截断（int(d.Seconds())）会在不足 1 秒时输出 `Retry-After: 0`——
// 那是非法头，且语义等于"立刻重试"，恰好是限流最不希望客户端做的事。
func TestRetryAfterSecondsCeilsWithFloor(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name  string
		after time.Duration
		want  int
	}{
		{name: "zero floors to one", after: 0, want: 1},
		{name: "negative floors to one", after: -time.Second, want: 1},
		{name: "sub-second floors to one", after: 100 * time.Millisecond, want: 1},
		{name: "exactly one second", after: time.Second, want: 1},
		{name: "just over a second ceils to two", after: 1001 * time.Millisecond, want: 2},
		{name: "fractional ceils up", after: 2500 * time.Millisecond, want: 3},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := (Decision{RetryAfter: tc.after}).RetryAfterSeconds(); got != tc.want {
				t.Fatalf("RetryAfterSeconds(%s) = %d, want %d", tc.after, got, tc.want)
			}
		})
	}
}

func TestFixedPointConversion(t *testing.T) {
	t.Parallel()

	p := testPolicy(func(p *Policy) { p.Rate, p.Burst = 2.5, 10 })

	if got := p.FixedPointRate(); got != 2_500_000 {
		t.Fatalf("FixedPointRate() = %d, want 2500000", got)
	}
	if got := p.FixedPointBurst(); got != 10_000_000 {
		t.Fatalf("FixedPointBurst() = %d, want 10000000", got)
	}
}

func TestRedisPolicyRefillTTLBoundary(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		rate  float64
		burst int
		store Store
		valid bool
		ttl   time.Duration
	}{
		{"exact day cap", 1, 86340, StoreRedis, true, 24 * time.Hour},
		{"one second beyond", 1, 86341, StoreRedis, false, 0},
		{"ceiling boundary", 2, 172680, StoreRedis, true, 24 * time.Hour},
		{"ceiling exceeds cap", 2, 172681, StoreRedis, false, 0},
		{"subsecond refill rounds upward", 2, 1, StoreRedis, true, 61 * time.Second},
		{"fixed point rounded slow rate accepted", 1.0 / 60, 1439, StoreRedis, true, 86399 * time.Second},
		{"fixed point rounded slow rate rejected", 1.0 / 60, 1440, StoreRedis, false, 0},
		{"minimum rate cannot refill in a day", 0.000001, 1, StoreRedis, false, 0},
		{"local store has no Redis residence cap", 0.000001, 1, StoreLocal, true, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			policy := redisTestPolicy()
			policy.Rate, policy.Burst, policy.Store = tc.rate, tc.burst, tc.store
			if err := policy.Validate(); (err == nil) != tc.valid {
				t.Fatalf("policy valid=%t err=%v", tc.valid, err)
			}
			if tc.store == StoreRedis {
				backend := &RedisBackend{}
				if _, err := NewRedisLimiter(backend, policy); (err == nil) != tc.valid {
					t.Fatalf("RedisLimiter bypassed refill validation: %v", err)
				}
				fallback, err := NewFallbackBackend(backend, policy)
				if (err == nil) != tc.valid {
					t.Fatalf("FallbackBackend bypassed refill validation: %v", err)
				}
				if fallback != nil {
					t.Cleanup(fallback.Close)
				}
				if tc.valid && bucketTTL(policy) != tc.ttl {
					t.Fatalf("bucket TTL=%s want=%s", bucketTTL(policy), tc.ttl)
				}
			}
		})
	}
}
