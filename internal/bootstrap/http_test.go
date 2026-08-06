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

// TestServerWriteTimeoutIsAlwaysZero 钉住"服务器层不施加隐式写上限"。
//
// 这条断言与 routespec 的注册期门禁（任何路由缺 TimeoutPolicy 即启动失败）
// 是一对：单独看它像是关掉了保护，实际是把保护从"所有路由共享一个值"
// 挪到了"每条路由各自声明"。两者必须同进同退——门禁一旦被放宽，
// 这里的 0 就变成纯粹的保护缺失。
//
// 旧实现按 LLM 配置抬高全局值：流式因此能跑，代价是所有非流式路由
// 同时失去写超时保护，而这个副作用在配置里完全看不出来。
func TestServerWriteTimeoutIsAlwaysZero(t *testing.T) {
	cfg := &config.Config{
		App: config.AppConfig{Host: "127.0.0.1", Port: "8008"},
	}
	if got := newHTTPConfig(cfg).WriteTimeout; got != 0 {
		t.Fatalf("WriteTimeout = %s, want 0: the server must not impose an implicit ceiling", got)
	}
}
