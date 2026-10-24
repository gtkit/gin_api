package data

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sync"
	"time"
)

// AvailabilityState 描述资源当前的可用性，包括等待首次恢复的可选连接。
type AvailabilityState uint8

const (
	// AvailabilityUnspecified 是零值，非法。
	//
	// 健康状态必须显式确认：零值若被解释成"健康"，
	// 忘记初始化的字段就会伪装成一个通过的检查。
	AvailabilityUnspecified AvailabilityState = iota
	// AvailabilityHealthy 表示最近的观测结果为成功。
	AvailabilityHealthy
	// AvailabilityDegraded 表示连续失败已达阈值，且尚未进入探测窗口。
	AvailabilityDegraded
	// AvailabilityProbing 表示正在用探测请求确认是否已恢复。
	AvailabilityProbing
)

// String 返回稳定的小写状态名，用于日志与 readiness 输出。
//
// 取值是**固定枚举**而不是自由文本：它会成为日志字段与 metrics label，
// 自由文本会让基数不可控。
func (s AvailabilityState) String() string {
	switch s {
	case AvailabilityHealthy:
		return "healthy"
	case AvailabilityDegraded:
		return "degraded"
	case AvailabilityProbing:
		return "probing"
	case AvailabilityUnspecified:
		return "unspecified"
	default:
		return "unspecified"
	}
}

// AvailabilityPolicy 是状态迁移的阈值与节奏。
type AvailabilityPolicy struct {
	// FailureThreshold 连续失败达到该值时由 Healthy 转 Degraded。
	// 取 >1 是为了不让单次网络抖动就把整个依赖标记为不可用。
	FailureThreshold int
	// SuccessThreshold 探测期连续成功达到该值时转回 Healthy。
	SuccessThreshold int
	// Cooldown 是转入 Degraded 后到允许第一次探测之间的静默期。
	// 没有它，故障期间每次业务调用都会立刻触发一次探测，
	// 等于对一个已经出问题的依赖持续加压。
	Cooldown time.Duration
	// ProbeInterval 是两次探测之间的最小间隔。
	ProbeInterval time.Duration
}

// DefaultAvailabilityPolicy 返回默认阈值。
//
// 这些数字的依据是"不被单次抖动误判、也不让故障长期不可见"：
// 3 次连续失败才降级；降级后静默 5s；此后每 5s 最多探一次；
// 连续 2 次探测成功才认为恢复（单次成功可能只是抖动的间隙）。
func DefaultAvailabilityPolicy() AvailabilityPolicy {
	return AvailabilityPolicy{
		FailureThreshold: 3,
		SuccessThreshold: 2,
		Cooldown:         5 * time.Second,
		ProbeInterval:    5 * time.Second,
	}
}

// validate 校验策略自洽。
func (p AvailabilityPolicy) validate() error {
	var errs []error
	if p.FailureThreshold < 1 {
		errs = append(errs, fmt.Errorf("failure threshold must be >= 1, got %d", p.FailureThreshold))
	}
	if p.SuccessThreshold < 1 {
		errs = append(errs, fmt.Errorf("success threshold must be >= 1, got %d", p.SuccessThreshold))
	}
	if p.Cooldown < 0 {
		errs = append(errs, fmt.Errorf("cooldown must not be negative, got %s", p.Cooldown))
	}
	if p.ProbeInterval < 0 {
		errs = append(errs, fmt.Errorf("probe interval must not be negative, got %s", p.ProbeInterval))
	}
	return errors.Join(errs...)
}

// AvailabilityTracker 跟踪一个已建立连接的可用性，并在降级后按节奏探测恢复。
//
// 它**不替换客户端**：go-redis / GORM 的连接池自己会重连，
// 热替换 client 要处理"旧 client 上还有在飞请求"的所有权问题，
// 而收益只是省掉连接池自身的重连——不成比例。本类型只回答"现在能不能依赖它"。
//
// 并发安全。
type AvailabilityTracker struct {
	name   string
	policy AvailabilityPolicy
	probe  func(context.Context) error
	now    func() time.Time

	mu        sync.Mutex
	state     AvailabilityState
	failures  int
	successes int
	// nextProbeAt 是允许下一次探测的最早时刻。
	nextProbeAt time.Time
	// probeInFlight 是探测的**单一所有权**标记：同一时刻只允许一个探测在跑。
	// 没有它，降级期间并发的业务请求会各自发起一次探测，
	// 对已经出问题的依赖形成放大。
	probeInFlight bool
	// transitions 按目标状态累计迁移次数。只按固定枚举计数，
	// 绝不以错误文本为 key——那会让观测基数随故障种类无限增长。
	transitions map[AvailabilityState]uint64
}

// NewAvailabilityTracker 创建一个初始为 Healthy 的跟踪器。
//
// probe 是恢复探测的执行体，必须遵守传入的 ctx。probe 为 nil 时
// 跟踪器仍能记录状态，但永远不会自行探测（用于"启动即失败、
// 无客户端可探"的永久降级场景，见 NewPermanentlyDegradedTracker）。
func NewAvailabilityTracker(
	name string, policy AvailabilityPolicy, probe func(context.Context) error,
) (*AvailabilityTracker, error) {
	if name == "" {
		return nil, errors.New("availability tracker requires a name")
	}
	if err := policy.validate(); err != nil {
		return nil, fmt.Errorf("availability policy for %q: %w", name, err)
	}

	return &AvailabilityTracker{
		name:        name,
		policy:      policy,
		probe:       probe,
		now:         time.Now,
		state:       AvailabilityHealthy,
		transitions: map[AvailabilityState]uint64{},
	}, nil
}

