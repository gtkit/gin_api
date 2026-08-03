package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	logger "github.com/gtkit/logger/v2"
)

const (
	// defaultWorkerPrepareTimeout 是单个 Prepare 的上限。
	defaultWorkerPrepareTimeout = 10 * time.Second
	// defaultWorkerStartupBudget 覆盖**全部 Prepare + 全部 required OneShot 的 Run**。
	//
	// 覆盖范围必须包含 required OneShot 的执行：它们是 HTTP 启动的前置屏障，
	// 只给 Prepare 设预算的话，一个卡住的启动期数据修复任务照样能让启动无限期挂起。
	defaultWorkerStartupBudget = 60 * time.Second
	// defaultWorkerCleanupTimeout 是单个 Cleanup 的上限。
	defaultWorkerCleanupTimeout = 10 * time.Second
)

// WorkerBudget 是 worker 启动与清理的时间预算。
type WorkerBudget struct {
	// Prepare 是单个 Prepare 的上限。
	Prepare time.Duration
	// Startup 覆盖全部 Prepare 与全部 required OneShot 的 Run。
	Startup time.Duration
	// Cleanup 是单个 Cleanup 的上限。
	Cleanup time.Duration
}

// DefaultWorkerBudget 返回默认预算。
func DefaultWorkerBudget() WorkerBudget {
	return WorkerBudget{
		Prepare: defaultWorkerPrepareTimeout,
		Startup: defaultWorkerStartupBudget,
		Cleanup: defaultWorkerCleanupTimeout,
	}
}

func (b WorkerBudget) normalize() WorkerBudget {
	if b.Prepare <= 0 {
		b.Prepare = defaultWorkerPrepareTimeout
	}
	if b.Startup <= 0 {
		b.Startup = defaultWorkerStartupBudget
	}
	if b.Cleanup <= 0 {
		b.Cleanup = defaultWorkerCleanupTimeout
	}
	return b
}

// preparedWorker 记录一个 Prepare 成功、尚未清理的 worker。
type preparedWorker struct {
	worker Worker
	// cleaned 保证管理器对 Cleanup 的调用**恰好一次**。
	// Cleanup 自身要求幂等只是防御，管理器的调用计数仍必须精确——
	// 否则"恰好一次"这条契约就没有任何一方在保证。
	cleaned bool
}

// WorkerManager 编排 worker 的准备、运行与清理。
//
// 启动顺序写死（见 Start 的注释），关停由 Stop 分相执行。
type WorkerManager struct {
	workers []Worker
	budget  WorkerBudget

	mu       sync.Mutex
	prepared []*preparedWorker // 按 Prepare 成功的顺序追加，清理时逆序

	// errCh 汇聚运行期异常（含 ErrUnexpectedExit 与 WorkerPanicError）。
	// 只有 required worker 的异常写入此通道；optional 的异常只记录并标降级。
	errCh chan error

	// running 记录仍在跑的长期 worker，Stop 按相位取消。
	runMu    sync.Mutex
	running  map[WorkerKind][]*runningWorker
	degraded map[string]error
}

// runningWorker 持有一个已启动 worker 的取消入口与退出信号。
type runningWorker struct {
	entry  *preparedWorker
	cancel context.CancelFunc
	done   chan struct{}
}

