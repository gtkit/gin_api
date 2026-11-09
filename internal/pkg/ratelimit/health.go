package ratelimit

import (
	"context"
	"errors"
	"sync"
	"time"
)

// HealthPhase 是限流后端的健康相位。
type HealthPhase uint8

const (
	// PhaseHealthy 正常走后端。
	PhaseHealthy HealthPhase = iota
	// PhaseDegraded 后端不可用，按 FailureMode 处理，**不再每请求打后端**。
	PhaseDegraded
	// PhaseProbing 正在用一个探测请求确认是否已恢复。
	PhaseProbing
)

func (p HealthPhase) String() string {
	switch p {
	case PhaseHealthy:
		return "healthy"
	case PhaseDegraded:
		return "degraded"
	case PhaseProbing:
		return "probing"
	default:
		return "healthy"
	}
}

// HealthPolicy 是相位迁移的阈值与节奏。
type HealthPolicy struct {
	// FailureThreshold 连续依赖失败达到该值转入 Degraded。
	FailureThreshold int
	// SuccessThreshold 探测期连续成功达到该值才回 Healthy。
	SuccessThreshold int
	// Cooldown 是转入 Degraded 后到允许第一次探测的静默期。
	Cooldown time.Duration
	// ProbeInterval 是两次探测之间的最小间隔。
	ProbeInterval time.Duration
}

// DefaultHealthPolicy 返回默认阈值。
//
// 3 次连续失败才降级——单次网络抖动不该让整条限流链路切到降级路径；
// 30s 冷却让故障期的后端得以喘息；恢复需连续 2 次成功且间隔 ≥5s，
// 比降级判定保守，因为误判恢复的代价是把流量重新导向一个还没好的依赖。
func DefaultHealthPolicy() HealthPolicy {
	return HealthPolicy{
		FailureThreshold: 3,
		SuccessThreshold: 2,
		Cooldown:         30 * time.Second,
		ProbeInterval:    5 * time.Second,
	}
}

// HealthState 是**共享**的后端健康状态。
//
// 所有引用同一后端的策略共享它。按策略各建一套会让同一次故障触发 N 份
// 独立探测，对一个已经出问题的依赖形成探测风暴。
//
// 并发安全。
type HealthState struct {
	policy   HealthPolicy
	now      func() time.Time
	observer Observer

	mu        sync.Mutex
	phase     HealthPhase
	failures  int
	successes int
	// nextProbeAt 是允许下一次探测的最早时刻。
	nextProbeAt time.Time
	// probeInFlight 是探测的单一所有权标记。
	probeInFlight bool
	generation    uint64
	// pending 是待通知的相位迁移事件（受 mu 保护，入队序 = 迁移发生序）。
	//
	// 由公开入口在解锁前取走、解锁后发出：通知全程不持锁，因此 Observer
	// 重入不死锁、慢 Observer 不阻塞判定。
	pending []phaseTransition
}

// phaseTransition 是一次相位迁移的快照。
type phaseTransition struct {
	from, to HealthPhase
}

// NewHealthState 创建一个初始为 Healthy 的状态机。
//
// observer 为 nil 时相位迁移不产生任何输出。
func NewHealthState(policy HealthPolicy, observer Observer) *HealthState {
	if observer == nil {
		observer = nopObserver{}
	}
	return &HealthState{policy: policy, now: time.Now, phase: PhaseHealthy, observer: observer}
}

// forceDegraded 把状态机直接置为 Degraded（构造期降级启动用）。
//
// nextProbeAt 取当前时刻而不是冷却之后：后端可能在进程启动的下一秒
// 就恢复了，首个请求即可探测；探测失败后由既有冷却节奏接管。
func (s *HealthState) forceDegraded() {
	now := s.now()
	s.mu.Lock()
	s.transitionLocked(PhaseDegraded)
	s.probeInFlight = false
	s.nextProbeAt = now
	events := s.takePendingLocked()
	s.mu.Unlock()

	s.notify(events)
}

