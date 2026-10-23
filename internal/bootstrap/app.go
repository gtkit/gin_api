package bootstrap

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gtkit/gin-api/internal/platform/config"

	"github.com/gtkit/logger/v2"
)

// Options defines runtime startup options.
// 服务配置由统一加载入口提供；这里仅接收进程级协作对象，
// 不再通过启动选项重复指定运行环境或配置内容。
type Options struct {
	// Shutdown 是进程级关停协调器，由**唯一的信号监督者**（cmd 层）构造并注入：
	// 信号在 NewApp 之前就可能到达，协调器因此必须比 App 更早存在。
	//
	// 为 nil 时 App 自建一个（单测与不接信号的命令走这条路），行为不变。
	Shutdown *ShutdownCoordinator

	// factories is a package-private acquisition seam for lifecycle tests.
	factories *runtimeFactories
}

// defaultWorkerShutdownTimeout 是 worker drain 的独立预算。
//
// 与 HTTP 的 ShutdownTimeout 分开：两者要等的东西不同（HTTP 等在飞请求收尾，
// worker 等一批任务处理完），共用一个值只能取二者中更保守的那个，
// 结果是要么 HTTP 等太久、要么 worker 被过早截断。
const defaultWorkerShutdownTimeout = 30 * time.Second

// App owns the process lifecycle and assembled runtime dependencies.
type App struct {
	cleanup    *providerCleanup
	runtime    *Runtime
	httpConfig HTTPConfig
	httpServer httpServer
	workers    *WorkerManager
	shutdown   *ShutdownCoordinator
	// drainWait 是停 producer 之后、关 HTTP 之前的等待时长，
	// 用于让负载均衡器观察到 /ready 的 503 并把本实例摘出转发目标。
	drainWait time.Duration
	// status 供 readiness 读取进程自身状态；pprof 启动后登记进去。
	status *processStatus
	// started 保证 Run 至多执行一次（见 ErrLifecycleReused）。
	started atomic.Bool
	// extClosed 保证扩展产物（限流注册表、指标回调）恰好释放一次，
	// 正常关停序列与启动失败的提前返回共用（见 closeExtensions）。
	extClosed atomic.Bool
	// Only a successful Shutdown proves all HTTP log producers have returned.
	httpStarted   bool
	httpDrained   bool
	extensionsErr error
	pprofErr      error
}

func NewApp(ctx context.Context, options Options) (*App, error) {
	if options.Shutdown == nil {
		options.Shutdown = NewShutdownCoordinator()
	}
	budget := &rollbackBudget{force: options.forceSignal()}
	defer budget.Close()
	runtime, err := newRuntime(ctx, options, budget)
	if err != nil {
		return nil, err
	}

	// 装配期确实会做 I/O：限流的 Redis backend 要 ScriptLoad 并跑一次探针。
	// 因此启动 ctx 必须传进去——启动被信号打断时，那次 Redis 往返应当随之取消，
	// 而不是把启动流程拖到它自己的超时为止。
	//
	// （此处曾用 context.Background() 并注释成"装配阶段不做任何 I/O"。
	//   那句话对 buildMiddlewares 成立，对限流不成立。）
	providers, err := newProvidersWithBudget(ctx, runtime, options.Shutdown, budget)
	if err != nil {
		return nil, errors.Join(err, runtime.Close(budget.Context(ctx)))
	}

	return &App{
		cleanup:    providers.cleanup,
		runtime:    runtime,
		httpConfig: providers.httpConfig,
		httpServer: providers.httpServer,
		workers:    providers.workers,
		shutdown:   providers.shutdown,
		drainWait:  drainWaitFrom(runtime.Config()),
		status:     providers.status,
	}, nil
}

