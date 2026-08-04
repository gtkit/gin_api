package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gtkit/gin-api/internal/platform/config"

	logger "github.com/gtkit/logger/v2"
)

// Options defines runtime startup options.
// 配置改为从固定 config/env.yml 读取后，启动不再需要环境名参数。
type Options struct {
	// UseMigrationCredentials 让本次装配的数据库连接优先使用迁移专用凭据。
	//
	// 只有 migrate 子命令置 true；server 永远为 false，因此运行账号不会因为配置了
	// 迁移凭据而改变自身连接身份。迁移凭据未配置时回退运行凭据并告警（不失败，
	// 避免破坏尚未配置迁移凭据的存量部署）。
	UseMigrationCredentials bool
}

// defaultWorkerShutdownTimeout 是 worker drain 的独立预算。
//
// 与 HTTP 的 ShutdownTimeout 分开：两者要等的东西不同（HTTP 等在飞请求收尾，
// worker 等一批任务处理完），共用一个值只能取二者中更保守的那个，
// 结果是要么 HTTP 等太久、要么 worker 被过早截断。
const defaultWorkerShutdownTimeout = 30 * time.Second

// App owns the process lifecycle and assembled runtime dependencies.
type App struct {
	runtime    *Runtime
	httpConfig HTTPConfig
	httpServer httpServer
	workers    *WorkerManager
	shutdown   *ShutdownCoordinator
	// rateLimits 持有限流注册表；关停时必须关闭它，否则每条本地策略的
	// 后台清理 goroutine 会一直活到进程退出。
	rateLimits rateLimitAssembly
	// drainWait 是停 producer 之后、关 HTTP 之前的等待时长，
	// 用于让负载均衡器观察到 /ready 的 503 并把本实例摘出转发目标。
	drainWait time.Duration
	// status 供 readiness 读取进程自身状态；pprof 启动后登记进去。
	status *processStatus
}

