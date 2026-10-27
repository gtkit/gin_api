package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// AllowRequest 报告是否允许访问后端。并发 Redis 调用在内部同时保存准入凭证。
func (s *HealthState) AllowRequest(ctx context.Context) bool {
	_, allowed := s.beginAttempt(ctx)
	return allowed
}

// Observe 上报独立健康样本；纯取消不计成功或失败。
// RedisBackend 使用携带准入凭证的内部入口隔离迟到结果。
func (s *HealthState) Observe(err error) {
	if s == nil {
		return
	}
	now := s.now()
	s.mu.Lock()
	s.observeLocked(healthAttempt{generation: s.generation, probe: s.probeInFlight}, err, now)
	events := s.takePendingLocked()
	s.mu.Unlock()
	s.notify(events)
}

func TestHealthCancellationTreesDoNotHideDependencyFailures(t *testing.T) {
	t.Parallel()
	dependency := errors.New("dependency failed")
	for _, tt := range []struct {
		name string
		err  error
		pure bool
	}{
		{name: "canceled", err: context.Canceled, pure: true},
		{name: "wrapped canceled", err: fmt.Errorf("request: %w", context.Canceled), pure: true},
		{name: "joined canceled", err: errors.Join(nil, context.Canceled, fmt.Errorf("nested: %w", context.Canceled)), pure: true},
		{name: "nested canceled tree", err: fmt.Errorf("outer: %w", errors.Join(context.Canceled, errors.Join(nil, context.Canceled))), pure: true},
		{name: "canceled and dependency", err: errors.Join(context.Canceled, dependency)},
		{name: "wrapped mixed tree", err: fmt.Errorf("outer: %w", errors.Join(context.Canceled, fmt.Errorf("nested: %w", io.EOF)))},
		{name: "deadline is dependency failure", err: context.DeadlineExceeded},
		{name: "canceled and deadline", err: errors.Join(context.Canceled, context.DeadlineExceeded)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s, _ := newTestHealth(t, HealthPolicy{FailureThreshold: 2})
			s.Observe(dependency)
			s.Observe(tt.err)
			if !tt.pure {
				if s.Phase() != PhaseDegraded {
					t.Fatalf("mixed/dependency failure left phase %s, want degraded", s.Phase())
				}
				return
			}
			if s.Phase() != PhaseHealthy {
				t.Fatalf("pure cancellation changed phase to %s", s.Phase())
			}
			// Cancellation cannot count as success and erase the previous failure.
			s.Observe(dependency)
			if s.Phase() != PhaseDegraded {
				t.Fatalf("cancellation erased the previous failure: phase=%s", s.Phase())
			}
		})
	}
}

func TestHealthCanceledProbePreservesRecoveryThresholdAndInterval(t *testing.T) {
	t.Parallel()
	for _, priorSuccess := range []bool{false, true} {
		t.Run(fmt.Sprintf("prior_success=%v", priorSuccess), func(t *testing.T) {
			t.Parallel()
			policy := HealthPolicy{FailureThreshold: 1, SuccessThreshold: 2, Cooldown: 30 * time.Second, ProbeInterval: 5 * time.Second}
			s, clock := newTestHealth(t, policy)
			s.Observe(io.EOF)
			clock.advance(policy.Cooldown)
			if priorSuccess {
				attempt, allowed := s.beginAttempt(t.Context())
				if !allowed {
					t.Fatal("first recovery probe denied")
				}
				s.observeAttempt(attempt, nil)
				clock.advance(policy.ProbeInterval)
			}
			attempt, allowed := s.beginAttempt(t.Context())
			if !allowed {
				t.Fatal("cancelable recovery probe denied")
			}
			s.observeAttempt(attempt, fmt.Errorf("request: %w", context.Canceled))
			if s.Phase() != PhaseDegraded {
				t.Fatalf("canceled probe phase=%s, want degraded", s.Phase())
			}
			clock.advance(policy.ProbeInterval - time.Nanosecond)
			if s.AllowRequest(t.Context()) {
				t.Fatal("canceled probe permitted another probe before ProbeInterval")
			}
			clock.advance(time.Nanosecond)
			retry, allowed := s.beginAttempt(t.Context())
			if !allowed {
				t.Fatal("canceled probe restarted Cooldown instead of ProbeInterval, or retained ownership")
			}
			s.observeAttempt(retry, nil)
			want := PhaseDegraded
			if priorSuccess {
				want = PhaseHealthy
			}
			if s.Phase() != want {
				t.Fatalf("phase=%s, want %s: canceled probe altered recovery samples", s.Phase(), want)
			}
		})
	}
}