// drainWaitFrom 解析 LB 传播等待时长。
//
// 未配置时用默认值而不是 0：0 意味着 /ready 刚翻成 503 就立刻关闭监听，
// 而负载均衡器还没来得及把本实例摘出转发目标——那一小段时间里到达的请求
// 会直接收到连接错误，而不是被优雅地处理完。
func drainWaitFrom(cfg *config.Config) time.Duration {
	if cfg == nil {
		return config.DefaultDrainWait
	}
	return cfg.App.EffectiveDrainWait()
}

// Run 启动进程并阻塞直到关停完成。
//
// 启动顺序：worker 启动屏障（Prepare → required OneShot → 后台 worker）→ HTTP Serve。
// required OneShot 必须先跑完：它们代表"对外服务之前必须做完的事"，
// 放到 HTTP 之后就失去了全部意义。
func (a *App) Run(ctx context.Context) (err error) {
	// 一次性守卫必须在任何 defer 与副作用之前：第二次调用不得触发
	// 资源关闭、退出告警或信号监听。
	if !a.started.CompareAndSwap(false, true) {
		return fmt.Errorf("App.Run: %w", ErrLifecycleReused)
	}

	// 资源关闭错误必须进入返回值：只记日志会让关闭失败的进程仍报告"正常退出"。
	//
	// closeExtensions 兜住全部提前返回：NewApp 成功后限流注册表与指标回调的
	// 所有权已在 App，启动失败路径若只关连接资源，本地限流器的清理 goroutine
	// 与 OTel 回调就地泄漏。正常路径它已在 gracefulShutdown 内执行过，
	// 这里的幂等调用是空操作。
	//
	// 退出告警必须先于 closeResources：runtime.Close 内的日志刷写会关闭并
	// 排空告警队列，之后入队的消息被静默丢弃。资源关闭阶段自身的错误因此
	// 物理上无法再经告警通道送出，它们只进返回值与日志。
	defer func() {
		stopCtx, finish := cleanupContext(context.WithoutCancel(ctx), a.shutdown.ForcedCh())
		defer finish()
		err = errors.Join(err, a.closeExtensions(stopCtx))
		alertExitError(err)
		err = errors.Join(err, a.closeResources(stopCtx))
	}()

	// pprof 绑定失败即启动失败：一个"配置说开了但实际没监听"的端点
	// 会让排障的人对着一个不存在的地址反复尝试。
	pprofEndpoint, err := startPprofServer(a.pprofConfig())
	if err != nil {
		return fmt.Errorf("start pprof endpoint: %w", err)
	}
	// 登记给 readiness：pprof 运行期异常退出时，实例仍可接流量，
	// 但就绪信息里要说清楚"这台机器上没有 pprof 了"。
	a.status.attachPprof(pprofEndpoint)

	// **App 不监听 OS 信号**（runtime-state-machine spec：进程只有一个信号
	// 监督者）。信号由进程入口（cmd 层）唯一监听并翻译成协调器动作：
	// 第一次 → RequestShutdown + 取消本 ctx（覆盖装配期），第二次 → Force。
	//
	// 曾经这里也 signal.Notify 过一份，与入口的监听**同时收到同一个物理信号**，
	// 于是任何"看对方是否已请求关停"的判定都成了竞态：调度顺序不同即可让
	// 首次信号被判成第二次而直接强制关停，整段 drain 被跳过。
	//
	// watchDone 让翻译协程随 Run 返回而退出，不残留。
	watchDone := make(chan struct{})
	defer close(watchDone)

	// 上层 ctx 取消是一次关停请求（信号监督者、cobra ExecuteContext、测试驱动）。
	go func() {
		select {
		case <-ctx.Done():
			a.shutdown.RequestShutdown(nil)
		case <-a.shutdown.Requested():
		case <-watchDone:
		}
	}()

	// ① 预绑定监听端口并校验 TLS 材料——必须在 worker 启动屏障**之前**。
	//    端口占用与证书过期都是"要么立刻成功、要么永远不会成功"的条件；
	//    放到屏障之后失败，required one-shot 的不可逆副作用就已经发生了。
	bound, prebindErr := prebindHTTP(a.httpConfig)
	if prebindErr != nil {
		// 启动回滚：pprof 已经开了采样，必须在这里清零。走 shutdownPprof
		// 而不是裸 Shutdown——后者没有预算，一个长时间 profile 请求会把
		// 启动失败路径无限拖住；关闭错误进返回值，与正常关停同款语义。
		return errors.Join(fmt.Errorf("prebind http: %w", prebindErr),
			a.shutdownPprof(context.WithoutCancel(ctx), pprofEndpoint))
	}
	// 屏障失败时关闭已绑定的监听器，不把端口一直占着。
	prebindClosed := false
	defer func() {
		if !prebindClosed {
			if closeErr := bound.Close(); closeErr != nil {
				err = errors.Join(err, fmt.Errorf("close prebound http listener: %w", closeErr))
			}
		}
	}()

	// TLS：预绑定阶段已装载并校验的证书注入服务配置，服务期不再读文件。
	// http.Server 与 a.httpConfig 持有同一个 *tls.Config，serve goroutine
	// 启动之前的单线程窗口内注入，net/http 在 ServeTLS 时克隆该配置。
	if bound.certificate != nil && a.httpConfig.TLSConfig != nil {
		a.httpConfig.TLSConfig.Certificates = []tls.Certificate{*bound.certificate}
	}

	// ② worker 启动屏障。失败即启动失败：HTTP 从不开始服务。
	//
	// 屏障必须响应关停请求：signal.Notify 接管信号后默认的进程终止已被禁用，
	// 若关停请求不作用于屏障，操作者面对一个卡住的启动任务连按 Ctrl+C 也只能
	// 等启动预算耗尽——信号被捕获却不被执行。startCtx 在关停被请求时取消
	// （Force 内部同样经 RequestShutdown，第二个信号无需单独监听）；启动回滚的
	// 清理经 WithoutCancel 剥离取消，预算不受 startCtx 取消影响。
	startCtx, cancelStart := context.WithCancel(ctx)
	go func() {
		select {
		case <-a.shutdown.Requested():
			cancelStart()
		case <-startCtx.Done():
		}
	}()
	startErr := a.workers.Start(startCtx)
	cancelStart()
	if startErr != nil {
		// 启动回滚：带预算关闭 pprof（理由见 prebind 失败分支），
		// 关闭错误 Join 进各返回分支。
		pprofErr := a.shutdownPprof(context.WithoutCancel(ctx), pprofEndpoint)
		// 上层 ctx 取消同样是一次关停请求；把它幂等登记进协调器，
		// 使"因关停而中止"的判定不依赖 ctx 翻译协程的调度时序。
		if ctx.Err() != nil {
			a.shutdown.RequestShutdown(nil)
		}
		if a.shutdownRequested() {
			// 屏障因关停请求而中止：按关停语义退出（纯信号为 nil、强制为
			// ErrForcedShutdown），不是一次启动失败。已 Prepare 的任务
			// 已由 Start 内部逆序清理。**纯取消**（全部分支归结为
			// context.Canceled）是中止的现象而不是原因，不进入返回值；
			// 混入清理/回滚失败或其他故障的错误树必须保留上报——
			// errors.Is 对 Join 任一成员命中即真，不能用它整体判定。
			if IsPureCancellation(startErr) {
				logger.Infof("startup barrier canceled by a shutdown request: %v", startErr)
				return errors.Join(a.shutdown.Cause(), pprofErr)
			}
			return errors.Join(a.shutdown.Cause(), fmt.Errorf("start workers: %w", startErr), pprofErr)
		}
		return errors.Join(fmt.Errorf("start workers: %w", startErr), pprofErr)
	}

	// 屏障成功但关停已被请求：HTTP 不再开始服务，消除"先 serve 再立即关停"
	// 的窗口——编排器不该观察到一次毫无意义的 serving 相位。
	if ctx.Err() != nil {
		a.shutdown.RequestShutdown(nil)
	}
	if a.shutdownRequested() {
		logger.Info("shutdown requested during startup: skipping http serve")
		return a.gracefulShutdown(ctx, nil, true, pprofEndpoint)
	}

	// ③ HTTP。监听器所有权自此转交给 server，由 Shutdown/Close 关闭。
	prebindClosed = true
	a.httpStarted = true
	serverErrCh := make(chan error, 1)
	go func() {
		serverErrCh <- serveHTTPServer(a.httpServer, a.httpConfig, bound.listener)
	}()
	a.shutdown.MarkServing()

	// ④ 等待关停：全部触发源都经 RequestShutdown 汇聚到同一个信号上。
	serverExited := a.awaitShutdown(serverErrCh)

	// ⑤ 分相关停。
	return a.gracefulShutdown(ctx, serverErrCh, serverExited, pprofEndpoint)
}

