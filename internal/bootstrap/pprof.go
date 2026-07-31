package bootstrap

import (
	"context"
	"net"
	"net/http"
	"net/http/pprof"
	"runtime"
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

// startPprofServer 根据配置启动一个独立的 pprof HTTP 端点。
//
// 设计要点：
//  1. 使用独立的 http.ServeMux，不污染全局 DefaultServeMux；
//  2. 默认只绑 127.0.0.1，外部无法直连，线上通过 SSH 端口转发访问；
//  3. 与业务 engine 完全隔离，不经过任何业务中间件（无日志噪音、无超时干扰）。
//
// 返回一个 shutdown 函数：调用它会优雅关闭 pprof 端点。
// 未启用（cfg.Enabled == false）时返回一个空操作函数，调用方无需做 nil 判断。
func startPprofServer(cfg config.PprofConfig) func(context.Context) error {
	// 空操作关闭函数：未启用时返回它，简化调用方逻辑。
	noop := func(context.Context) error { return nil }

	if !cfg.Enabled {
		logger.Info("pprof endpoint disabled")
		return noop
	}

	// 按需开启 block / mutex 采样。这两类采样有运行时开销，
	// 因此默认关闭（采样率为 0），仅在配置显式打开时生效。
	if cfg.BlockProfileRate > 0 {
		runtime.SetBlockProfileRate(cfg.BlockProfileRate)
		logger.Infof("pprof block profile enabled, rate=%d", cfg.BlockProfileRate)
	}
	if cfg.MutexProfileFraction > 0 {
		runtime.SetMutexProfileFraction(cfg.MutexProfileFraction)
		logger.Infof("pprof mutex profile enabled, fraction=%d", cfg.MutexProfileFraction)
	}

	// 兜底默认值：未配置时绑回环 + 6060。
	host := cfg.Host
	if host == "" {
		host = defaultPprofHost
	}
	port := cfg.Port
	if port == "" {
		port = defaultPprofPort
	}
	addr := net.JoinHostPort(host, port)

	// 手动把 net/http/pprof 的 handler 注册到独立 mux。
	// pprof.Index 处理 /debug/pprof/ 下的 heap、goroutine、allocs、block、
	// mutex、threadcreate 等子路径；profile/cmdline/symbol/trace 是特殊
	// handler，必须单独注册。
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile) // CPU profile
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace) // 执行跟踪

	server := &http.Server{
		Addr:    addr,
		Handler: mux,
		// 不设 WriteTimeout：CPU/trace 采集是长连接（profile?seconds=30 会
		// 持续 30 秒），写超时会导致采集被中途掐断。这也是不挂业务 engine 的原因之一。
		ReadHeaderTimeout: pprofReadHeaderTimeout,
	}

	go func() {
		logger.Infof("pprof endpoint starting on http://%s/debug/pprof/", addr)
		// ListenAndServe 在 server 关闭时返回 http.ErrServerClosed，属正常关闭。
		if err := server.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			logger.Errorf("pprof endpoint exited: %v", err)
		}
	}()

	// 返回优雅关闭函数：交给调用方在进程退出时调用。
	return server.Shutdown
}