type controlledRateLimitRunner struct {
	*fakeRedis
	process func(context.Context, redis.Cmder) error
}

func (r controlledRateLimitRunner) Process(ctx context.Context, cmd redis.Cmder) error {
	return r.process(ctx, cmd)
}

func healthTestBackend(client scriptRunner, health *HealthState) *RedisBackend {
	backend := &RedisBackend{client: client, health: health, observer: nopObserver{}}
	backend.sha.Store(new("test-sha"))
	return backend
}

func TestRedisBackendLateResultsCannotReleaseAnotherProbe(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		err  error
	}{
		{name: "success"},
		{name: "failure", err: io.EOF},
		{name: "cancellation", err: context.Canceled},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			policy := HealthPolicy{FailureThreshold: 1, SuccessThreshold: 1, Cooldown: time.Second, ProbeInterval: time.Second}
			health, clock := newTestHealth(t, policy)
			oldEntered, probeEntered := make(chan struct{}), make(chan struct{})
			oldRelease, probeRelease := make(chan struct{}), make(chan struct{})
			unblockOld, unblockProbe := sync.OnceFunc(func() { close(oldRelease) }), sync.OnceFunc(func() { close(probeRelease) })
			defer unblockOld()
			defer unblockProbe()
			client := controlledRateLimitRunner{fakeRedis: &fakeRedis{}, process: func(ctx context.Context, cmd redis.Cmder) error {
				switch cmd.Args()[3] {
				case "old":
					close(oldEntered)
					<-oldRelease
					if tt.err != nil {
						cmd.SetErr(tt.err)
						return tt.err
					}
				case "probe":
					close(probeEntered)
					<-probeRelease
				}
				value, ok := cmd.(interface{ SetVal(any) })
				if !ok {
					return fmt.Errorf("unexpected Redis command %T", cmd)
				}
				value.SetVal([]any{int64(statusAllowed), int64(0), int64(0)})
				return nil
			}}
			backend := healthTestBackend(client, health)
			oldDone := make(chan error, 1)
			go func() {
				_, err := backend.check(t.Context(), redisTestPolicy(), "old")
				oldDone <- err
			}()
			<-oldEntered
			health.Observe(io.ErrUnexpectedEOF)
			clock.advance(policy.Cooldown)
			probeDone := make(chan error, 1)
			go func() {
				_, err := backend.check(t.Context(), redisTestPolicy(), "probe")
				probeDone <- err
			}()
			<-probeEntered
			unblockOld()
			if err := <-oldDone; !errors.Is(err, tt.err) {
				t.Fatalf("late request err=%v, want %v", err, tt.err)
			}
			if health.Phase() != PhaseProbing || health.AllowRequest(t.Context()) {
				t.Fatalf("late %s released or completed someone else's probe: phase=%s", tt.name, health.Phase())
			}
			unblockProbe()
			if err := <-probeDone; err != nil || health.Phase() != PhaseHealthy {
				t.Fatalf("owned probe err=%v phase=%s, want healthy", err, health.Phase())
			}
		})
	}
}