// closeExtensions 释放 provider 登记的模块、限流、出站、入队、遥测及指标回调。
//
// 幂等（CAS 保证恰好一次）：gracefulShutdown 在正常序列的正确位置调用它，
// Run 的 defer 兜住全部提前返回——两处共用，不存在只被一条路径覆盖的释放。
func (a *App) closeExtensions(ctx context.Context) error {
	if a.httpStarted && !a.httpDrained {
		a.extensionsErr = errors.New("skipping extension close: HTTP handlers are not confirmed stopped")
		return a.extensionsErr
	}
	if live := a.workers.LiveWorkers(); len(live) > 0 {
		a.extensionsErr = fmt.Errorf("skipping extension close, %d worker(s) still running (%s): %w",
			len(live), strings.Join(live, ", "), ErrWorkerNotExited)
		return a.extensionsErr
	}
	if !a.extClosed.CompareAndSwap(false, true) {
		return nil
	}
	a.extensionsErr = a.cleanup.close(ctx)
	return a.extensionsErr
}

// alertExitError 对非 nil 的退出错误发出一次进程级告警。
//
// H 系列（HErrorf）是告警执行点：进程以错误结束是进程级事件，与 worker
// 异常/降级（reportRunFailure）同属唯一的一组告警触发面。请求级异常
// （HTTP panic、单请求错误）不接告警，只走日志与指标。
func alertExitError(err error) {
	if err == nil {
		return
	}
	logger.HError(fmt.Sprintf("process exiting with error: %v", err))
}