func NewApp(ctx context.Context, options Options) (*App, error) {
	runtime, err := NewRuntime(ctx, options)
	if err != nil {
		return nil, err
	}

	// 装配期确实会做 I/O：限流的 Redis backend 要 ScriptLoad 并跑一次探针。
	// 因此启动 ctx 必须传进去——启动被信号打断时，那次 Redis 往返应当随之取消，
	// 而不是把启动流程拖到它自己的超时为止。
	//
	// （此处曾用 context.Background() 并注释成"装配阶段不做任何 I/O"。
	//   那句话对 buildMiddlewares 成立，对限流不成立。）
	providers, err := newProviders(ctx, runtime)
	if err != nil {
		runtime.Close()
		return nil, err
	}

	return &App{
		runtime:    runtime,
		httpConfig: providers.httpConfig,
		httpServer: providers.httpServer,
		workers:    providers.workers,
		shutdown:   providers.shutdown,
		rateLimits: providers.rateLimits,
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
func (a *App) Run(ctx context.Context) error {
	defer a.closeResources()

	// pprof 绑定失败即启动失败：一个"配置说开了但实际没监听"的端点
	// 会让排障的人对着一个不存在的地址反复尝试。
	pprofEndpoint, err := startPprofServer(a.pprofConfig())
	if err != nil {
		return fmt.Errorf("start pprof endpoint: %w", err)
	}
	// 登记给 readiness：pprof 运行期异常退出时，实例仍可接流量，
	// 但就绪信息里要说清楚"这台机器上没有 pprof 了"。
	a.status.attachPprof(pprofEndpoint)

	// 第一个信号进入优雅关停，第二个信号进入强制路径。
	// 用 signal.Notify 而不是 signal.NotifyContext：后者只能表达"收到过信号"，
	// 无法区分第一个与第二个——而"再按一次 Ctrl+C 立刻退出"正是运维的普遍预期。
	signals := make(chan os.Signal, 2)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	go a.watchSignals(signals)

	// 上层 ctx 取消同样是一次关停请求（cobra ExecuteContext / 测试驱动）。
	go func() {
		select {
		case <-ctx.Done():
			a.shutdown.RequestShutdown(nil)
		case <-a.shutdown.Requested():
		}
	}()

	// ① 预绑定监听端口并校验 TLS 材料——必须在 worker 启动屏障**之前**。
	//    端口占用与证书过期都是"要么立刻成功、要么永远不会成功"的条件；
	//    放到屏障之后失败，required one-shot 的不可逆副作用就已经发生了。
	bound, prebindErr := prebindHTTP(a.httpConfig)
	if prebindErr != nil {
		// 启动回滚：pprof 已经开了采样，必须在这里清零。
		_ = pprofEndpoint.Shutdown(context.WithoutCancel(ctx))
		return fmt.Errorf("prebind http: %w", prebindErr)
	}
	// 屏障失败时关闭已绑定的监听器，不把端口一直占着。
	prebindClosed := false
	defer func() {
		if !prebindClosed {
			_ = bound.Close()
		}
	}()

	// ② worker 启动屏障。失败即启动失败：HTTP 从不开始服务。
	if err := a.workers.Start(ctx); err != nil {
		_ = pprofEndpoint.Shutdown(context.WithoutCancel(ctx))
		return fmt.Errorf("start workers: %w", err)
	}

	// ③ HTTP。监听器所有权自此转交给 server，由 Shutdown/Close 关闭。
	prebindClosed = true
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

// watchSignals 把第一个信号翻译成优雅关停请求，第二个翻译成强制关停。
func (a *App) watchSignals(signals <-chan os.Signal) {
	first := true
	for sig := range signals {
		if first {
			logger.Infof("received %s: starting graceful shutdown", sig)
			a.shutdown.RequestShutdown(nil)
			first = false
			continue
		}
		logger.Warnf("received %s again: forcing shutdown", sig)
		a.shutdown.Force()
		return
	}
}

// awaitShutdown 阻塞直到出现任一关停触发源，返回 HTTP 是否已自行退出。
//
// 三类触发源全部经 RequestShutdown 进入同一个信号：OS 信号（watchSignals）、
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
	base := context.WithoutCancel(ctx)
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
	if !serverExited {
		collect(a.shutdownHTTP(base))
		if err := <-serverErrCh; err != nil {
			collect(err)
		}
	}

	// 4. drain consumer：此时不会再有新请求写入任务。
	collect(a.stopKindWithBudget(base, KindConsumer))

	// 5. 停 bootstrap 类 worker（optional OneShot 可能仍在跑）。
	collect(a.stopKindWithBudget(base, KindBootstrap))

	// 6. 兜底清理：正常路径上各 worker 的 Cleanup 已在 Run 返回后执行，
	//    这里只覆盖关停超时导致 supervise 未走完的条目，保证"恰好一次"不漏。
	collect(a.workers.CleanupRemaining(base))

	// 7. 关限流：每条本地策略都有一个后台清理 goroutine。
	//    排在 HTTP 之后：在飞请求可能仍要做限流判定。
	collect(closeRateLimits(a.rateLimits))

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
func (a *App) stopKindWithBudget(base context.Context, kind WorkerKind) error {
	stopCtx, cancel := context.WithTimeout(base, defaultWorkerShutdownTimeout)
	defer cancel()
	return a.workers.StopKind(stopCtx, kind)
}

// shutdownHTTP 优雅关闭主 HTTP；强制路径直接 Close。
//
// Shutdown 会等在途请求收尾，而强制路径的前提正是"已经等不下去了"——
// 此时继续 Shutdown 只会让进程在故障关停中无限滞留。
func (a *App) shutdownHTTP(base context.Context) error {
	if a.shutdown.Forced() {
		logger.Warn("forced shutdown: closing http server without draining in-flight requests")
		return a.httpServer.Close()
	}

	shutdownCtx, cancel := context.WithCancel(base)
	defer cancel()
	// 强制信号在 Shutdown 等待期间到达时，立刻取消等待并改用 Close。
	go func() {
		select {
		case <-a.shutdown.ForcedCh():
			cancel()
		case <-shutdownCtx.Done():
		}
	}()

	if err := shutdownHTTPServer(shutdownCtx, a.httpServer, a.httpConfig); err != nil {
		logger.Warnf("graceful http shutdown failed (%v); closing listeners", err)
		return errors.Join(err, a.httpServer.Close())
	}
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
		return fmt.Errorf("shutdown pprof endpoint: %w", err)
	}
	return nil
}

func (a *App) closeResources() {
	if a.runtime != nil {
		a.runtime.Close()
	}
}