func TestHealthCanceledAttemptCannotCompleteItsReplacement(t *testing.T) {
	t.Parallel()
	policy := HealthPolicy{FailureThreshold: 1, SuccessThreshold: 1, ProbeInterval: time.Second}
	s, clock := newTestHealth(t, policy)
	s.Observe(io.EOF)
	old, allowed := s.beginAttempt(t.Context())
	if !allowed {
		t.Fatal("first probe denied")
	}
	s.observeAttempt(old, context.Canceled)
	clock.advance(policy.ProbeInterval)
	replacement, allowed := s.beginAttempt(t.Context())
	if !allowed {
		t.Fatal("replacement probe denied")
	}
	for _, late := range []error{nil, context.Canceled, io.EOF} {
		s.observeAttempt(old, late)
		if s.Phase() != PhaseProbing || s.AllowRequest(t.Context()) {
			t.Fatal("canceled attempt changed the replacement probe's ownership")
		}
	}
	s.observeAttempt(replacement, nil)
	if s.Phase() != PhaseHealthy {
		t.Fatalf("replacement phase=%s, want healthy", s.Phase())
	}
}

func TestRedisPreCanceledRequestDoesNotCallOrClaimBackend(t *testing.T) {
	t.Parallel()
	for _, degraded := range []bool{false, true} {
		t.Run(fmt.Sprintf("degraded=%v", degraded), func(t *testing.T) {
			t.Parallel()
			fake := &fakeRedis{}
			health := NewHealthState(HealthPolicy{FailureThreshold: 1, SuccessThreshold: 1}, nil)
			if degraded {
				health.forceDegraded()
			}
			backend := healthTestBackend(fake, health)
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			_, err := backend.check(ctx, redisTestPolicy(), "canceled")
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("canceled request err=%v", err)
			}
			_, calls, _ := fake.counts()
			if calls != 0 || !health.AllowRequest(t.Context()) {
				t.Fatalf("canceled request called Redis %d times or retained probe ownership", calls)
			}
		})
	}
}

func TestFallbackPureCancellationDoesNotSpendShadowTokens(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		err  error
		pure bool
	}{
		{name: "pure", err: fmt.Errorf("request: %w", context.Canceled), pure: true},
		{name: "mixed", err: errors.Join(context.Canceled, io.EOF)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			fake := &fakeRedis{evalErrs: []error{tt.err}}
			backend := healthTestBackend(fake, NewHealthState(DefaultHealthPolicy(), nil))
			policy := redisTestPolicy()
			policy.Burst, policy.Rate, policy.FailureMode = 1, 0.01, FailureLocalFallback
			fallback, err := NewFallbackBackend(backend, policy)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(fallback.Close)
			decision, err := fallback.Check(t.Context(), "subject")
			if tt.pure {
				if !errors.Is(err, context.Canceled) || decision.Outcome != OutcomeBackendUnavailable {
					t.Fatalf("pure cancellation decision=%s err=%v", decision.Outcome, err)
				}
			} else if err != nil || !decision.Allowed() || !decision.Degraded {
				t.Fatalf("mixed failure did not use fallback: decision=%+v err=%v", decision, err)
			}
			shadow, err := fallback.shadow.Check(t.Context(), "subject")
			if err != nil || shadow.Allowed() != tt.pure {
				t.Fatalf("shadow allowed=%v err=%v, want %v", shadow.Allowed(), err, tt.pure)
			}
		})
	}
}

// degrade 用连续失败把状态机打到 Degraded。
func degrade(s *HealthState) {
	for range DefaultHealthPolicy().FailureThreshold {
		s.Observe(errors.New("backend down"))
	}
}

// reentrantObserver 在回调内重入状态机的公开方法。
type reentrantObserver struct {
	state *HealthState
	calls int
}

func (o *reentrantObserver) BackendPhaseChanged(HealthPhase, HealthPhase) {
	o.calls++
	_ = o.state.Phase()
	_ = o.state.AllowRequest(context.Background())
}

func (o *reentrantObserver) FellBackToLocal(string) {}

// TestHealthObserverReentrancyDoesNotDeadlock 钉住通知出锁（reusable-packages spec）：
// Observer 回调内重入公开方法不得死锁；不能把"Observer 只准写日志"
// 当成没有编译器背书的隐式契约。
func TestHealthObserverReentrancyDoesNotDeadlock(t *testing.T) {
	t.Parallel()

	obs := &reentrantObserver{}
	s := NewHealthState(DefaultHealthPolicy(), obs)
	obs.state = s

	done := make(chan struct{})
	go func() {
		degrade(s) // 触发 Healthy→Degraded 迁移与回调
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("state machine deadlocked when the observer re-entered a public method")
	}
	if obs.calls == 0 {
		t.Fatal("observer was never notified")
	}
}