// shutdownRequested 非阻塞探测是否已有关停请求。
func (a *App) shutdownRequested() bool {
	select {
	case <-a.shutdown.Requested():
		return true
	default:
		return false
	}
}

// awaitShutdown 阻塞直到出现任一关停触发源，返回 HTTP 是否已自行退出。
//
// 三类触发源全部经 RequestShutdown 进入同一个信号：OS 信号（由 cmd 层唯一的
// 信号监督者翻译，App 自己不监听）、
// required worker 运行期异常、主 HTTP serve 异常。各自直接执行关停动作时，
// 并发到达会重复 close channel、重复关闭连接、重复清理，
// 而这些重复中的任何一个都会把一次正常关停变成一次报错的关停。
func (a *App) awaitShutdown(serverErrCh <-chan error) bool {
	select {
	case err := <-serverErrCh:
		if err != nil {
			a.shutdown.RequestShutdown(fmt.Errorf("http server: %w", err))
		} else {
			a.shutdown.RequestShutdown(nil)
		}
		return true

	case err := <-a.workers.Errors():
		if err != nil {
			a.shutdown.RequestShutdown(fmt.Errorf("worker failed: %w", err))
		}
		return false

	case <-a.shutdown.Requested():
		return false
	}
}

// gracefulShutdown 执行写死的分相关停顺序。
//
//	停 producer → 等 LB 传播 → 关主 HTTP 并等在途请求收尾 →
//	drain consumer → 停 bootstrap worker → 关 auxiliary（pprof）→ 关资源
//
// 顺序必须一次定死：producer（cron/scheduler）若不先停，会在 HTTP 关停期间
// 继续生产新任务，而随后 consumer 被取消，新任务无人消费或半途中断。
// consumer 若不在 HTTP 之后才 drain，HTTP 关停期间在飞请求写入的任务就会丢。
func (a *App) gracefulShutdown(
	ctx context.Context, serverErrCh <-chan error, serverExited bool, pprofEndpoint *pprofHandle,
) error {
	// base 剥离父 ctx 的取消（优雅路径不能被早已取消的信号 ctx 打断），
	// 但接入强制信号：契约是"第二个信号取消**各阶段**剩余等待"，而尾段
	// （worker 兜底清理、扩展释放、telemetry flush、pprof）此前不在覆盖
	// 范围里——各自的预算串行叠加后，"再按一次立即退出"实际要等数十秒。
	base, finish := forceContext(context.WithoutCancel(ctx), a.shutdown.ForcedCh())
	defer finish()
	var errs []error
	collect := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}

	logger.Infof("shutdown phase=%s", a.shutdown.Phase())

	// 1. 停 producer：不再产生新任务。
	collect(a.stopKindWithBudget(base, KindProducer))

	// 2. 等 LB 传播：/ready 已返回 503，给编排器时间把本实例摘出转发目标。
	//    强制路径不等。
	a.shutdown.WaitOrForce(a.drainWait)

	// 3. 关主 HTTP 并等在途请求收尾。
	if a.httpStarted || !serverExited {
		collect(a.shutdownHTTP(base))
		if !serverExited {
			if err := <-serverErrCh; err != nil {
				collect(err)
			}
		}
	}

	// 4. drain consumer：此时不会再有新请求写入任务。
	collect(a.stopKindWithBudget(base, KindConsumer))

	// 5. 停 bootstrap 类 worker（optional OneShot 可能仍在跑）。
	collect(a.stopKindWithBudget(base, KindBootstrap))

	// 6. 兜底清理：正常路径上各 worker 的 Cleanup 已在 Run 返回后执行，
	//    这里只覆盖关停超时导致 supervise 未走完的条目，保证"恰好一次"不漏。
	collect(a.workers.CleanupRemaining(base))

	// 6b. 收割终止错误：关停开始后 required 任务的运行错误与全部任务的
	//     Cleanup 错误再无其他消费者，不收割就是静默丢失——进程会带着
	//     失败以零退出码结束。必须在全部 StopKind 与兜底清理之后执行。
	collect(a.workers.TerminationErrors())

	// 7. 释放扩展产物：模块客户端、限流注册表及出站/入队/遥测资源和指标回调。
	//    排在 HTTP 与 worker 之后：在飞请求可能仍要做限流
	//    判定，worker 停掉之前采集到的指标仍是有效状态。
	//    与 Run 的 defer 共用同一个幂等入口，启动失败的提前返回同样被兜住。
	collect(a.closeExtensions(base))

	// 8. 关 auxiliary handle（pprof）。它是 optional 能力，错误只聚合不升级。
	collect(a.shutdownPprof(base, pprofEndpoint))

	// 9. 资源由 closeResources（defer）按建立逆序关闭。

	collect(a.shutdown.Cause())

	if a.shutdown.Forced() {
		// 强制路径必须非零退出：进程是被迫结束的，不能表现成一次正常关停。
		return errors.Join(append(errs, ErrForcedShutdown)...)
	}
	return errors.Join(errs...)
}

