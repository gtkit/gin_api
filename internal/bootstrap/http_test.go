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
			Host:                "127.0.0.1",
			Port:                "8008",
			EnableHTTPS:         true,
			ReadTimeoutSeconds:  3,
			WriteTimeoutSeconds: 4,
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
	if cfg.WriteTimeout != 4*time.Second {
		t.Fatalf("WriteTimeout = %s", cfg.WriteTimeout)
	}
}

func TestNewHTTPConfigKeepsLongerConfiguredWriteTimeout(t *testing.T) {
	cfg := newHTTPConfig(&config.Config{
		App: config.AppConfig{
			Host:                "127.0.0.1",
			Port:                "8008",
			WriteTimeoutSeconds: 90,
		},
		LLM: config.LLMConfig{
			Providers: []config.LLMProviderConfig{
				{Name: "deepseek", APIKey: "k"},
			},
			TimeoutSeconds: 60,
		},
	})

	if cfg.WriteTimeout != 90*time.Second {
		t.Fatalf("WriteTimeout = %s", cfg.WriteTimeout)
	}
}

func TestNewHTTPConfigRaisesWriteTimeoutForLLMStreaming(t *testing.T) {
	cfg := newHTTPConfig(&config.Config{
		App: config.AppConfig{
			Host:                "127.0.0.1",
			Port:                "8008",
			WriteTimeoutSeconds: 4,
		},
		LLM: config.LLMConfig{
			Providers: []config.LLMProviderConfig{
				{Name: "deepseek", APIKey: "k"},
			},
			TimeoutSeconds: 10,
		},
	})

	if cfg.WriteTimeout != 15*time.Second {
		t.Fatalf("WriteTimeout = %s", cfg.WriteTimeout)
	}
}