// blockingPhaseObserver 的回调阻塞到 release 关闭。
type blockingPhaseObserver struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (o *blockingPhaseObserver) BackendPhaseChanged(HealthPhase, HealthPhase) {
	o.once.Do(func() { close(o.entered) })
	<-o.release
}

func (o *blockingPhaseObserver) FellBackToLocal(string) {}

// TestHealthSlowObserverDoesNotBlockDecisions 钉住慢 Observer 的隔离：
// 回调长时间不返回时，并发的限流判定不得被其阻塞。
func TestHealthSlowObserverDoesNotBlockDecisions(t *testing.T) {
	t.Parallel()

	obs := &blockingPhaseObserver{entered: make(chan struct{}), release: make(chan struct{})}
	defer close(obs.release)
	s := NewHealthState(DefaultHealthPolicy(), obs)

	go degrade(s)
	select {
	case <-obs.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("observer was never notified")
	}

	// 回调仍阻塞中：判定路径必须立即返回。
	decided := make(chan bool, 1)
	go func() { decided <- s.AllowRequest(context.Background()) }()
	select {
	case <-decided:
	case <-time.After(2 * time.Second):
		t.Fatal("AllowRequest was blocked by a slow observer")
	}
}

// orderObserver 记录通知顺序。
type orderObserver struct {
	mu     sync.Mutex
	events []phaseTransition
}

func (o *orderObserver) BackendPhaseChanged(from, to HealthPhase) {
	o.mu.Lock()
	o.events = append(o.events, phaseTransition{from: from, to: to})
	o.mu.Unlock()
}

func (o *orderObserver) FellBackToLocal(string) {}

func (o *orderObserver) snapshot() []phaseTransition {
	o.mu.Lock()
	defer o.mu.Unlock()
	return append([]phaseTransition(nil), o.events...)
}

// TestHealthNotificationsPreserveOrder 钉住通知顺序与迁移发生顺序一致：
// Healthy→Degraded→Probing→Degraded 的序列必须按序送达。
func TestHealthNotificationsPreserveOrder(t *testing.T) {
	t.Parallel()

	obs := &orderObserver{}
	clock := &fakeClock{now: time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)}
	s := NewHealthState(DefaultHealthPolicy(), obs)
	s.now = clock.Now

	degrade(s)                                        // Healthy→Degraded
	clock.advance(DefaultHealthPolicy().Cooldown + 1) // 越过冷却
	if !s.AllowRequest(context.Background()) {        // Degraded→Probing（拿到探测所有权）
		t.Fatal("probe request was not allowed after cooldown")
	}
	s.Observe(errors.New("still down")) // Probing→Degraded

	want := []phaseTransition{
		{from: PhaseHealthy, to: PhaseDegraded},
		{from: PhaseDegraded, to: PhaseProbing},
		{from: PhaseProbing, to: PhaseDegraded},
	}
	got := obs.snapshot()
	if len(got) != len(want) {
		t.Fatalf("events = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event[%d] = %v, want %v (通知顺序必须与迁移发生顺序一致)", i, got[i], want[i])
		}
	}
}

// TestNotificationsAreNotStranded 钉住通知不滞留（reusable-packages spec）：
// 每次相位迁移都被立即发出一次，不存在"等下一次状态活动才发"的窗口。
//
// 曾经用"单一发出者 + TryLock 交接"规避重入死锁：拿不到锁就返回，于是
// 持有者恰在收尾时入队的事件要等下一次活动才发出——一个降级事件可能
// 因此延迟到故障已经结束。
func TestNotificationsAreNotStranded(t *testing.T) {
	t.Parallel()

	obs := &orderObserver{}
	clock := &fakeClock{now: time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)}
	s := NewHealthState(DefaultHealthPolicy(), obs)
	s.now = clock.Now

	// 迁移 1：Healthy→Degraded。发出后事件数必须立刻变成 1。
	degrade(s)
	if got := len(obs.snapshot()); got != 1 {
		t.Fatalf("after the first transition the observer saw %d events, want 1 "+
			"(a transition must not wait for the next state activity)", got)
	}

	// 迁移 2：Degraded→Probing，同样立即可见。
	clock.advance(DefaultHealthPolicy().Cooldown + 1)
	if !s.AllowRequest(context.Background()) {
		t.Fatal("probe request was not allowed after cooldown")
	}
	if got := len(obs.snapshot()); got != 2 {
		t.Fatalf("after the second transition the observer saw %d events, want 2", got)
	}
}

