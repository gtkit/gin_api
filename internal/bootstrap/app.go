package bootstrap

import (
	"context"
	"fmt"
	"os/signal"
	"syscall"

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

// App owns the process lifecycle and assembled runtime dependencies.
type App struct {
	runtime    *Runtime
	httpConfig HTTPConfig
	httpServer httpServer
	workers    *WorkerManager
}

func NewApp(options Options) (*App, error) {
	runtime, err := NewRuntime(options)
	if err != nil {
		return nil, err
	}

	providers, err := newProviders(runtime)
	if err != nil {
		runtime.Close()
		return nil, err
	}

	return &App{
		runtime:    runtime,
		httpConfig: providers.httpConfig,
		httpServer: providers.httpServer,
		workers:    providers.workers,
	}, nil
}

func (a *App) Run(ctx context.Context) error {
	defer a.closeResources()

	// 启动独立 pprof 端点（按配置决定是否真正监听）。放在最前面，确保进程一启动
	// 就能采样；defer 在退出时优雅关闭。未启用时 startPprofServer 返回空操作。
	// runtime/config 在单元测试中可能为 nil（与 closeResources 的 nil 容忍一致），
	// 故此处安全读取，缺失时按零值（Enabled=false，即禁用）处理。
	var pprofCfg config.PprofConfig
	if a.runtime != nil {
		if c := a.runtime.Config(); c != nil {
			pprofCfg = c.Pprof
		}
	}
	pprofShutdown := startPprofServer(pprofCfg)
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), pprofShutdownTimeout)
		defer cancel()
		if err := pprofShutdown(shutdownCtx); err != nil {
			logger.Errorf("shutdown pprof endpoint: %v", err)
		}
	}()

	runCtx, stop := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// 1. 启动 workers。workerCtx 独立于 runCtx：让关停顺序与启动对称——
	// 启动「workers → HTTP」，关停「HTTP → workers」，
	// 保证 HTTP 关停期间在飞请求写入的任务仍能被 worker 消费。
	// Errors() 必须在 Start() 之后取：错误 channel 由 Start 创建。
	workerCtx, cancelWorkers := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelWorkers()

	a.workers.Start(workerCtx)
	workerErrCh := a.workers.Errors()

	// 2. 启动 HTTP 服务。
	serverErrCh := make(chan error, 1)
	go func() {
		serverErrCh <- serveHTTPServer(a.httpServer, a.httpConfig)
	}()

	// 3. 等待三类退出信号中最先到达的一个：HTTP 退出、worker 失败、关停信号。
	serverExited := false
	var resultErr error

	select {
	case err := <-serverErrCh:
		serverExited = true
		if err != nil {
			resultErr = err
		}
	case err := <-workerErrCh:
		if err != nil {
			resultErr = fmt.Errorf("worker failed: %w", err)
		}
	case <-runCtx.Done():
		logger.Info("app received shutdown signal")
	}

	// 4. 先关 HTTP：停掉对外入口并等在飞请求收尾。
	if !serverExited {
		if err := shutdownHTTPServer(context.WithoutCancel(ctx), a.httpServer, a.httpConfig); err != nil && resultErr == nil {
			resultErr = fmt.Errorf("shutdown http server: %w", err)
		}

		if err := <-serverErrCh; err != nil && resultErr == nil {
			resultErr = err
		}
	}

	// 5. 后停 workers：此时已不会有新任务写入，取消 workerCtx 并在超时内等其退出。
	cancelWorkers()

	waitCtx, cancelWait := context.WithTimeout(context.WithoutCancel(ctx), a.httpConfig.ShutdownTimeout)
	defer cancelWait()
	if err := a.workers.Wait(waitCtx); err != nil && resultErr == nil {
		resultErr = fmt.Errorf("wait workers: %w", err)
	}

	return resultErr
}

func (a *App) closeResources() {
	if a.runtime != nil {
		a.runtime.Close()
	}
}
