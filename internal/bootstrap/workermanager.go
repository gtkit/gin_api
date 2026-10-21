package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	logger "github.com/gtkit/gin-api/internal/pkg/log"

	"github.com/gtkit/gin-api/internal/pkg/errx"
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

// ErrWorkerNotExited 表示任务未在停止预算内退出（Run 尚未返回）。
//
// 关停路径用它判别"存在存活任务"：兜底清理跳过这类任务，共享资源关闭被跳过，
// 进程以非零退出——既然任务已无法在预算内停止，"干净关闭"已不可达成，
// 唯一诚实的行为是显式报告非正常退出。
var ErrWorkerNotExited = errors.New("worker did not exit within the shutdown budget")

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

	// started 保证 Start 至多执行一次（见 ErrLifecycleReused）。
	started atomic.Bool

	mu       sync.Mutex
	prepared []*preparedWorker // 按 Prepare 成功的顺序追加，清理时逆序

	// errCh 汇聚运行期异常（含 ErrUnexpectedExit 与 WorkerPanicError）。
	// 只有 required worker 的异常写入此通道；optional 的异常只记录并标降级。
	errCh chan error

	// running 记录仍在跑的长期 worker，Stop 按相位取消。
	runMu    sync.Mutex
	running  map[WorkerKind][]*runningWorker
	degraded map[string]error
	// stuck 记录停止预算内未退出的任务。它们的 Cleanup 延迟到 Run 返回后
	// 由 supervise 执行；兜底清理与共享资源关闭都必须跳过它们——
	// 对仍在 Run 中的任务执行清理是数据竞争。
	stuck []*runningWorker
	// termErrs 汇集运行期 Cleanup 的失败（required 与 optional 一视同仁）：
	// 清理失败是进程退出时的资源事实，与"optional 运行故障不影响进程"的
	// 运行期语义无关。由 TerminationErrors 在关停末端一次性收割。
	termErrs []error

	// forced 是强制关停信号（第二个 OS 信号）。清理预算必须能被它切断：
	// cleanup ctx 刻意剥离了调用方的取消（见 cleanupEntry），若不单独接回
	// 强制信号，"再按一次立刻退出"就要在每个 worker 的清理预算上依次等满。
	// 装配期安装一次，进入并发阶段后不再改动。
	forced <-chan struct{}
}

// SetForceSignal 安装强制关停信号，供清理预算提前放弃。
//
// 只允许在装配阶段调用一次：与 Start 之后的并发读没有同步保护。
func (m *WorkerManager) SetForceSignal(ch <-chan struct{}) {
	if m != nil {
		m.forced = ch
	}
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
	maps.Copy(out, m.degraded)
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
	// 一次性守卫：重复 Start 会重复 Prepare 全部 worker、重复启动 goroutine。
	if !m.started.CompareAndSwap(false, true) {
		return fmt.Errorf("WorkerManager.Start: %w", ErrLifecycleReused)
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

		// cancel 不在本函数内调用是**有意的**——它被存进 runningWorker，
		// 由 StopKind 在关停对应相位时调用，这正是分相关停的实现方式。
		// 三个类别都由 gracefulShutdown / StopAll 覆盖，不存在无人调用的分支。
		//
		// 此处此前带一条 //nolint:gosec 抑制。当前钉住的 lint 版本不再在该行报告，
		// 于是那条抑制被 nolintlint 判为 unused——一条没有作用的抑制指令留着，
		// 只会让门禁永久报一条无法归零的问题。
		runCtx, cancel := context.WithCancel(base)
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

	// ctx 已取消时的**纯取消**是正常关停，不算异常。
	//
	// 必须逐成员判定（errx.IsPureCancellation）：errors.Is 对聚合错误任一成员
	// 命中即真，混入真实故障的 errors.Join(Canceled, realErr) 会被整体判成
	// 正常关停，那个故障就此静默消失。
	if err != nil && ctx.Err() != nil && errx.IsPureCancellation(err) {
		err = nil
	}

	if err != nil {
		m.reportRunFailure(w, err)
	}

	// 正常 Run 结束后同样要 Cleanup：只在"不会进入 Run"时清理，
	// 会让 Prepare 建立的连接与持有的锁在正常退出路径上无人释放。
	//
	// 清理错误不走 reportRunFailure：它在关停开始后无人消费 errCh，错误会
	// 静默消失，而进程带着清理失败以零退出码结束。改投终止错误集合，
	// 由关停末端统一收割进 Run 的返回值。
	if cerr := m.cleanupEntry(ctx, rw.entry); cerr != nil {
		m.recordTermination(fmt.Errorf("cleanup worker %q: %w", w.Name(), cerr))
	}
}

// recordTermination 登记一条终止期错误并落一次日志。
func (m *WorkerManager) recordTermination(err error) {
	logger.Errorf("worker termination error: %v", err)
	m.runMu.Lock()
	m.termErrs = append(m.termErrs, err)
	m.runMu.Unlock()
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
	if panicErr, ok := errors.AsType[*WorkerPanicError](err); ok {
		logger.Errorf("worker %s panicked: %v\n%s", panicErr.Worker, panicErr.Value, panicErr.Stack)
	}

	// H 系列（HErrorf）是告警执行点：worker 运行期异常与降级是进程级事件，
	// 经 Messager 扇出到已启用的告警通道。请求级异常不在此列——它们只走
	// 日志与指标，接入告警会在一次流量异常里形成告警风暴。
	if !w.Required() {
		m.markDegraded(w.Name(), err)
		// Error 而不是 Warn：能力已经没了，只是进程还活着。
		logger.HErrorf("optional worker %s stopped and was marked degraded: %v", w.Name(), err)
		return
	}

	wrapped := fmt.Errorf("worker %s: %w", w.Name(), err)
	select {
	case m.errCh <- wrapped:
		// 入队成功也要落日志：关停开始后通道可能再无消费者（错误由
		// TerminationErrors 在末端排空），不落日志的话，从入队到收割
		// 之间这个错误在任何输出里都不存在。
		logger.HErrorf("required worker failed: %v", wrapped)
	default:
		// 通道容量按 worker 数分配，写满说明已有足够多的异常在等待处理；
		// 丢弃后仍记录，避免在关停路径上阻塞。
		logger.HErrorf("worker error channel is full, dropping: %v", wrapped)
	}
}

// TerminationErrors 收割终止期无人消费的任务错误：非阻塞排空 errCh
// （关停开始后 required 任务的运行错误再无消费者），并取走 Cleanup 错误集合。
//
// 只应在全部 StopKind 与 CleanupRemaining 完成之后调用：StopKind 等待
// rw.done，而 done 在 supervise 写完错误之后才关闭，此时序保证收割不漏。
// 已被 awaitShutdown 消费并记为关停 cause 的那个错误不在通道里，天然不重复。
func (m *WorkerManager) TerminationErrors() error {
	if m == nil {
		return nil
	}

	var errs []error
	for {
		select {
		case err := <-m.errCh:
			errs = append(errs, err)
		default:
			m.runMu.Lock()
			errs = append(errs, m.termErrs...)
			m.termErrs = nil
			m.runMu.Unlock()
			return errors.Join(errs...)
		}
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
			m.runMu.Lock()
			m.stuck = append(m.stuck, rw)
			m.runMu.Unlock()
			errs = append(errs, fmt.Errorf(
				"worker %q: %w: %v", rw.entry.worker.Name(), ErrWorkerNotExited, ctx.Err()))
		}
	}
	return errors.Join(errs...)
}