// countingObserver 数事件次数。
type countingObserver struct {
	mu            sync.Mutex
	phaseChanges  int
	localFallback int
}

func (o *countingObserver) BackendPhaseChanged(HealthPhase, HealthPhase) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.phaseChanges++
}

func (o *countingObserver) FellBackToLocal(string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.localFallback++
}

func (o *countingObserver) counts() (phase, fallback int) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.phaseChanges, o.localFallback
}

// TestObserverStaysLowCardinality 钉住 Observer 的契约：**不是每请求回调**。
//
// 本包的 Observer 是给日志用的：它的文档承诺事件低基数，因此实现可以放心地
// 每次都写一条日志。哪天有人为了做指标在判定路径上加一个 Observer 事件，
// 一次流量高峰就会把调用方的日志刷满，而信息量只有第一条——那种改动在
// 功能测试里毫无迹象，只有上线后才发现。
//
// 每请求粒度的观测由**调用方**在中间件里做（那里判定结果已经拿到，
// 记一次指标就是一次计数器递增）。两者分处不同的包，是这条边界的实际形态。
func TestObserverStaysLowCardinality(t *testing.T) {
	t.Parallel()

	observer := &countingObserver{}
	fake := &fakeRedis{
		// 稳定放行：健康路径上不该有任何相位迁移。
		evalResults: []any{[]any{int64(1), int64(20), int64(0)}},
	}

	backend, err := NewRedisBackend(context.Background(), fake,
		WithKeyPrefix("svc"), WithObserver(observer))
	if err != nil {
		t.Fatalf("NewRedisBackend: %v", err)
	}

	policy := Policy{
		ID: "p", Rate: 100, Burst: 20,
		Scope: ScopeIP, Store: StoreRedis, FailureMode: FailureClosed, MaxKeys: 1000,
	}

	const requests = 200
	for range requests {
		if _, err := backend.check(context.Background(), policy, "subject"); err != nil {
			t.Fatalf("check: %v", err)
		}
	}

	phase, fallback := observer.counts()
	if phase != 0 || fallback != 0 {
		t.Fatalf("%d healthy requests produced %d phase events and %d fallback events, "+
			"want 0 and 0: the Observer contract is low-cardinality lifecycle events, "+
			"and per-request events there would flood the caller's logs",
			requests, phase, fallback)
	}
}

// --- 健康状态机 ---

