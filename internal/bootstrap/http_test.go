package bootstrap

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/platform/config"
)

func TestNewHTTPConfigResolvesTLSFilesFromProjectRoot(t *testing.T) {
	cfg := newHTTPConfig(&config.Config{
		App: config.AppConfig{
			Host:                     "127.0.0.1",
			Port:                     "8008",
			EnableHTTPS:              true,
			ReadHeaderTimeoutSeconds: 3,
			ReadTimeoutFullSeconds:   45,
		},
		SSL: config.SSLConfig{
			PEM: "config/pem/jwtpub.pem",
			Key: "config/pem/jwtpri.pem",
		},
	})

	if !filepath.IsAbs(cfg.CertFile) || !strings.HasSuffix(filepath.Clean(cfg.CertFile), filepath.Join("config", "pem", "jwtpub.pem")) {
		t.Fatalf("CertFile = %q", cfg.CertFile)
	}
	if !filepath.IsAbs(cfg.KeyFile) || !strings.HasSuffix(filepath.Clean(cfg.KeyFile), filepath.Join("config", "pem", "jwtpri.pem")) {
		t.Fatalf("KeyFile = %q", cfg.KeyFile)
	}
	if cfg.ReadHeaderTimeout != 3*time.Second {
		t.Fatalf("ReadHeaderTimeout = %s", cfg.ReadHeaderTimeout)
	}
}

// TestServerWriteTimeoutHasConservativeDefault 钉住服务级写超时兜底
// （http-lifecycle spec）：写超时保护不依赖任何注册期门禁的存在——
// 曾经的 0 依赖 routespec"每条路由必须声明 TimeoutPolicy"的门禁，
// 门禁随路由 DSL 移除后，0 就是纯粹的保护缺失：新增路由忘挂超时中间件
// 就永久没有写超时。流式路由经每响应 SetWriteDeadline 覆盖兜底值。
func TestServerWriteTimeoutHasConservativeDefault(t *testing.T) {
	cfg := &config.Config{
		App: config.AppConfig{Host: "127.0.0.1", Port: "8008"},
	}
	if got := newHTTPConfig(cfg).WriteTimeout; got != serverWriteTimeout || got <= 0 {
		t.Fatalf("WriteTimeout = %s, want the conservative fallback %s", got, serverWriteTimeout)
	}
}
