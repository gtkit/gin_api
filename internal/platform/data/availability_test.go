package dao

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// newTestTracker 构造带固定时钟的跟踪器，让冷却与探测间隔可以确定性地推进。
//
// 用注入时钟而不是 time.Sleep：真实等待会让这组用例变成秒级，
// 且在负载高的机器上仍然会偶发失败——那正是被人加 -short 跳过的开始。
func newTestTracker(
	t *testing.T, policy AvailabilityPolicy, probe func(context.Context) error,
) (*AvailabilityTracker, *fakeClock) {
	t.Helper()

	clock := &fakeClock{now: time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)}
	tracker, err := NewAvailabilityTracker("redis", policy, probe)
	if err != nil {
		t.Fatalf("NewAvailabilityTracker: %v", err)
	}
	tracker.now = clock.Now
	return tracker, clock
}

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
}

func TestAvailabilityPolicyValidation(t *testing.T) {
	t.Parallel()

	valid := DefaultAvailabilityPolicy()

	cases := []struct {
		name    string
		policy  AvailabilityPolicy
		wantErr bool
	}{
		{name: "default policy is valid", policy: valid},
		{
			name:    "zero failure threshold rejected",
			policy:  AvailabilityPolicy{FailureThreshold: 0, SuccessThreshold: 1},
			wantErr: true,
		},
		{
			name:    "zero success threshold rejected",
			policy:  AvailabilityPolicy{FailureThreshold: 1, SuccessThreshold: 0},
			wantErr: true,
		},
		{
			name:    "negative cooldown rejected",
			policy:  AvailabilityPolicy{FailureThreshold: 1, SuccessThreshold: 1, Cooldown: -time.Second},
			wantErr: true,
		},
		{
			name:    "negative probe interval rejected",
			policy:  AvailabilityPolicy{FailureThreshold: 1, SuccessThreshold: 1, ProbeInterval: -time.Second},
			wantErr: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			_, err := NewAvailabilityTracker("redis", tc.policy, func(context.Context) error { return nil })
			if tc.wantErr && err == nil {
				t.Fatal("NewAvailabilityTracker() error = nil, want error on invalid policy")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("NewAvailabilityTracker() error = %v, want nil", err)
			}
		})
	}
}

// TestAvailabilityDegradesOnlyAtThreshold 钉住"单次抖动不降级"。
//
// 阈值若退化成 1，一次偶发的网络抖动就会把整个依赖标记为不可用，
// 而恢复要等一个完整的冷却窗口——抖动被放大成一段可见的服务降级。
func TestAvailabilityDegradesOnlyAtThreshold(t *testing.T) {
	t.Parallel()

	policy := AvailabilityPolicy{FailureThreshold: 3, SuccessThreshold: 2, Cooldown: time.Second, ProbeInterval: time.Second}
	tracker, _ := newTestTracker(t, policy, func(context.Context) error { return nil })

	boom := errors.New("boom")
	for i := 1; i < policy.FailureThreshold; i++ {
		tracker.Observe(boom)
		if got := tracker.State(); got != AvailabilityHealthy {
			t.Fatalf("after %d failures state = %s, want healthy", i, got)
		}
	}

	tracker.Observe(boom)
	if got := tracker.State(); got != AvailabilityDegraded {
		t.Fatalf("after %d failures state = %s, want degraded", policy.FailureThreshold, got)
	}
}

// TestAvailabilitySuccessResetsFailureRun 验证失败计数是**连续**的，不是累计的。
func TestAvailabilitySuccessResetsFailureRun(t *testing.T) {
	t.Parallel()

	policy := AvailabilityPolicy{FailureThreshold: 3, SuccessThreshold: 1, Cooldown: time.Second, ProbeInterval: time.Second}
	tracker, _ := newTestTracker(t, policy, func(context.Context) error { return nil })

	boom := errors.New("boom")
	tracker.Observe(boom)
	tracker.Observe(boom)
	tracker.Observe(nil) // 成功打断连败
	tracker.Observe(boom)
	tracker.Observe(boom)

	if got := tracker.State(); got != AvailabilityHealthy {
		t.Fatalf("state = %s, want healthy: 失败计数必须按连续段而非累计", got)
	}
}