// stopKindWithBudget 在独立预算内停止某一类 worker。
//
// 强制信号必须能取消这里的等待：三类 worker 的停止预算是串行叠加的，
// 不接入强制信号，"再按一次 Ctrl+C 立刻退出"实际要等上数十秒。
// 与 shutdownHTTP 同款模式；已处于强制状态时 ForcedCh 已关闭，等待立刻取消。
func (a *App) stopKindWithBudget(base context.Context, kind WorkerKind) error {
	stopCtx, cancel := context.WithTimeout(base, defaultWorkerShutdownTimeout)
	defer cancel()
	stopCtx, finish := forceContext(stopCtx, a.shutdown.ForcedCh())
	defer finish()
	return a.workers.StopKind(stopCtx, kind)
}

// shutdownHTTP 优雅关闭主 HTTP；强制路径直接 Close。
//
// Shutdown 会等在途请求收尾，而强制路径的前提正是"已经等不下去了"——
// 此时继续 Shutdown 只会让进程在故障关停中无限滞留。
func (a *App) shutdownHTTP(base context.Context) error {
	a.httpStarted = true
	if a.shutdown.Forced() {
		logger.Warn("forced shutdown: closing http server without draining in-flight requests")
		return a.httpServer.Close()
	}

	shutdownCtx, finish := forceContext(base, a.shutdown.ForcedCh())
	defer finish()

	if err := shutdownHTTPServer(shutdownCtx, a.httpServer, a.httpConfig); err != nil {
		logger.Warnf("graceful http shutdown failed (%v); closing listeners", err)
		return errors.Join(err, a.httpServer.Close())
	}
	a.httpDrained = true
	return nil
}

