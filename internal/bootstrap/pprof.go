package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/pprof"
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gtkit/gin-api/internal/platform/config"

	logger "github.com/gtkit/logger/v2"
)

const (
	// pprofShutdownTimeout pprof 端点优雅关闭的等待上限。
	pprofShutdownTimeout = 3 * time.Second
	// pprofReadHeaderTimeout 仅限制读取请求头，避免慢连接占用；
	// 不设 WriteTimeout，否则会掐断 profile?seconds=N / trace 这类长采集。
	pprofReadHeaderTimeout = 5 * time.Second

	defaultPprofHost = "127.0.0.1"
	defaultPprofPort = "6060"
)

// pprofHandle 是受管理的 pprof 端点。
//
// 它替代此前的 "fire-and-forget goroutine"：那种写法在监听失败或运行期退出时
// 只打一条日志，状态随即永久丢失——readiness 仍报一切正常，而端点其实早就没了，
// 排障时连"它到底有没有在跑"都无从判断。
type pprofHandle struct {
	server   *http.Server
	listener net.Listener
	// errCh 承载 Serve 的退出结果，由第一次 Shutdown 取走。
	errCh chan error
	// serveOnce + serveErr 缓存 Serve 的结果。
	//
	// 没有缓存的话，第二次 Shutdown 会在一个已经被取空的 channel 上等到预算耗尽——
	// 而关停触发源不止一个（正常关停、启动回滚），重复调用是常态。
	serveOnce sync.Once
	serveErr  error
	// cleanupOnce 保证采样率**恰好清零一次**：异常退出、正常关停与启动回滚
	// 三条路径都会走到这里。
	cleanupOnce sync.Once
	// exited 记录运行期是否已异常退出（用于把该能力标 degraded）。
	exited atomic.Bool
}

// startPprofServer 建立并启动独立的 pprof HTTP 端点。
//
// 设计要点：
//  1. 使用独立的 http.ServeMux，不污染全局 DefaultServeMux；
//  2. **先 net.Listen 再 Serve**：绑定失败必须让启动失败，而不是在后台
//     goroutine 里记一条日志然后当作没事发生；
//  3. 与业务 engine 完全隔离，不经过任何业务中间件（无日志噪音、无超时干扰）；
//  4. block/mutex 采样率**只在 listener 已绑定之后**才开启——绑定失败时端点
//     根本不存在，却让整个进程持续承担采样开销是纯粹的浪费。
//
// 未启用时返回 (nil, nil)，调用方须判空。
func startPprofServer(cfg config.PprofConfig) (*pprofHandle, error) {
	if !cfg.Enabled {
		logger.Info("pprof endpoint disabled")
		return nil, nil //nolint:nilnil // 未启用是正常状态，不是错误
	}

	host := cfg.Host
	if host == "" {
		host = defaultPprofHost
	}
	port := cfg.Port
	if port == "" {
		port = defaultPprofPort
	}
	addr := net.JoinHostPort(host, port)

	// 先绑定。失败即启动失败：一个"配置说开了但实际没监听"的端点，
	// 会让排障的人对着一个不存在的地址反复尝试。
	listener, err := net.Listen("tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("bind pprof endpoint on %s: %w", addr, err)
	}

	// 采样率在绑定成功之后才开启，并由 handle 负责清零。
	// 这两项是**进程全局状态**，端点消失后不清零就等于白白付出采样开销。
	if cfg.BlockProfileRate > 0 {
		runtime.SetBlockProfileRate(cfg.BlockProfileRate)
		logger.Infof("pprof block profile enabled, rate=%d", cfg.BlockProfileRate)
	}
	if cfg.MutexProfileFraction > 0 {
		runtime.SetMutexProfileFraction(cfg.MutexProfileFraction)
		logger.Infof("pprof mutex profile enabled, fraction=%d", cfg.MutexProfileFraction)
	}

	h := &pprofHandle{
		server: &http.Server{
			Handler: newPprofMux(),
			// 不设 WriteTimeout：CPU/trace 采集是长连接（profile?seconds=30 会
			// 持续 30 秒），写超时会导致采集被中途掐断。
			ReadHeaderTimeout: pprofReadHeaderTimeout,
		},
		listener: listener,
		errCh:    make(chan error, 1),
	}

	go func() {
		// 打**实际绑定地址**而不是配置值：端口配 0 时由内核分配，
		// 打配置值会得到一条 `http://127.0.0.1:0/...` 的无用日志。
		logger.Infof("pprof endpoint starting on http://%s/debug/pprof/", listener.Addr())
		serveErr := h.server.Serve(listener)
		if errors.Is(serveErr, http.ErrServerClosed) {
			serveErr = nil
		}
		if serveErr != nil {
			// 运行期异常退出：标记状态并清零采样，端点已经不在了。
			h.exited.Store(true)
			h.cleanup()
			logger.Errorf("pprof endpoint exited: %v", serveErr)
		}
		h.errCh <- serveErr
	}()

	return h, nil
}