// NewWorkerManager 校验全部 worker 的声明并返回管理器。
//
// 声明非法在**这里**就失败，而不是等到启动：非法组合是编码错误，
// 启动时零成本可判，留到运行期只能靠观察行为反推。
func NewWorkerManager(budget WorkerBudget, workers ...Worker) (*WorkerManager, error) {
	var errs []error
	seen := make(map[string]struct{}, len(workers))

	for _, w := range workers {
		if err := validateWorkerContract(w); err != nil {
			errs = append(errs, err)
			continue
		}
		if _, dup := seen[w.Name()]; dup {
			// 重名让日志与错误无法指认到具体 worker，也让 degraded 表互相覆盖。
			errs = append(errs, fmt.Errorf("%w: duplicate worker name %q", ErrWorkerContractViolation, w.Name()))
			continue
		}
		seen[w.Name()] = struct{}{}
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	return &WorkerManager{
		workers:  workers,
		budget:   budget.normalize(),
		errCh:    make(chan error, max(len(workers), 1)),
		running:  make(map[WorkerKind][]*runningWorker),
		degraded: make(map[string]error),
	}, nil
}

// Errors 返回 required worker 运行期异常的通道。
func (m *WorkerManager) Errors() <-chan error {
	if m == nil {
		return nil
	}
	return m.errCh
}

// Degraded 返回已降级的 worker 及其原因快照。
func (m *WorkerManager) Degraded() map[string]error {
	if m == nil {
		return nil
	}
	m.runMu.Lock()
	defer m.runMu.Unlock()

	out := make(map[string]error, len(m.degraded))
	for k, v := range m.degraded {
		out[k] = v
	}
	return out
}

// Start 按写死的顺序完成 worker 启动，返回后 HTTP 才可以开始 Serve。
//
// 顺序：
//
//	① 对所有 worker 顺序执行 Prepare（单个有上限，整体受启动总预算约束）；
//	② 在**同一总预算内**同步执行全部 required OneShot 的 Run，每个返回后立即 Cleanup；
//	③ required OneShot 全部成功后，启动 LongRunning 与 optional OneShot。
//
// 任一步失败或预算耗尽：对仍处于 prepared 且未清理的 worker 按**逆序** Cleanup，
// 保证每个 Prepare 成功的 worker 最终恰好清理一次，然后返回错误（= 启动失败）。
//
// required OneShot 必须执行完成才启动 HTTP：它们代表"服务对外之前必须先做完的事"
// （如启动期数据修复），放到 HTTP 之后就失去了全部意义。
func (m *WorkerManager) Start(ctx context.Context) error {
	if m == nil {
		return nil
	}

	startupCtx, cancel := context.WithTimeout(ctx, m.budget.Startup)
	defer cancel()

	if err := m.prepareAll(startupCtx); err != nil {
		return errors.Join(err, m.cleanupPrepared(ctx))
	}

	if err := m.runRequiredOneShots(startupCtx); err != nil {
		return errors.Join(err, m.cleanupPrepared(ctx))
	}

	m.startBackgroundWorkers(ctx)
	return nil
}

// prepareAll 顺序执行全部 Prepare。
//
// 顺序而不是并发：Prepare 允许抢锁与建连，并发执行会让"失败时该按什么顺序回滚"
// 失去确定答案，而回滚顺序恰恰是资源释放正确性的前提。
func (m *WorkerManager) prepareAll(ctx context.Context) error {
	for _, w := range m.workers {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("worker startup budget exhausted before preparing %q: %w", w.Name(), err)
		}

		prepareCtx, cancel := withBoundedTimeout(ctx, m.budget.Prepare)
		err := w.Prepare(prepareCtx)
		cancel()

		if err != nil {
			if w.Required() {
				return fmt.Errorf("prepare required worker %q: %w", w.Name(), err)
			}
			// optional 的 Prepare 失败不阻断启动：记录、标降级、该 worker 不启动。
			// 它没有进入 prepared 集合，因此也不会被 Cleanup——Prepare 失败的实现
			// 有责任自己清理其内部半成品。
			m.markDegraded(w.Name(), err)
			logger.Warnf("optional worker %q failed to prepare and will not start: %v", w.Name(), err)
			continue
		}

		m.mu.Lock()
		m.prepared = append(m.prepared, &preparedWorker{worker: w})
		m.mu.Unlock()
	}
	return nil
}

// runRequiredOneShots 在启动总预算内同步执行全部 required OneShot，并逐个清理。
//
// 每个 Run 返回后立即 Cleanup 并标记已清理：这样后续步骤失败时的逆序回滚
// 不会对它们重复调用 Cleanup。
func (m *WorkerManager) runRequiredOneShots(ctx context.Context) error {
	for _, entry := range m.snapshotPrepared() {
		w := entry.worker
		if w.Lifecycle() != LifecycleOneShot || !w.Required() {
			continue
		}

		if err := ctx.Err(); err != nil {
			return fmt.Errorf("worker startup budget exhausted before running %q: %w", w.Name(), err)
		}

		if err := m.runOnce(ctx, w); err != nil {
			return fmt.Errorf("required one-shot worker %q: %w", w.Name(), err)
		}

		// required OneShot 的 Cleanup 失败视为启动失败：它的资源（锁、连接）
		// 若没能释放，后续的服务运行就建立在一个不确定的前提上。
		if err := m.cleanupEntry(ctx, entry); err != nil {
			return fmt.Errorf("cleanup required one-shot worker %q: %w", w.Name(), err)
		}
	}
	return nil
}