// NewPermanentlyDegradedTracker 返回一个恒为 Degraded 且永不探测的跟踪器。
//
// 用于未配置或确实没有恢复能力的缺失句柄。
func NewPermanentlyDegradedTracker(name string) *AvailabilityTracker {
	return &AvailabilityTracker{
		name:        name,
		policy:      DefaultAvailabilityPolicy(),
		probe:       nil,
		now:         time.Now,
		state:       AvailabilityDegraded,
		transitions: map[AvailabilityState]uint64{AvailabilityDegraded: 1},
	}
}

func newRecoverableDegradedTracker(name string, policy AvailabilityPolicy, probe func(context.Context) error) (*AvailabilityTracker, error) {
	tracker, err := NewAvailabilityTracker(name, policy, probe)
	if err != nil {
		return nil, err
	}
	tracker.degradeLocked()
	return tracker, nil
}

// Name 返回资源名（低基数，可直接作日志字段/metrics label）。
func (t *AvailabilityTracker) Name() string { return t.name }

// State 返回当前状态。
func (t *AvailabilityTracker) State() AvailabilityState {
	if t == nil {
		return AvailabilityUnspecified
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state
}

// Transitions 返回按目标状态累计的迁移次数快照（低基数观测）。
func (t *AvailabilityTracker) Transitions() map[AvailabilityState]uint64 {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()

	out := make(map[AvailabilityState]uint64, len(t.transitions))
	maps.Copy(out, t.transitions)
	return out
}

// Observe 上报一次真实调用的结果，并按策略推进状态。
//
// 传 nil 表示成功。永久降级的跟踪器（probe 为 nil）忽略上报——
// 它的语义是"本进程在重启前都没有这个能力"。
func (t *AvailabilityTracker) Observe(err error) {
	if t == nil || t.probe == nil {
		return
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	if err == nil {
		t.onSuccessLocked()
		return
	}
	t.onFailureLocked()
}

func (t *AvailabilityTracker) onSuccessLocked() {
	t.failures = 0

	switch t.state {
	case AvailabilityHealthy:
		// 已经健康，无需迁移。
	case AvailabilityProbing:
		t.successes++
		if t.successes >= t.policy.SuccessThreshold {
			t.transitionLocked(AvailabilityHealthy)
		}
	case AvailabilityDegraded:
		// 降级期间出现一次成功的业务调用：按探测成功计一次，
		// 但仍需达到 SuccessThreshold 才回到 Healthy——
		// 单次成功可能只是故障间隙。
		t.transitionLocked(AvailabilityProbing)
		t.successes = 1
		if t.successes >= t.policy.SuccessThreshold {
			t.transitionLocked(AvailabilityHealthy)
		}
	case AvailabilityUnspecified:
		t.transitionLocked(AvailabilityHealthy)
	}
}

func (t *AvailabilityTracker) onFailureLocked() {
	t.successes = 0
	t.failures++

	switch t.state {
	case AvailabilityHealthy, AvailabilityUnspecified:
		if t.failures >= t.policy.FailureThreshold {
			t.degradeLocked()
		}
	case AvailabilityProbing:
		// 探测期的失败立刻退回 Degraded 并重置冷却：
		// 恢复判定必须比降级判定保守。
		t.degradeLocked()
	case AvailabilityDegraded:
		// 已降级，只重置冷却窗口。
		t.nextProbeAt = t.now().Add(t.policy.Cooldown)
	}
}

func (t *AvailabilityTracker) degradeLocked() {
	t.transitionLocked(AvailabilityDegraded)
	t.successes = 0
	t.nextProbeAt = t.now().Add(t.policy.Cooldown)
}

// transitionLocked 记录一次状态迁移。调用方必须持锁。
func (t *AvailabilityTracker) transitionLocked(next AvailabilityState) {
	if t.state == next {
		return
	}
	t.state = next
	t.transitions[next]++
}

// MaybeProbe 在允许的时机执行**至多一次**恢复探测。
//
// 返回 (probed, err)：probed 为 false 表示本次没有探测（未降级、冷却未到、
// 或已有探测在飞），此时 err 恒为 nil。
//
// 探测所有权由 probeInFlight 保证：并发调用中只有一个会真正发探测请求，
// 其余立即返回。否则降级期间每个请求都会各自探一次，
// 对一个已经出问题的依赖形成放大。
func (t *AvailabilityTracker) MaybeProbe(ctx context.Context) (bool, error) {
	if t == nil || t.probe == nil {
		return false, nil
	}

	t.mu.Lock()
	if t.state != AvailabilityDegraded || t.probeInFlight || t.now().Before(t.nextProbeAt) {
		t.mu.Unlock()
		return false, nil
	}
	t.probeInFlight = true
	t.transitionLocked(AvailabilityProbing)
	t.mu.Unlock()

	err := t.probe(ctx)

	t.mu.Lock()
	defer t.mu.Unlock()
	t.probeInFlight = false
	// 下一次探测的最早时刻在**本次探测结束后**开始计算，
	// 而不是发起时——否则一次耗时超过 ProbeInterval 的探测结束时，
	// 下一次探测已经可以立刻发起，节奏形同虚设。
	t.nextProbeAt = t.now().Add(t.policy.ProbeInterval)

	if err != nil {
		t.successes = 0
		t.failures++
		t.transitionLocked(AvailabilityDegraded)
		t.nextProbeAt = t.now().Add(t.policy.Cooldown)
		return true, err
	}

	t.failures = 0
	t.successes++
	if t.successes >= t.policy.SuccessThreshold {
		t.transitionLocked(AvailabilityHealthy)
	}
	return true, nil
}