// TestAvailabilityProbeRespectsCooldownAndInterval 钉住探测节奏。
//
// 没有冷却与间隔，降级期间每一次业务调用都会顺带探一次，
// 等于对一个已经出问题的依赖持续加压。
func TestAvailabilityProbeRespectsCooldownAndInterval(t *testing.T) {
	t.Parallel()

	policy := AvailabilityPolicy{
		FailureThreshold: 1, SuccessThreshold: 1,
		Cooldown: 5 * time.Second, ProbeInterval: 3 * time.Second,
	}

	var probes int
	probeErr := errors.New("still down")
	tracker, clock := newTestTracker(t, policy, func(context.Context) error {
		probes++
		return probeErr
	})

	tracker.Observe(errors.New("boom"))
	if tracker.State() != AvailabilityDegraded {
		t.Fatalf("state = %s, want degraded", tracker.State())
	}

	// 冷却未到：不探测。
	if probed, err := tracker.MaybeProbe(context.Background()); probed || err != nil {
		t.Fatalf("MaybeProbe before cooldown = (%v, %v), want (false, nil)", probed, err)
	}
	if probes != 0 {
		t.Fatalf("probes = %d, want 0 before cooldown elapses", probes)
	}

	// 冷却到期：探测一次，失败后重新进入冷却。
	clock.advance(policy.Cooldown)
	probed, err := tracker.MaybeProbe(context.Background())
	if !probed || !errors.Is(err, probeErr) {
		t.Fatalf("MaybeProbe after cooldown = (%v, %v), want (true, probe error)", probed, err)
	}
	if probes != 1 {
		t.Fatalf("probes = %d, want 1", probes)
	}
	if tracker.State() != AvailabilityDegraded {
		t.Fatalf("state after failed probe = %s, want degraded", tracker.State())
	}

	// 紧接着再调：仍在冷却内，不探测。
	if probed, _ := tracker.MaybeProbe(context.Background()); probed {
		t.Fatal("MaybeProbe probed again immediately after a failed probe")
	}
}

// TestAvailabilityRecoversAfterSuccessThreshold 验证恢复比降级保守：
// 需要连续 SuccessThreshold 次成功探测才回到 healthy。
func TestAvailabilityRecoversAfterSuccessThreshold(t *testing.T) {
	t.Parallel()

	policy := AvailabilityPolicy{
		FailureThreshold: 1, SuccessThreshold: 2,
		Cooldown: time.Second, ProbeInterval: time.Second,
	}
	tracker, clock := newTestTracker(t, policy, func(context.Context) error { return nil })

	tracker.Observe(errors.New("boom"))
	clock.advance(policy.Cooldown)

	if probed, err := tracker.MaybeProbe(context.Background()); !probed || err != nil {
		t.Fatalf("first probe = (%v, %v), want (true, nil)", probed, err)
	}
	if got := tracker.State(); got != AvailabilityProbing {
		t.Fatalf("state after 1 successful probe = %s, want probing (recovery must need %d)", got, policy.SuccessThreshold)
	}

	clock.advance(policy.ProbeInterval)
	// 探测成功后状态已不是 Degraded，MaybeProbe 会短路；此时靠真实业务调用的成功推进。
	tracker.Observe(nil)
	if got := tracker.State(); got != AvailabilityHealthy {
		t.Fatalf("state after %d successes = %s, want healthy", policy.SuccessThreshold, got)
	}
}

