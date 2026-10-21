package bootstrap

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/runtime/resource"

	"github.com/gin-gonic/gin"
	logger "github.com/gtkit/gin-api/internal/pkg/log"
)

const (
	defaultReadHeaderTimeout = 10 * time.Second
	// defaultReadTimeout 是读取「整个请求（头+体）」的上限，约束慢 body / slowloris。
	// 独立于 ReadHeaderTimeout；比它宽松，给正常 body 上传留足时间。
	defaultReadTimeout     = 60 * time.Second
	defaultIdleTimeout     = 120 * time.Second
	defaultShutdownTimeout = 10 * time.Second
)

// serverWriteTimeout 是服务级写超时**兜底**（http-lifecycle spec）。
//
// 曾经恒为 0，安全性依赖"每条路由必须声明 TimeoutPolicy"的 routespec
// 注册期门禁——那条门禁已随路由 DSL 移除，0 就成了纯粹的保护缺失：
// 新增路由忘挂超时中间件就永久没有写超时，且没有任何机制会发现。
//
// 现行契约是"保守兜底 + 路由覆盖"：
//
//	普通路由受本值保护（遗忘的代价从"无界挂起"降为"保守截断"）；
//	需要长时间写出的流式路由在 handler 内经
//	http.ResponseController.SetWriteDeadline 显式延长——每响应 deadline
//	**覆盖**服务器全局值，"我需要长写"因此是一次显式决策而不是默认状态。
//
// 取值与 ReadTimeout 同量级：兜底要宽到不误伤慢速大响应，紧到能回收死连接。
const serverWriteTimeout = 60 * time.Second

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
	// 两项分别取值。合并成一个键时，只有读头受配置控制，而读 body 用固定默认值——
	// 于是配置里的数字与实际暴露面差了一个数量级，且从配置上完全看不出来。
	readHeaderTimeout := defaultReadHeaderTimeout
	if cfg.App.ReadHeaderTimeoutSeconds > 0 {
		readHeaderTimeout = time.Duration(cfg.App.ReadHeaderTimeoutSeconds) * time.Second
	}
	readTimeout := defaultReadTimeout
	if cfg.App.ReadTimeoutFullSeconds > 0 {
		readTimeout = time.Duration(cfg.App.ReadTimeoutFullSeconds) * time.Second
	}

	return HTTPConfig{
		Host:              cfg.App.Host,
		Port:              cfg.App.Port,
		EnableTLS:         cfg.App.EnableHTTPS,
		CertFile:          resource.ResolvePath(cfg.SSL.PEM),
		KeyFile:           resource.ResolvePath(cfg.SSL.Key),
		TLSConfig:         newTLSConfig(),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      serverWriteTimeout,
		IdleTimeout:       defaultIdleTimeout,
		ShutdownTimeout:   defaultShutdownTimeout,
	}
}

func newHTTPServer(engine *gin.Engine, cfg HTTPConfig) httpServer {
	return &http.Server{
		Addr:      net.JoinHostPort(cfg.Host, cfg.Port),
		Handler:   engine,
		TLSConfig: cfg.TLSConfig,
		// **不设 TLSNextProto**：留空让标准库按默认启用 HTTP/2 协商。
		//
		// 此前这里被设成空 map，显式关闭了协商，且没有记录任何理由。
		// 历史上确有 WriteTimeout 与 HTTP/2 交互不良的情形（Go 1.8 时代的
		// 已修缺陷）；当前标准库对 HTTP/2 正常尊重 WriteTimeout，
		// 服务级兜底 + 流式路由每响应覆盖的契约在两个协议版本下一致。
		//
		// 禁用协商的影响是全局且不可见的：所有客户端退回到较旧的协议版本，
		// 而没人会收到任何提示。要重新禁用，必须在此写明具体理由。
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
		ErrorLog:       httpServerErrorLog(),
	}
}

// httpServerErrorLog 把 net/http 的内部错误输出桥接到结构化日志器。
//
// 这些错误（TLS 握手失败、请求解析错误、连接异常）发生在任何中间件之前，
// 访问日志里没有它们的痕迹；不接管的话唯一的第一现场落在裸 stderr 上，
// 采集、告警与脱敏对它全部失效。
func httpServerErrorLog() *log.Logger {
	return log.New(httpErrorWriter{}, "", 0)
}

type httpErrorWriter struct{}

func (httpErrorWriter) Write(message []byte) (int, error) {
	logger.Error(strings.TrimSuffix(string(message), "\n"))
	return len(message), nil
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
		// 只用预绑定阶段已装载并校验的证书（经 TLSConfig.Certificates 注入）。
		// 空路径调用杜绝二次装载与"校验-使用"之间的文件变更窗口；
		// 证书缺失时报错而不是回退路径装载——回退会让校验形同虚设。
		if cfg.TLSConfig == nil || len(cfg.TLSConfig.Certificates) == 0 {
			return fmt.Errorf(
				"tls enabled on %s but no certificate loaded: the validated certificate from prebind was not injected", addr)
		}
		logger.Infof("https server starting on %s", addr)
		return normalizeServeError(server.ServeTLS(listener, "", ""))
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