// runOnce 同步执行一次 Run，并把 panic 转成错误。
func (m *WorkerManager) runOnce(ctx context.Context, w Worker) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = recoverWorkerPanic(w.Name(), r)
		}
	}()
	return w.Run(ctx)
}

// startBackgroundWorkers 启动 LongRunning 与 optional OneShot。
//
// workerCtx 与启动 ctx 解耦：启动预算到期不该顺带取消已经跑起来的长期 worker。
func (m *WorkerManager) startBackgroundWorkers(parent context.Context) {
	base := context.WithoutCancel(parent)

	for _, entry := range m.snapshotPrepared() {
		w := entry.worker
		if w.Lifecycle() == LifecycleOneShot && w.Required() {
			continue // 已在启动屏障内执行并清理
		}

		// 豁免理由：cancel 不在本函数内调用是**有意的**——它被存进 runningWorker，
		// 由 StopKind 在关停对应相位时调用，这正是分相关停的实现方式。
		// 三个类别都由 gracefulShutdown / StopAll 覆盖，不存在无人调用的分支。
		runCtx, cancel := context.WithCancel(base) //nolint:gosec // cancel 由 StopKind 持有并调用
		rw := &runningWorker{entry: entry, cancel: cancel, done: make(chan struct{})}

		m.runMu.Lock()
		m.running[w.Kind()] = append(m.running[w.Kind()], rw)
		m.runMu.Unlock()

		go m.supervise(runCtx, rw)
	}
}

// supervise 运行一个 worker 并按契约解释它的退出。
func (m *WorkerManager) supervise(ctx context.Context, rw *runningWorker) {
	defer close(rw.done)

	w := rw.entry.worker
	logger.Infof("starting worker %s (kind=%s lifecycle=%s required=%v)",
		w.Name(), w.Kind(), w.Lifecycle(), w.Required())

	err := m.runOnce(ctx, w)

	// 长期 worker 在 ctx 取消之前返回——**包括返回 nil**——一律是异常退出。
	// 不做这个判定，一个静默返回 nil 的长期 worker 会让进程继续对外服务，
	// 而它负责的功能已经没了。
	if w.Lifecycle() == LifecycleLongRunning && ctx.Err() == nil {
		if err == nil {
			err = ErrUnexpectedExit
		} else {
			err = fmt.Errorf("%w: %w", ErrUnexpectedExit, err)
		}
	}

	// ctx 已取消时的 ctx.Canceled 是正常关停，不算异常。
	if err != nil && errors.Is(err, context.Canceled) && ctx.Err() != nil {
		err = nil
	}

	if err != nil {
		m.reportRunFailure(w, err)
	}

	// 正常 Run 结束后同样要 Cleanup：只在"不会进入 Run"时清理，
	// 会让 Prepare 建立的连接与持有的锁在正常退出路径上无人释放。
	if cerr := m.cleanupEntry(ctx, rw.entry); cerr != nil {
		m.reportRunFailure(w, fmt.Errorf("cleanup worker %q: %w", w.Name(), cerr))
	}
}

// reportRunFailure 按 Required 语义分派运行期异常。
//
//	required → 写入错误通道，由关停协调器触发全进程优雅关停并非零退出；
//	optional → 标降级并停止该 worker，进程继续服务。
//
// 明确不做 optional 的有限退避重启：那需要退避策略、最大次数、抖动、
// 状态上报一整套机制，属无现实消费者的功能。需要重试的 worker
// 自己在 Run 内实现内部重试。
func (m *WorkerManager) reportRunFailure(w Worker, err error) {
	var panicErr *WorkerPanicError
	if errors.As(err, &panicErr) {
		logger.Errorf("worker %s panicked: %v\n%s", panicErr.Worker, panicErr.Value, panicErr.Stack)
	}

	if !w.Required() {
		m.markDegraded(w.Name(), err)
		logger.Warnf("optional worker %s stopped: %v", w.Name(), err)
		return
	}

	wrapped := fmt.Errorf("worker %s: %w", w.Name(), err)
	select {
	case m.errCh <- wrapped:
	default:
		// 通道容量按 worker 数分配，写满说明已有足够多的异常在等待处理；
		// 丢弃后仍记录，避免在关停路径上阻塞。
		logger.Errorf("worker error channel is full, dropping: %v", wrapped)
	}
}

func (m *WorkerManager) markDegraded(name string, err error) {
	m.runMu.Lock()
	defer m.runMu.Unlock()
	m.degraded[name] = err
}