func newTestHealth(t *testing.T, policy HealthPolicy) (*HealthState, *fakeClock) {
	t.Helper()
	clock := &fakeClock{now: time.Date(2026, 8, 3, 12, 0, 0, 0, time.UTC)}
	s := NewHealthState(policy, nil)
	s.now = clock.Now
	return s, clock
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

// TestHealthDegradesOnlyAtThreshold 验证单次抖动不降级。
func TestHealthDegradesOnlyAtThreshold(t *testing.T) {
	t.Parallel()

	policy := HealthPolicy{FailureThreshold: 3, SuccessThreshold: 2, Cooldown: time.Second, ProbeInterval: time.Second}
	s, _ := newTestHealth(t, policy)

	boom := errors.New("dial tcp: connection refused")
	for i := 1; i < policy.FailureThreshold; i++ {
		s.Observe(boom)
		if s.Phase() != PhaseHealthy {
			t.Fatalf("phase after %d failures = %s, want healthy", i, s.Phase())
		}
	}
	s.Observe(boom)
	if s.Phase() != PhaseDegraded {
		t.Fatalf("phase after %d failures = %s, want degraded", policy.FailureThreshold, s.Phase())
	}
}

// TestHealthDegradedStopsHittingBackend 钉住"降级期间不再每请求打后端"。
//
// 继续打只会给一个已经出问题的依赖持续加压，并让每个请求都白等一次超时。
// 冷却期内只允许**一个**探测请求通过。
func TestHealthDegradedStopsHittingBackend(t *testing.T) {
	t.Parallel()

	policy := HealthPolicy{FailureThreshold: 1, SuccessThreshold: 1, Cooldown: 30 * time.Second, ProbeInterval: 5 * time.Second}
	s, clock := newTestHealth(t, policy)

	s.Observe(errors.New("boom"))
	if s.Phase() != PhaseDegraded {
		t.Fatalf("phase = %s, want degraded", s.Phase())
	}

	ctx := context.Background()
	for i := range 10 {
		if s.AllowRequest(ctx) {
			t.Fatalf("request %d was sent to the backend during the cooldown window", i)
		}
	}

	// 冷却到期后只放行一个探测。
	clock.advance(policy.Cooldown)
	if !s.AllowRequest(ctx) {
		t.Fatal("no probe was allowed after the cooldown elapsed")
	}
	if s.AllowRequest(ctx) {
		t.Fatal("a second concurrent probe was allowed; probing must have a single owner")
	}
}

// TestHealthRecoveryIsConservative 验证恢复比降级保守。
//
// 误判恢复的代价是把流量重新导向一个还没好的依赖，因此需要连续成功
// 且间隔足够；单次成功不足以回到 Healthy。
func TestHealthRecoveryIsConservative(t *testing.T) {
	t.Parallel()

	policy := HealthPolicy{FailureThreshold: 1, SuccessThreshold: 2, Cooldown: 10 * time.Second, ProbeInterval: 5 * time.Second}
	s, clock := newTestHealth(t, policy)
	ctx := context.Background()

	s.Observe(errors.New("boom"))
	clock.advance(policy.Cooldown)

	if !s.AllowRequest(ctx) {
		t.Fatal("probe was not allowed after cooldown")
	}
	s.Observe(nil)
	if s.Phase() == PhaseHealthy {
		t.Fatalf("phase = healthy after a single successful probe; recovery must require %d",
			policy.SuccessThreshold)
	}
}

// TestHealthProbeFailureRestartsCooldown 验证探测失败退回并重新冷却。
func TestHealthProbeFailureRestartsCooldown(t *testing.T) {
	t.Parallel()

	policy := HealthPolicy{FailureThreshold: 1, SuccessThreshold: 1, Cooldown: 10 * time.Second, ProbeInterval: time.Second}
	s, clock := newTestHealth(t, policy)
	ctx := context.Background()

	s.Observe(errors.New("boom"))
	clock.advance(policy.Cooldown)

	if !s.AllowRequest(ctx) {
		t.Fatal("probe was not allowed after cooldown")
	}
	s.Observe(errors.New("still down"))

	if s.Phase() != PhaseDegraded {
		t.Fatalf("phase after a failed probe = %s, want degraded", s.Phase())
	}
	// 冷却重新开始：立刻再探必须被拒绝。
	if s.AllowRequest(ctx) {
		t.Fatal("a probe was allowed immediately after a failed probe; the cooldown must restart")
	}
}

// TestHealthProbeOwnershipIsSingle 在并发下验证探测的单一所有权。
func TestHealthProbeOwnershipIsSingle(t *testing.T) {
	t.Parallel()

	policy := HealthPolicy{FailureThreshold: 1, SuccessThreshold: 1, Cooldown: 0, ProbeInterval: time.Hour}
	s, _ := newTestHealth(t, policy)
	s.Observe(errors.New("boom"))

	ctx := context.Background()
	const concurrent = 16
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		allowed int
	)
	wg.Add(concurrent)
	for range concurrent {
		go func() {
			defer wg.Done()
			if s.AllowRequest(ctx) {
				mu.Lock()
				allowed++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()

	if allowed != 1 {
		t.Fatalf("allowed probes = %d, want exactly 1", allowed)
	}
}
