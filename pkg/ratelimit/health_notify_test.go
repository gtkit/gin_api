package ratelimit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// notifyClock 是可推进的时间桩。
type notifyClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *notifyClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *notifyClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
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
// Observer 回调内重入公开方法不得死锁——对外发布的包不能把"Observer 只准
// 写日志"当成没有编译器背书的隐式契约。
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

// TestNewRedisBackendDegradedStart 钉住降级启动（rate-limiting spec）：
// WithDegradedStart 下脚本装载失败以 Degraded 相位构造成功、恢复探测可用；
// 缺省行为保持构造失败；取消不被降级吸收。
func TestNewRedisBackendDegradedStart(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		opts      []Option
		ctx       func() context.Context
		wantErr   bool
		wantPhase HealthPhase
	}{
		{
			name:      "降级启动：装载失败构造成功且相位为 Degraded",
			opts:      []Option{WithDegradedStart()},
			wantPhase: PhaseDegraded,
		},
		{
			name:    "缺省：装载失败即构造失败",
			wantErr: true,
		},
		{
			name: "取消不被降级吸收",
			opts: []Option{WithDegradedStart()},
			ctx: func() context.Context {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				return ctx
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := &fakeRedis{loadErr: errors.New("connection refused")}
			ctx := context.Background()
			if tt.ctx != nil {
				ctx = tt.ctx()
			}

			backend, err := NewRedisBackend(ctx, client, tt.opts...)
			if tt.wantErr {
				if err == nil {
					t.Fatal("NewRedisBackend succeeded, want construction failure")
				}
				return
			}
			if err != nil {
				t.Fatalf("NewRedisBackend: %v", err)
			}
			if got := backend.Health().Phase(); got != tt.wantPhase {
				t.Fatalf("phase = %s, want %s", got, tt.wantPhase)
			}
		})
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
	clock := &notifyClock{now: time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)}
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

// TestProbeRejectsNonAllowedDecision 是**回归测试**（rate-limiting spec）：
// 探针必须断言判定结果为放行，非放行即探测失败。
//
// 上一批给真实判定路径加了键基数准入，而探针共享那条路径——它用固定策略
// 标识、MaxKeys=1、且只看 err 不看 Decision，于是进程重启后探针恒定拿到
// "键基数拒绝"却报告成功：探针从此不验证任何东西，而它存在的全部理由
// 就是验证端到端能力。
func TestProbeRejectsNonAllowedDecision(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		status  int64
		wantErr bool
	}{
		{name: "放行即探测成功", status: statusAllowed},
		{name: "限速判定即探测失败", status: statusRejected, wantErr: true},
		{name: "键基数拒绝即探测失败", status: statusKeyBudgetDenied, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client := &fakeRedis{evalResults: []any{
				[]any{tt.status, int64(1000000), int64(0)},
			}}
			_, err := NewRedisBackend(context.Background(), client)
			if gotErr := err != nil; gotErr != tt.wantErr {
				t.Fatalf("NewRedisBackend = %v, wantErr = %v", err, tt.wantErr)
			}
		})
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
	clock := &notifyClock{now: time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)}
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

// blockingLimiter 的 Close 阻塞到 release 关闭。
type blockingLimiter struct {
	policy  Policy
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (l *blockingLimiter) Policy() Policy { return l.policy }

func (l *blockingLimiter) Check(context.Context, string) (Decision, error) {
	return Decision{Outcome: OutcomeAllowed}, nil
}

func (l *blockingLimiter) Close() {
	l.once.Do(func() { close(l.entered) })
	<-l.release
}

// TestRegistryCloseDoesNotHoldTheLock 钉住关闭执行体时不持注册表锁
// （reusable-packages spec）：Limiter 的 Close 是外部实现，持锁调用它会把
// "Close 必须又快又不重入"变成没有编译器背书的隐式契约，违反者得到死锁。
func TestRegistryCloseDoesNotHoldTheLock(t *testing.T) {
	t.Parallel()

	blocking := &blockingLimiter{
		policy: Policy{
			ID: "blocking", Rate: 1, Burst: 1, Scope: ScopeGlobal,
			Store: StoreLocal, FailureMode: FailureClosed, MaxKeys: 1,
		},
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	defer close(blocking.release)

	registry := NewRegistry()
	if err := registry.Register(blocking); err != nil {
		t.Fatalf("Register: %v", err)
	}
	registry.Freeze()

	closed := make(chan struct{})
	go func() { _ = registry.Close(); close(closed) }()

	select {
	case <-blocking.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("Close never reached the limiter")
	}

	// Close 仍卡在外部实现里：注册表的读取路径必须照常可用。
	done := make(chan struct{})
	go func() { _ = registry.IDs(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the registry lock is held while an external Close is running: " +
			"any Close that touches the registry would deadlock")
	}
}