// StopKind 取消指定类别的全部 worker 并在预算内等待它们退出。
//
// 分类别停止是 producer/consumer 两相关停的执行入口：
// producer 必须在 HTTP 关停**之前**停（否则它会在关停期间继续生产新任务，
// 而随后 consumer 被取消，新任务无人消费或半途中断），
// consumer 必须在 HTTP 关停**之后**才 drain（让在飞请求写入的任务仍能被消费完）。
func (m *WorkerManager) StopKind(ctx context.Context, kind WorkerKind) error {
	if m == nil {
		return nil
	}

	m.runMu.Lock()
	targets := m.running[kind]
	m.running[kind] = nil
	m.runMu.Unlock()

	for _, rw := range targets {
		rw.cancel()
	}

	var errs []error
	for _, rw := range targets {
		select {
		case <-rw.done:
		case <-ctx.Done():
			errs = append(errs, fmt.Errorf(
				"worker %q did not exit within the shutdown budget: %w", rw.entry.worker.Name(), ctx.Err()))
		}
	}
	return errors.Join(errs...)
}

// StopAll 依次停止 bootstrap（optional OneShot）与两类长期 worker。
//
// 只用于"没有分相需求"的场景（如启动失败回滚）。正常关停走 StopKind 的分相顺序。
func (m *WorkerManager) StopAll(ctx context.Context) error {
	if m == nil {
		return nil
	}
	return errors.Join(
		m.StopKind(ctx, KindProducer),
		m.StopKind(ctx, KindConsumer),
		m.StopKind(ctx, KindBootstrap),
	)
}

// CleanupRemaining 清理仍未清理的 prepared worker（逆序）。
//
// 正常路径上各 worker 的 Cleanup 已由 supervise 在 Run 返回后调用，
// 本方法只兜住那些从未进入 Run 的条目，以及关停超时导致 supervise 未完成的情况。
func (m *WorkerManager) CleanupRemaining(ctx context.Context) error {
	if m == nil {
		return nil
	}
	return m.cleanupPrepared(ctx)
}

// cleanupPrepared 按 Prepare 成功的**逆序**清理尚未清理的 worker。
//
// 逆序：后 Prepare 的可能依赖先 Prepare 的（如后者持有前者建立的连接）。
// 单个失败不阻断其余，错误聚合返回——否则第一个失败就会让其后的全部泄漏。
func (m *WorkerManager) cleanupPrepared(ctx context.Context) error {
	entries := m.snapshotPrepared()

	var errs []error
	for i := len(entries) - 1; i >= 0; i-- {
		if err := m.cleanupEntry(ctx, entries[i]); err != nil {
			errs = append(errs, fmt.Errorf("cleanup worker %q: %w", entries[i].worker.Name(), err))
		}
	}
	return errors.Join(errs...)
}

// cleanupEntry 对单个条目调用 Cleanup，保证**恰好一次**。
//
// Cleanup 自身要求幂等只是防御；管理器的调用计数仍必须精确，
// 否则"恰好一次"这条契约就没有任何一方在保证。
//
// cleanup ctx 必须独立于启动/运行 ctx：启动 deadline 过期正是最需要清理的时刻，
// 而此时启动 ctx 已经 Done——把它传进去会让每个 Cleanup 立即失败，资源照样泄漏。
func (m *WorkerManager) cleanupEntry(ctx context.Context, entry *preparedWorker) error {
	m.mu.Lock()
	if entry.cleaned {
		m.mu.Unlock()
		return nil
	}
	entry.cleaned = true
	m.mu.Unlock()

	cleanupCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx), m.budget.Cleanup)
	defer cancel()

	return entry.worker.Cleanup(cleanupCtx)
}

func (m *WorkerManager) snapshotPrepared() []*preparedWorker {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]*preparedWorker(nil), m.prepared...)
}

// withBoundedTimeout 返回 min(limit, 父 ctx 剩余) 的子 ctx。
//
// 与资源预算同一形态：单项上限不能冲破总预算。
func withBoundedTimeout(parent context.Context, limit time.Duration) (context.Context, context.CancelFunc) {
	if deadline, ok := parent.Deadline(); ok {
		if remaining := time.Until(deadline); remaining < limit {
			limit = remaining
		}
	}
	if limit <= 0 {
		return context.WithDeadline(parent, time.Now().Add(-time.Nanosecond))
	}
	return context.WithTimeout(parent, limit)
}
