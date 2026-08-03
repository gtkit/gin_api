package bootstrap

import (
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// ProcessPhase 是进程对外可观测的生命周期相位。
//
// 它是 readiness 判定的输入之一：draining 期间实例必须让编排器摘流，
// 但**不能**让 liveness 判定为死——否则编排器会在优雅关停中途 kill 进程，
// 在飞请求被直接截断。
type ProcessPhase uint8

const (
	// PhaseStarting 表示尚未开始对外服务。
	PhaseStarting ProcessPhase = iota
	// PhaseServing 表示正常对外服务。
	PhaseServing
	// PhaseDraining 表示已进入优雅关停：/ready 返回 503，/health 仍返回 200。
	PhaseDraining
)

// String 返回稳定的小写相位名。
func (p ProcessPhase) String() string {
	switch p {
	case PhaseStarting:
		return "starting"
	case PhaseServing:
		return "serving"
	case PhaseDraining:
		return "draining"
	default:
		return "starting"
	}
}

// ErrForcedShutdown 表示进程走了强制关停路径（第二个信号）。
var ErrForcedShutdown = errors.New("shutdown forced by a second signal")

// ShutdownCoordinator 是**唯一**的关停入口。
//
// 关停触发源不止一个：OS 信号、required worker 运行期异常、主 HTTP serve 异常。
// 它们必须全部经 RequestShutdown 汇聚，否则各自直接执行关停动作时，
// 并发到达就会重复 close channel、重复关闭连接、重复执行清理——
// 而这些重复中的任何一个都会把一次正常关停变成一次报错的关停。
//
// optional 能力的异常（如 pprof 监听退出）**不**经此入口：它们只更新能力状态
// 与错误聚合，不触发全进程关停。
type ShutdownCoordinator struct {
	once      sync.Once
	requested chan struct{}

	mu     sync.Mutex
	causes []error

	// forced 由第二个信号置位：取消各阶段等待、直接 Close 而不是 Shutdown、
	// 并以非零退出，避免进程在故障关停中无限滞留。
	forced atomic.Bool
	// forceCh 关闭后，各等待点立即放弃剩余预算。
	forceOnce sync.Once
	forceCh   chan struct{}

	phase atomic.Uint32
}

// NewShutdownCoordinator 创建协调器，初始相位为 starting。
func NewShutdownCoordinator() *ShutdownCoordinator {
	c := &ShutdownCoordinator{
		requested: make(chan struct{}),
		forceCh:   make(chan struct{}),
	}
	c.phase.Store(uint32(PhaseStarting))
	return c
}

// Phase 返回当前相位。
//
// 相位以 uint32 原子存储（atomic 没有 uint8 变体），读出时按已知取值收窄。
// 不做裸转换：越界值一旦出现，静默截断会得到一个看起来合法的相位。
func (c *ShutdownCoordinator) Phase() ProcessPhase {
	if c == nil {
		return PhaseStarting
	}
	switch c.phase.Load() {
	case uint32(PhaseServing):
		return PhaseServing
	case uint32(PhaseDraining):
		return PhaseDraining
	default:
		return PhaseStarting
	}
}

// MarkServing 把相位推进到 serving。
//
// 已进入 draining 后不再回退：关停是单向的，一个"重新变回 serving"的实例
// 会让编排器在摘流后又把流量导回来。
func (c *ShutdownCoordinator) MarkServing() {
	if c == nil {
		return
	}
	c.phase.CompareAndSwap(uint32(PhaseStarting), uint32(PhaseServing))
}

// RequestShutdown 请求一次优雅关停。
//
// 首次调用触发关停并记录 cause；后续调用只把 cause 聚合进最终错误，
// 不重复触发任何阶段。cause 为 nil 表示正常的信号关停。
func (c *ShutdownCoordinator) RequestShutdown(cause error) {
	if c == nil {
		return
	}

	if cause != nil {
		c.mu.Lock()
		c.causes = append(c.causes, cause)
		c.mu.Unlock()
	}

	c.once.Do(func() {
		c.phase.Store(uint32(PhaseDraining))
		close(c.requested)
	})
}

// Force 进入强制关停路径（第二个 OS 信号）。
//
// 语义：取消各阶段的剩余等待，调用方据此改用 Close 而不是 Shutdown，
// 停止继续 drain，并以非零退出。没有它，一个挂死的 handler 会让进程
// 在"优雅关停"里无限滞留，而运维已经在等它退出。
func (c *ShutdownCoordinator) Force() {
	if c == nil {
		return
	}
	c.forced.Store(true)
	c.forceOnce.Do(func() { close(c.forceCh) })
	// 强制路径也算一次关停请求：先收到第二个信号的情形同样要能启动关停。
	c.RequestShutdown(ErrForcedShutdown)
}

// Forced 报告是否已进入强制关停路径。
func (c *ShutdownCoordinator) Forced() bool {
	if c == nil {
		return false
	}
	return c.forced.Load()
}

// Requested 返回关停请求信号通道。
func (c *ShutdownCoordinator) Requested() <-chan struct{} {
	if c == nil {
		return nil
	}
	return c.requested
}

// ForcedCh 返回强制关停信号通道，供各等待点提前放弃剩余预算。
func (c *ShutdownCoordinator) ForcedCh() <-chan struct{} {
	if c == nil {
		return nil
	}
	return c.forceCh
}

// Cause 返回聚合后的关停原因；正常信号关停返回 nil。
func (c *ShutdownCoordinator) Cause() error {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return errors.Join(c.causes...)
}

// WaitOrForce 等待 d，或在强制关停时提前返回。
//
// 用于 LB 传播等待这类"正常路径要等、强制路径不该等"的场景。
func (c *ShutdownCoordinator) WaitOrForce(d time.Duration) {
	if d <= 0 {
		return
	}
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-timer.C:
	case <-c.ForcedCh():
	}
}
