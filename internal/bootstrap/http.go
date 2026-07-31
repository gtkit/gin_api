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
	defaultReadTimeout       = 60 * time.Second
	defaultWriteTimeout      = 10 * time.Second
	defaultIdleTimeout       = 120 * time.Second
	defaultShutdownTimeout   = 10 * time.Second
	streamWriteTimeoutBuffer = 5 * time.Second
)

type httpServer interface {
	ListenAndServe() error
	ListenAndServeTLS(certFile, keyFile string) error
	Shutdown(ctx context.Context) error
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

	writeTimeout := defaultWriteTimeout
	if cfg.App.WriteTimeoutSeconds > 0 {
		writeTimeout = time.Duration(cfg.App.WriteTimeoutSeconds) * time.Second
	}
	writeTimeout = effectiveWriteTimeout(cfg, writeTimeout)

	return HTTPConfig{
		Host:              cfg.App.Host,
		Port:              cfg.App.Port,
		EnableTLS:         cfg.App.EnableHTTPS,
		CertFile:          resource.ResolvePath(cfg.SSL.PEM),
		KeyFile:           resource.ResolvePath(cfg.SSL.Key),
		TLSConfig:         newTLSConfig(),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       defaultReadTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       defaultIdleTimeout,
		ShutdownTimeout:   defaultShutdownTimeout,
	}
}

func effectiveWriteTimeout(cfg *config.Config, current time.Duration) time.Duration {
	if cfg == nil {
		return current
	}

	if len(cfg.LLM.Providers) == 0 {
		return current
	}

	llmTimeout := defaultWriteTimeout
	if cfg.LLM.TimeoutSeconds > 0 {
		llmTimeout = time.Duration(cfg.LLM.TimeoutSeconds) * time.Second
	}

	minimum := llmTimeout + streamWriteTimeoutBuffer
	if current < minimum {
		return minimum
	}

	return current
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
	}
}

func serveHTTPServer(server httpServer, cfg HTTPConfig) error {
	addr := net.JoinHostPort(cfg.Host, cfg.Port)

	if cfg.EnableTLS {
		logger.Infof("https server starting on %s", addr)
		return normalizeServeError(server.ListenAndServeTLS(cfg.CertFile, cfg.KeyFile))
	}

	logger.Infof("http server starting on %s", addr)
	return normalizeServeError(server.ListenAndServe())
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