// LiveWorkers 返回 Run 仍未返回的任务名快照，并把已退出的条目移出追踪。
//
// 已退出条目的清理由 supervise 负责（rw.done 在 Cleanup 之后才关闭），
// 这里只需把它们从"存活"集合中剔除。
func (m *WorkerManager) LiveWorkers() []string {
	if m == nil {
		return nil
	}
	m.runMu.Lock()
	defer m.runMu.Unlock()

	kept := m.stuck[:0]
	var live []string
	for _, rw := range m.stuck {
		select {
		case <-rw.done:
		default:
			kept = append(kept, rw)
			live = append(live, rw.entry.worker.Name())
		}
	}
	m.stuck = kept
	return live
}

// liveEntries 返回 Run 仍未返回的任务条目集合，供兜底清理过滤。
func (m *WorkerManager) liveEntries() map[*preparedWorker]struct{} {
	m.runMu.Lock()
	defer m.runMu.Unlock()

	live := make(map[*preparedWorker]struct{})
	for _, rw := range m.stuck {
		select {
		case <-rw.done:
		default:
			live[rw.entry] = struct{}{}
		}
	}
	return live
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
	live := m.liveEntries()

	var errs []error
	for _, entrie := range slices.Backward(entries) {
		if _, running := live[entrie]; running {
			// Run 未返回的任务不清理：与 Run 并发执行 Cleanup 是数据竞争，
			// 且先行标记"已清理"会让任务真正退出后的正常清理被跳过。
			// 它的清理由 supervise 在 Run 返回后执行，或随进程退出回收。
			logger.Warnf("skipping cleanup of worker %q: its Run has not returned",
				entrie.worker.Name())
			continue
		}
		if err := m.cleanupEntry(ctx, entrie); err != nil {
			errs = append(errs, fmt.Errorf("cleanup worker %q: %w", entrie.worker.Name(), err))
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

	// 剥离调用方取消 + 独立预算 + **接回强制信号**：前两者的理由见上，
	// 而强制路径的前提正是"已经等不下去了"——不接回的话，第二个信号之后
	// 每个 worker 的清理预算仍会被依次等满，"立刻退出"名存实亡。
	cleanupCtx, cancel := context.WithTimeout(
		context.WithoutCancel(ctx), m.budget.Cleanup)
	defer cancel()
	if m.forced != nil {
		stop := make(chan struct{})
		defer close(stop)
		go func() {
			select {
			case <-m.forced:
				cancel()
			case <-stop:
			}
		}()
	}

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