// TestAvailabilitySingleProbeOwnership 证明并发调用中只有一个真正发出探测。
//
// 没有这条，降级期间 N 个并发请求会各自探一次——故障期恰恰是并发最高的时候。
func TestAvailabilitySingleProbeOwnership(t *testing.T) {
	t.Parallel()

	policy := AvailabilityPolicy{
		FailureThreshold: 1, SuccessThreshold: 1,
		Cooldown: 0, ProbeInterval: time.Hour,
	}

	release := make(chan struct{})
	var (
		mu     sync.Mutex
		probes int
	)
	tracker, _ := newTestTracker(t, policy, func(context.Context) error {
		mu.Lock()
		probes++
		mu.Unlock()
		<-release // 卡住第一个探测，制造"探测在飞"的窗口
		return nil
	})

	tracker.Observe(errors.New("boom"))

	const concurrent = 8
	var wg sync.WaitGroup
	wg.Add(concurrent)
	for range concurrent {
		go func() {
			defer wg.Done()
			_, _ = tracker.MaybeProbe(context.Background())
		}()
	}

	// 等第一个探测真正进入执行体后再放行，确保并发窗口成立。
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		started := probes
		mu.Unlock()
		if started > 0 || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Millisecond)
	}
	close(release)
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if probes != 1 {
		t.Fatalf("probes = %d, want exactly 1: 并发探测必须只有一个所有者", probes)
	}
}

// TestPermanentlyDegradedTrackerNeverProbes 钉住"启动期失败保持降级到重启"。
//
// 此时没有客户端可探，也不做 client 热替换；Observe 与 MaybeProbe 都必须是空操作，
// 否则一个不存在的连接会被误判成已恢复。
func TestPermanentlyDegradedTrackerNeverProbes(t *testing.T) {
	t.Parallel()

	tracker := NewPermanentlyDegradedTracker("redis")

	if got := tracker.State(); got != AvailabilityDegraded {
		t.Fatalf("initial state = %s, want degraded", got)
	}

	tracker.Observe(nil) // 即便"成功"也不得转健康
	if got := tracker.State(); got != AvailabilityDegraded {
		t.Fatalf("state after success = %s, want degraded (no client exists to recover)", got)
	}

	probed, err := tracker.MaybeProbe(context.Background())
	if probed || err != nil {
		t.Fatalf("MaybeProbe = (%v, %v), want (false, nil)", probed, err)
	}
}

// TestAvailabilityTransitionsAreLowCardinality 验证观测只按固定枚举计数。
//
// 若以错误文本为 key，一次故障风暴就会制造无界的观测基数。
func TestAvailabilityTransitionsAreLowCardinality(t *testing.T) {
	t.Parallel()

	policy := AvailabilityPolicy{FailureThreshold: 1, SuccessThreshold: 1, Cooldown: 0, ProbeInterval: 0}
	tracker, _ := newTestTracker(t, policy, func(context.Context) error { return nil })

	for i := range 20 {
		tracker.Observe(errors.New("distinct error " + string(rune('a'+i%26))))
		tracker.Observe(nil)
	}

	transitions := tracker.Transitions()
	for state := range transitions {
		switch state {
		case AvailabilityHealthy, AvailabilityDegraded, AvailabilityProbing:
		case AvailabilityUnspecified:
			t.Fatal("transitions must never record the unspecified state")
		default:
			t.Fatalf("unexpected transition key %v", state)
		}
	}
	if len(transitions) > 3 {
		t.Fatalf("transition keys = %d, want at most 3 fixed states", len(transitions))
	}
}

func TestAvailabilityStateString(t *testing.T) {
	t.Parallel()

	cases := []struct {
		state AvailabilityState
		want  string
	}{
		{AvailabilityHealthy, "healthy"},
		{AvailabilityDegraded, "degraded"},
		{AvailabilityProbing, "probing"},
		{AvailabilityUnspecified, "unspecified"},
		{AvailabilityState(200), "unspecified"},
	}

	for _, tc := range cases {
		t.Run(tc.want, func(t *testing.T) {
			t.Parallel()
			if got := tc.state.String(); got != tc.want {
				t.Fatalf("String() = %q, want %q", got, tc.want)
			}
		})
	}
}