// Phase 返回当前相位。
func (s *HealthState) Phase() HealthPhase {
	if s == nil {
		return PhaseHealthy
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.phase
}

// healthAttempt 将完成结果绑定到准入时的健康代次和探测所有权。
type healthAttempt struct {
	generation uint64
	probe      bool
}

func (s *HealthState) beginAttempt(ctx context.Context) (healthAttempt, bool) {
	if ctx.Err() != nil {
		return healthAttempt{}, false
	}
	if s == nil {
		return healthAttempt{}, true
	}

	now := s.now()
	s.mu.Lock()
	allowed := s.allowRequestLocked(now)
	attempt := healthAttempt{generation: s.generation, probe: allowed && s.probeInFlight}
	events := s.takePendingLocked()
	s.mu.Unlock()
	s.notify(events)
	return attempt, allowed
}

func (s *HealthState) allowRequestLocked(now time.Time) bool {
	if s.phase == PhaseHealthy {
		return true
	}
	if s.probeInFlight || now.Before(s.nextProbeAt) {
		return false
	}
	s.probeInFlight = true
	s.transitionLocked(PhaseProbing)
	return true
}

func (s *HealthState) observeAttempt(attempt healthAttempt, err error) {
	if s == nil {
		return
	}
	now := s.now()
	s.mu.Lock()
	s.observeLocked(attempt, err, now)
	events := s.takePendingLocked()
	s.mu.Unlock()
	s.notify(events)
}

func (s *HealthState) observeLocked(attempt healthAttempt, err error, now time.Time) {
	if attempt.generation != s.generation {
		return
	}
	if attempt.probe {
		if !s.probeInFlight || s.phase != PhaseProbing {
			return
		}
		s.probeInFlight = false
	}
	if isPureCancellation(err) {
		if attempt.probe {
			s.transitionLocked(PhaseDegraded)
			s.nextProbeAt = now.Add(s.policy.ProbeInterval)
		}
		return
	}
	if err != nil {
		s.successes = 0
		s.failures++
		if attempt.probe || s.phase == PhaseHealthy && s.failures >= s.policy.FailureThreshold {
			s.transitionLocked(PhaseDegraded)
			s.nextProbeAt = now.Add(s.policy.Cooldown)
		}
		return
	}

	s.failures = 0
	if !attempt.probe {
		return
	}
	s.successes++
	if s.successes >= s.policy.SuccessThreshold {
		s.transitionLocked(PhaseHealthy)
		s.successes = 0
		return
	}
	s.transitionLocked(PhaseDegraded)
	s.nextProbeAt = now.Add(s.policy.ProbeInterval)
}

// 只要错误树还包含真实故障，就不能因 errors.Is 命中取消而丢弃故障样本。
func isPureCancellation(err error) bool {
	if err == nil {
		return false
	}
	switch wrapped := err.(type) {
	case interface{ Unwrap() []error }:
		found := false
		for _, child := range wrapped.Unwrap() {
			if child == nil {
				continue
			}
			if !isPureCancellation(child) {
				return false
			}
			found = true
		}
		return found
	case interface{ Unwrap() error }:
		if child := wrapped.Unwrap(); child != nil {
			return isPureCancellation(child)
		}
	}
	return errors.Is(err, context.Canceled)
}

func (s *HealthState) transitionLocked(next HealthPhase) {
	if s.phase == next {
		return
	}
	previous := s.phase
	s.phase = next
	s.generation++
	// 相位迁移是低基数事件，可以每次都发；它是故障排查的第一手线索。
	// 只入队不回调：通知在主锁之外发出（见 notify），Observer 阻塞
	// 不得挡住限流判定，Observer 重入公开方法不得死锁。
	s.pending = append(s.pending, phaseTransition{from: previous, to: next})
}

// notifyPending 在**不持有任何锁**的状态下按序发出本次取出的事件
// （reusable-packages spec）。
//
// 调用方在主锁内完成状态迁移，事件已按发生顺序入队；本函数由持锁方在
// 解锁之后立即调用，把自己那批事件发完。
//
// 为什么不用"单一发出者 + TryLock 交接"：拿不到锁就返回会留下一个真实的
// 滞留窗口——持有者恰在收尾时入队的事件，要等下一次状态活动才发出，
// 而一个降级事件可能因此延迟到故障结束。改为各自发送自己取出的那批：
// 每个事件都被立即发出一次，顺序在同一次迁移序列内仍与发生顺序一致。
//
// 通知期间不持任何锁，因此 Observer 重入公开方法不会死锁，慢 Observer
// 也只拖住它自己的调用者，不阻塞并发的限流判定。
func (s *HealthState) notify(events []phaseTransition) {
	for _, event := range events {
		s.observer.BackendPhaseChanged(event.from, event.to)
	}
}

// takePending 在主锁内取走待发事件（调用方必须已持锁）。
func (s *HealthState) takePendingLocked() []phaseTransition {
	if len(s.pending) == 0 {
		return nil
	}
	events := s.pending
	s.pending = nil
	return events
}