// newPprofMux 构造独立的 pprof 路由表。
//
// 手动注册而不是依赖 DefaultServeMux：net/http/pprof 的 init 会把 handler
// 挂进全局 mux，业务 engine 若也用了它，pprof 就会暴露在业务端口上。
func newPprofMux() *http.ServeMux {
	mux := http.NewServeMux()
	// pprof.Index 处理 /debug/pprof/ 下的 heap、goroutine、allocs、block、
	// mutex、threadcreate 等子路径；profile/cmdline/symbol/trace 是特殊
	// handler，必须单独注册。
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile) // CPU profile
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace) // 执行跟踪

	return mux
}

// Addr 返回实际监听地址（端口配 0 时由内核分配，此时它与配置值不同）。
func (h *pprofHandle) Addr() string {
	if h == nil || h.listener == nil {
		return ""
	}
	return h.listener.Addr().String()
}

// Exited 报告 pprof 端点是否已在运行期异常退出。
//
// 用于把它标记为 degraded 的可选能力：主业务继续服务，
// 但 readiness 要说清楚"这台机器上没有 pprof 了"。
func (h *pprofHandle) Exited() bool {
	return h != nil && h.exited.Load()
}

// Shutdown 优雅关闭端点并清零采样率。
//
// 关停错误进入调用方的聚合错误：pprof 是可选能力，它的关停失败不该升级为
// 全进程失败，但也不能悄悄吞掉——那会让"关停不干净"永远没人知道。
func (h *pprofHandle) Shutdown(ctx context.Context) error {
	if h == nil {
		return nil
	}
	defer h.cleanup()

	shutdownErr := h.server.Shutdown(ctx)

	return errors.Join(shutdownErr, h.awaitServe(ctx))
}

// awaitServe 等 Serve 返回并**缓存**结果。
//
// 缓存是必需的：Serve 的结果只会从 channel 里出现一次，而 Shutdown 可能被
// 调用多次（正常关停、启动回滚）。不缓存的话第二次调用会在空 channel 上
// 等到预算耗尽——一次本该立刻返回的重复关停变成一段几秒的无谓阻塞。
func (h *pprofHandle) awaitServe(ctx context.Context) error {
	h.serveOnce.Do(func() {
		select {
		case h.serveErr = <-h.errCh:
		case <-ctx.Done():
			h.serveErr = fmt.Errorf(
				"pprof endpoint did not stop within the shutdown budget: %w", ctx.Err())
		}
	})
	return h.serveErr
}

// cleanup 清零 block/mutex 采样率，恰好一次。
//
// 三条路径都必须走到它：运行期异常退出、正常关停、启动回滚。
// 只关 server 不清零的话，一个已经 degraded 的可选端点会让整个进程
// 持续承担采样开销——而那笔开销在任何监控上都只表现为"莫名其妙的性能下降"。
func (h *pprofHandle) cleanup() {
	if h == nil {
		return
	}
	h.cleanupOnce.Do(func() {
		runtime.SetBlockProfileRate(0)
		runtime.SetMutexProfileFraction(0)
	})
}
