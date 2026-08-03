package bootstrap

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"
	"time"

	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/runtime/resource"

	"github.com/gin-gonic/gin"
	logger "github.com/gtkit/logger/v2"
)

const (
	defaultReadHeaderTimeout = 10 * time.Second
	// defaultReadTimeout 是读取「整个请求（头+体）」的上限，约束慢 body / slowloris。
	// 独立于 ReadHeaderTimeout；比它宽松，给正常 body 上传留足时间。
	defaultReadTimeout     = 60 * time.Second
	defaultIdleTimeout     = 120 * time.Second
	defaultShutdownTimeout = 10 * time.Second
)

// serverWriteTimeout 恒为 0：**服务器层不施加隐式的绝对写上限**。
//
// 它不是"关掉保护"，而是把保护从进程级挪到路由级：
//
//	http.Server.WriteTimeout 是所有路由共享的一个值，而流式路由需要它足够长、
//	普通路由需要它足够短——两者不可能同时满足。旧实现的做法是：配了 LLM provider
//	就把**全局** WriteTimeout 抬到 `LLM timeout + 5s`，于是流式能跑了，
//	代价是所有非流式路由同时失去写超时保护，而这个副作用在配置里完全看不出来。
//
// 定稿是 `WriteTimeout = 0` + 每条路由经 TimeoutPolicy 显式设置连接级写 deadline
// （ResponseController.SetWriteDeadline 是**覆盖**，不受全局值压制）。
//
// **它的安全性完全依赖 routespec 的注册期门禁**：任何路由缺 TimeoutPolicy 即启动失败。
// 那条门禁一旦被放宽，这里的 0 就是纯粹的保护缺失，两者必须同进同退。
const serverWriteTimeout = 0

type httpServer interface {
	// Serve 在**已绑定**的监听器上提供服务。
	//
	// 只暴露 Serve 而不是 ListenAndServe：端口绑定与 TLS 校验必须在
	// worker 启动屏障**之前**完成，否则一个端口占用会发生在
	// required one-shot 的不可逆副作用之后。
	Serve(l net.Listener) error
	ServeTLS(l net.Listener, certFile, keyFile string) error
	Shutdown(ctx context.Context) error
	// Close 立即关闭监听与全部连接，不等在途请求。
	// 强制关停路径（第二个信号）与 Shutdown 超时后都要用它，
	// 否则一个挂死的 handler 能让进程在"优雅关停"里无限滞留。
	Close() error
}

// HTTPConfig wraps bootstrap HTTP server configuration.
type HTTPConfig struct {
	Host              string
	Port              string
	EnableTLS         bool
	CertFile          string
	KeyFile           string
	TLSConfig         *tls.Config
	ReadHeaderTimeout time.Duration
	ReadTimeout       time.Duration
	WriteTimeout      time.Duration
	IdleTimeout       time.Duration
	ShutdownTimeout   time.Duration
}

func newHTTPConfig(cfg *config.Config) HTTPConfig {
	readHeaderTimeout := defaultReadHeaderTimeout
	if cfg.App.ReadTimeoutSeconds > 0 {
		readHeaderTimeout = time.Duration(cfg.App.ReadTimeoutSeconds) * time.Second
	}

	return HTTPConfig{
		Host:              cfg.App.Host,
		Port:              cfg.App.Port,
		EnableTLS:         cfg.App.EnableHTTPS,
		CertFile:          resource.ResolvePath(cfg.SSL.PEM),
		KeyFile:           resource.ResolvePath(cfg.SSL.Key),
		TLSConfig:         newTLSConfig(),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       defaultReadTimeout,
		WriteTimeout:      serverWriteTimeout,
		IdleTimeout:       defaultIdleTimeout,
		ShutdownTimeout:   defaultShutdownTimeout,
	}
}

func newHTTPServer(engine *gin.Engine, cfg HTTPConfig) httpServer {
	return &http.Server{
		Addr:              net.JoinHostPort(cfg.Host, cfg.Port),
		Handler:           engine,
		TLSConfig:         cfg.TLSConfig,
		TLSNextProto:      make(map[string]func(*http.Server, *tls.Conn, http.Handler)),
		ReadHeaderTimeout: cfg.ReadHeaderTimeout,
		ReadTimeout:       cfg.ReadTimeout,
		WriteTimeout:      cfg.WriteTimeout,
		IdleTimeout:       cfg.IdleTimeout,
		// MaxHeaderBytes 必须显式设置。
		//
		// net/http 的默认值是 1 MiB——单个连接就能让服务端为请求头分配 1 MiB，
		// 而请求头在被解析完之前不受任何业务中间件约束（MaxBytesLimit 只管 body）。
		// 几千个并发连接即可把内存吃光，且访问日志里什么都看不到，
		// 因为这些请求根本没走到 handler。
		MaxHeaderBytes: defaultMaxHeaderBytes,
	}
}

// defaultMaxHeaderBytes 是请求头（含请求行）的总上限。
//
// 64 KiB 远超任何正常请求：常见的大头是 Cookie 与 Authorization，
// 二者相加通常在 8 KiB 以内。
const defaultMaxHeaderBytes = 64 << 10

// serveHTTPServer 在预绑定的监听器上开始服务。
//
// 只走 Serve/ServeTLS：ListenAndServe 会自己绑端口，那就把绑定失败推迟到了
// 启动屏障之后——而屏障之前的 required one-shot 可能已经产生不可逆副作用。
func serveHTTPServer(server httpServer, cfg HTTPConfig, listener net.Listener) error {
	addr := listener.Addr().String()

	if cfg.EnableTLS {
		logger.Infof("https server starting on %s", addr)
		// 证书已在预绑定阶段校验过；这里再传一次路径由 net/http 完成实际装载。
		return normalizeServeError(server.ServeTLS(listener, cfg.CertFile, cfg.KeyFile))
	}

	logger.Infof("http server starting on %s", addr)
	return normalizeServeError(server.Serve(listener))
}

func shutdownHTTPServer(ctx context.Context, server httpServer, cfg HTTPConfig) error {
	shutdownCtx, cancel := context.WithTimeout(ctx, cfg.ShutdownTimeout)
	defer cancel()

	logger.Info("http server shutting down")
	return normalizeServeError(server.Shutdown(shutdownCtx))
}

func normalizeServeError(err error) error {
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		return nil
	}

	return err
}

func newTLSConfig() *tls.Config {
	return &tls.Config{
		MinVersion:       tls.VersionTLS12,
		CurvePreferences: []tls.CurveID{tls.X25519, tls.CurveP256},
		CipherSuites: []uint16{
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
		},
	}
}