// pprofConfig 读取 pprof 配置；runtime/config 在单元测试中可能为 nil，
// 缺失时按零值（Enabled=false，即禁用）处理。
func (a *App) pprofConfig() config.PprofConfig {
	if c := a.runtime.Config(); c != nil {
		return c.Pprof
	}
	return config.PprofConfig{}
}

// shutdownPprof 关闭 pprof 端点。
//
// 它是**可选能力**：关停失败进入聚合错误但不升级为全进程失败。
// 运行期已异常退出时只记录一次，不把它当成新的关停错误——端点早就没了。
func (a *App) shutdownPprof(base context.Context, endpoint *pprofHandle) error {
	if endpoint == nil {
		return nil
	}
	if endpoint.Exited() {
		logger.Warn("pprof endpoint had already exited before shutdown; " +
			"the capability was degraded for part of this process lifetime")
	}

	shutdownCtx, cancel := context.WithTimeout(base, pprofShutdownTimeout)
	defer cancel()

	if err := endpoint.Shutdown(shutdownCtx); err != nil {
		a.pprofErr = fmt.Errorf("shutdown pprof endpoint: %w", err)
		return a.pprofErr
	}
	return nil
}

// closeResources 关闭进程级共享资源。
//
// 关闭前查询仍未退出的任务：存活任务手里的连接被并发关闭等价于使用中拔资源。
// 此时跳过关闭、返回可判别错误，回收交给进程退出。判定放在这里而不是
// gracefulShutdown：defer 路径在关闭那一刻查询实时状态，天然覆盖 Run 的
// 全部提前返回分支。
func (a *App) closeResources(ctx context.Context) error {
	if live := a.workers.LiveWorkers(); len(live) > 0 {
		// Live producers retain the native instances and notification channels.
		// Only file buffers can be flushed safely here.
		return errors.Join(fmt.Errorf("skipping resource close, %d worker(s) still running (%s): %w",
			len(live), strings.Join(live, ", "), ErrWorkerNotExited), a.runtime.DrainLogs(ctx))
	}
	if a.runtime == nil {
		return nil
	}
	if (a.httpStarted && !a.httpDrained) || a.extensionsErr != nil || a.pprofErr != nil {
		return errors.Join(errors.New("skipping resource close: log producers are not confirmed stopped"),
			a.runtime.DrainLogs(ctx))
	}
	return a.runtime.Close(ctx)
}

// forceSignal 返回注入协调器的强制关停信号；未注入时为 nil（排空只受预算约束）。
func (o Options) forceSignal() <-chan struct{} {
	if o.Shutdown == nil {
		return nil
	}
	return o.Shutdown.ForcedCh()
}
