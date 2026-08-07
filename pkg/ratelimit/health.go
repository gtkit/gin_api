package ratelimit

import (
	"context"
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
	s.mu.Lock()
	s.transitionLocked(PhaseDegraded)
	s.nextProbeAt = s.now()
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

// AllowRequest 报告本次请求是否应当真的去打后端。
//
// Degraded 期间只放行**一个**探测请求，其余一律走降级路径。
// 没有这条，故障期每个请求都会去打一次已经出问题的后端，
// 既拖慢自己（每个请求白等一次超时），又给后端持续加压。
func (s *HealthState) AllowRequest(_ context.Context) bool {
	if s == nil {
		return true
	}

	s.mu.Lock()
	allowed := s.allowRequestLocked()
	events := s.takePendingLocked()
	s.mu.Unlock()

	s.notify(events)
	return allowed
}

func (s *HealthState) allowRequestLocked() bool {
	switch s.phase {
	case PhaseHealthy:
		return true
	case PhaseProbing:
		// **探测的单一所有权由 probeInFlight 把关，不是相位。**
		//
		// 按相位放行是一个很容易写出的错误：第一个请求把相位从 Degraded
		// 切到 Probing 之后，后续每个请求看到的都是 Probing，于是全部跟着
		// 打后端——探测风暴照旧发生，而状态机看起来一切正常。
		return !s.probeInFlight
	case PhaseDegraded:
		if s.probeInFlight || s.now().Before(s.nextProbeAt) {
			return false
		}
		// CAS 式获取探测所有权：只有一个请求能走到后端。
		s.probeInFlight = true
		s.transitionLocked(PhaseProbing)
		return true
	default:
		return true
	}
}

// Observe 上报一次后端调用的结果。
//
// 只有**依赖失败**（网络错误、超时、服务端错误）才计失败。
// 业务上的"被限流"是正常结果，计进去会让一个正在生效的限流策略
// 反过来把自己判成故障。
func (s *HealthState) Observe(err error) {
	if s == nil {
		return
	}

	s.mu.Lock()
	wasProbing := s.phase == PhaseProbing
	if wasProbing {
		s.probeInFlight = false
	}
	if err != nil {
		s.onFailureLocked(wasProbing)
	} else {
		s.onSuccessLocked(wasProbing)
	}
	events := s.takePendingLocked()
	s.mu.Unlock()

	s.notify(events)
}

func (s *HealthState) onSuccessLocked(wasProbing bool) {
	s.failures = 0

	switch s.phase {
	case PhaseHealthy:
		return
	case PhaseProbing:
		s.successes++
		// 恢复比降级保守：需要连续成功且间隔足够。
		if s.successes >= s.policy.SuccessThreshold && !s.now().Before(s.nextProbeAt) {
			s.transitionLocked(PhaseHealthy)
			s.successes = 0
			return
		}
		// 未达标：退回 Degraded 等待下一次探测窗口。
		s.transitionLocked(PhaseDegraded)
		s.nextProbeAt = s.now().Add(s.policy.ProbeInterval)
	case PhaseDegraded:
		if wasProbing {
			s.nextProbeAt = s.now().Add(s.policy.ProbeInterval)
		}
	}
}

func (s *HealthState) onFailureLocked(wasProbing bool) {
	s.successes = 0
	s.failures++

	switch s.phase {
	case PhaseHealthy:
		if s.failures >= s.policy.FailureThreshold {
			s.degradeLocked()
		}
	case PhaseProbing:
		// 探测失败立刻退回并重新开始冷却。
		s.degradeLocked()
	case PhaseDegraded:
		if wasProbing {
			s.nextProbeAt = s.now().Add(s.policy.Cooldown)
		}
	}
}

func (s *HealthState) degradeLocked() {
	s.transitionLocked(PhaseDegraded)
	s.successes = 0
	s.nextProbeAt = s.now().Add(s.policy.Cooldown)
}

func (s *HealthState) transitionLocked(next HealthPhase) {
	if s.phase == next {
		return
	}
	previous := s.phase
	s.phase = next
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
