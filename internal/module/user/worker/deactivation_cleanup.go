// Package worker 承载 user 模块的后台任务。
package worker

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// DeactivationPurger 是清理注销档案所需的最小仓储能力。
//
// 定义在消费侧而不是复用仓储的完整接口：这个 worker 只需要"删掉过期的"，
// 而把整个仓储接口拖进来会让它看起来还能读写别的东西。
type DeactivationPurger interface {
	DeleteOlderThan(ctx context.Context, cutoff time.Time, limit int) (int64, error)
}

// Auditor 记录清理结果。日志栈由装配层注入。
//
// 成功与失败**分方法**而不是共用一个：清理失败意味着一份该被清除的个人信息
// 仍在留存，那是需要告警的事件；把它与"删了 N 行"混在同一日志级别里，
// 告警规则就只能靠猜消息文本。
type Auditor interface {
	// Auditf 记录正常运行结果（Info 级）。
	Auditf(format string, args ...any)
	// Failf 记录清理失败（Warn 级及以上，供日志告警规则匹配）。
	Failf(format string, args ...any)
}

// DeactivationCleanup 按保留期清理注销档案。
//
// **纯业务逻辑，不实现装配层的 Worker 契约**：那个契约的类型属于 bootstrap，
// 模块层实现它会让依赖方向倒转——一个业务模块从此依赖进程编排层。
// Name / Kind / Required / Lifecycle 等由 runtime/module/user 的 adapter 补齐。
//
// **它处理的是个人信息**：注销档案里有原手机号、邮箱、昵称与来源 IP。
// 保留期由使用方按所在司法辖区确定——各行业与各地对最长保留期、删除权响应
// 时限的要求并不一致，脚手架替使用者选一个数字只会让它在某些辖区是错的。
//
// 未配置保留期时这个 worker 根本不会被装配（见 runtime 层），因此这里
// 无需再判一次"是否启用"。
type DeactivationCleanup struct {
	repo      DeactivationPurger
	auditor   Auditor
	retention time.Duration
	interval  time.Duration
	batch     int
	// now 可注入，测试用它确定性地驱动截止时刻。
	now func() time.Time

	// health 保存运行状态，供日志与指标读取。
	//
	// **必须加锁**：写发生在 Run 的 goroutine 上，而读发生在指标采集线程上
	// （异步回调在采集周期被调用）。此前只有日志读它、且与写同一 goroutine，
	// 不加锁是安全的；接上指标之后那个前提就不成立了。
	health healthState
}

// healthState 是并发安全的运行状态。
type healthState struct {
	mu                  sync.Mutex
	consecutiveFailures int
	lastSuccess         time.Time
}

func (h *healthState) recordFailure() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.consecutiveFailures++
	return h.consecutiveFailures
}

func (h *healthState) recordSuccess(at time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.consecutiveFailures = 0
	h.lastSuccess = at
}

// snapshot 返回当前状态的副本，供指标采集读取。
func (h *healthState) snapshot() (failures int, lastSuccess time.Time) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.consecutiveFailures, h.lastSuccess
}

// Health 返回当前运行状态，供指标以异步回调读取。
//
// 导出它而不是让本包直接依赖指标包：worker 是业务逻辑，
// 让它认识遥测栈会把一个纯逻辑单元绑到具体的可观测性实现上。
func (w *DeactivationCleanup) Health() (consecutiveFailures int, lastSuccess time.Time) {
	return w.health.snapshot()
}

// NewDeactivationCleanup 构造清理任务。
//
// retention <= 0 视为装配错误而不是"禁用"：禁用应当表现为**根本不注册这个
// worker**，而不是注册一个什么都不做的。后者会在关停顺序、就绪信息里留下
// 一个名字，让人以为清理正在进行。
func NewDeactivationCleanup(
	repo DeactivationPurger, auditor Auditor,
	retention, interval time.Duration, batch int,
) (*DeactivationCleanup, error) {
	if repo == nil {
		return nil, fmt.Errorf("deactivation cleanup requires a repository")
	}
	if retention <= 0 {
		return nil, fmt.Errorf(
			"deactivation cleanup requires a positive retention window, got %s: "+
				"to disable cleanup, do not register the worker at all", retention)
	}
	if interval <= 0 {
		return nil, fmt.Errorf("deactivation cleanup requires a positive interval, got %s", interval)
	}
	if batch <= 0 {
		return nil, fmt.Errorf("deactivation cleanup requires a positive batch size, got %d", batch)
	}

	return &DeactivationCleanup{
		repo: repo, auditor: auditor,
		retention: retention, interval: interval, batch: batch,
		now: time.Now,
	}, nil
}

// Run 按间隔清理，直到 ctx 取消。
//
// **启动时立即跑一轮**再进入定时：只等第一个 tick 的话，一个频繁重启的部署
// 可能永远等不到清理发生。
func (w *DeactivationCleanup) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.interval)
	defer ticker.Stop()

	w.purge(ctx)

	for {
		select {
		case <-ctx.Done():
			// 关停不是错误：ctx 取消是这个 worker 唯一正常的结束方式。
			return nil
		case <-ticker.C:
			w.purge(ctx)
		}
	}
}

// purge 执行一轮清理，分批直到删完或 ctx 取消。
//
// 单轮内循环而不是每个间隔只删一批：积压很大时，每天只删 500 行永远追不上。
// 但每批之间检查 ctx——关停时必须能立刻退出，而不是等这一轮跑完。
func (w *DeactivationCleanup) purge(ctx context.Context) {
	cutoff := w.now().Add(-w.retention)
	var total int64

	for {
		if ctx.Err() != nil {
			return
		}

		deleted, err := w.repo.DeleteOlderThan(ctx, cutoff, w.batch)
		if err != nil {
			// 不返回错误：这是 optional worker，一次失败的正确处置是下一轮重试。
			// 但必须以**告警级别**留痕，并带上连续失败计数与最后成功时间——
			// "失败了一次"和"已经连续失败三天"是完全不同的运维事件，
			// 而持续失败意味着该被清除的个人信息在超期留存。
			failures := w.health.recordFailure()
			w.fail("deactivation cleanup failed (consecutive failures: %d, last success: %s): %v",
				failures, w.lastSuccessLabel(), err)
			return
		}
		total += deleted
		if deleted < int64(w.batch) {
			break
		}
	}

	w.health.recordSuccess(w.now())

	if total > 0 {
		w.audit("purged %d deactivation records created before %s",
			total, cutoff.Format(time.RFC3339))
	}
}

// lastSuccessLabel 给失败日志一个可读的"上次成功"时刻。
//
// 从未成功过时明说 never：进程刚启动就开始失败，与"跑了三天后开始失败"
// 是两种不同的故障画像。
func (w *DeactivationCleanup) lastSuccessLabel() string {
	_, lastSuccess := w.health.snapshot()
	if lastSuccess.IsZero() {
		return "never"
	}
	return lastSuccess.Format(time.RFC3339)
}

func (w *DeactivationCleanup) fail(format string, args ...any) {
	if w.auditor != nil {
		w.auditor.Failf(format, args...)
	}
}

func (w *DeactivationCleanup) audit(format string, args ...any) {
	if w.auditor != nil {
		w.auditor.Auditf(format, args...)
	}
}
