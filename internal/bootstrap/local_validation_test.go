package bootstrap

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gtkit/gin-api/internal/testutil"
)

func TestRuntimeRejectsLocalCredentialsBeforeOpeningResources(t *testing.T) {
	for _, kind := range []string{"jwt", "tls"} {
		t.Run(kind, func(t *testing.T) {
			f := newLifecycleFixture(t)
			cfg := f.components[0].cfg
			missing := filepath.Join(t.TempDir(), "missing.pem")
			if kind == "jwt" {
				cfg.JWT.Active.PublicKeyPath = missing
			} else {
				cfg.App.EnableHTTPS = true
				cfg.SSL.PEM, cfg.SSL.Key = missing, missing
			}
			r, err := NewRuntime(t.Context(), f.options())
			if r != nil {
				if closeErr := r.Close(t.Context()); closeErr != nil {
					t.Error(closeErr)
				}
			}
			if err == nil || !slices.Equal(f.events, []string{"open:configuration"}) {
				t.Fatalf("invalid %s credentials: err=%v resources=%v", kind, err, f.events)
			}
		})
	}
}

func TestRuntimeKeepsValidatedTLSMaterialForHTTP(t *testing.T) {
	testutil.RequireLoopback(t)
	f := newLifecycleFixture(t)
	cfg := f.components[0].cfg
	cfg.App.Host, cfg.App.Port, cfg.App.EnableHTTPS = "127.0.0.1", "0", true
	cfg.SSL.PEM, cfg.SSL.Key = writeSelfSignedCert(t, time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour))
	r, err := NewRuntime(t.Context(), f.options())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := r.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	})
	for _, path := range []string{cfg.SSL.PEM, cfg.SSL.Key} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	inf, err := resolveInfra(r)
	if err != nil {
		t.Fatal(err)
	}
	bound, err := prebindHTTP(inf.httpConfig)
	if err != nil {
		t.Fatalf("HTTP reloaded certificate paths instead of using validated material: %v", err)
	}
	if err := bound.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPrebindRechecksCachedCertificateExpiry(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		certPath, keyPath := writeSelfSignedCert(t, time.Now().Add(-time.Hour), time.Now().Add(time.Minute))
		cfg := HTTPConfig{Host: "127.0.0.1", Port: "invalid", EnableTLS: true,
			CertFile: certPath, KeyFile: keyPath, TLSConfig: newTLSConfig()}
		cert, err := loadAndVerifyCertificate(cfg)
		if err != nil {
			t.Fatal(err)
		}
		cfg.TLSConfig.Certificates = append(cfg.TLSConfig.Certificates, *cert)
		time.Sleep(2 * time.Minute)
		bound, err := prebindHTTP(cfg)
		if bound != nil {
			if closeErr := bound.Close(); closeErr != nil {
				t.Error(closeErr)
			}
		}
		if err == nil || !strings.Contains(err.Error(), "certificate expired") {
			t.Fatalf("cached certificate was not revalidated before binding: %v", err)
		}
	})
}
