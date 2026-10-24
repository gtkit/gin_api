package router

import (
	"context"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/gtkit/gin-api/internal/pkg/metrics"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/platform/logging"
)

func TestPanicRequestObservability(t *testing.T) {
	for _, tc := range []struct {
		name      string
		committed bool
		want      int
	}{
		{"success", false, http.StatusOK},
		{"panic before response", false, http.StatusInternalServerError},
		{"panic after response", true, http.StatusAccepted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			owner, err := logging.Open(t.Context(), logging.Options{Config: &config.Config{Log: config.LogConfig{Path: filepath.Join(dir, "request"), Level: "info", FileStdout: 1}}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := owner.Close(context.WithoutCancel(t.Context())); err != nil {
					t.Error(err)
				}
			})

			reader := sdkmetric.NewManualReader()
			provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
			t.Cleanup(func() {
				if err := provider.Shutdown(context.WithoutCancel(t.Context())); err != nil {
					t.Errorf("provider.Shutdown(context.WithoutCancel(t.Context())): %v", err)
				}
			})
			recorder, err := metrics.New(provider.Meter("panic-contract"))
			if err != nil {
				t.Fatal(err)
			}
			engine, err := newRouterForTest(t, "test", Options{AccessLogger: owner.AccessLogger(), Metrics: recorder, RegisterModules: func(v1 *gin.RouterGroup) {
				v1.GET("/observe", func(c *gin.Context) {
					if tc.name == "success" {
						c.String(http.StatusOK, "ok")
						return
					}
					if tc.committed {
						c.String(http.StatusAccepted, "started")
					}
					panic("contract panic")
				})
			}})
			if err != nil {
				t.Fatal(err)
			}
			w := httptest.NewRecorder()
			engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/observe", nil))
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d", w.Code, tc.want)
			}
			var data metricdata.ResourceMetrics
			if err := reader.Collect(t.Context(), &data); err != nil {
				t.Fatal(err)
			}
			var count int64
			for _, scope := range data.ScopeMetrics {
				for _, m := range scope.Metrics {
					if m.Name != "http.server.requests" {
						continue
					}
					sum, ok := m.Data.(metricdata.Sum[int64])
					if !ok {
						t.Fatalf("panic metric data type %T", m.Data)
					}
					for _, point := range sum.DataPoints {
						status, _ := point.Attributes.Value("http.response.status_code")
						if status.AsInt64() != int64(tc.want) {
							t.Fatalf("metric status = %v", status)
						}
						count += point.Value
					}
				}
			}
			if count != 1 {
				t.Fatalf("request count = %d, want 1", count)
			}
			if err := owner.Drain(t.Context()); err != nil {
				t.Fatal(err)
			}
			var logs strings.Builder
			if err := filepath.WalkDir(dir, func(file string, entry fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if entry.IsDir() {
					return nil
				}
				data, err := os.ReadFile(file)
				if err != nil {
					return err
				}
				_, err = logs.Write(data)
				return err
			}); err != nil {
				t.Fatal(err)
			}
			if got := strings.Count(logs.String(), "\t/api/v1/observe\t"); got != 1 {
				t.Fatalf("access entries = %d, logs=%s", got, logs.String())
			}
		})
	}
}

func TestReadinessKeepsPrivateCauseInRateLimitedLogs(t *testing.T) {
	dir := t.TempDir()
	owner, err := logging.Open(t.Context(), logging.Options{Config: &config.Config{Log: config.LogConfig{
		Path: filepath.Join(dir, "readiness"), Level: "info", FileStdout: 1,
	}}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owner.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	})
	const private = "private-readiness-user@10.0.3.12"
	checker := &stubChecker{dbErr: errors.New(private)}
	engine := newTestEngine(t, checker)
	for range 2 {
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ready", nil))
		if w.Code != http.StatusServiceUnavailable || strings.Contains(w.Body.String(), private) {
			t.Fatalf("unsafe readiness response: %d %s", w.Code, w.Body.String())
		}
	}
	checker.dbErr = nil
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/ready", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("recovered readiness = %d", w.Code)
	}
	if err := owner.Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	var logs strings.Builder
	if err := filepath.WalkDir(dir, func(path string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		_, err = logs.Write(data)
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if strings.Count(logs.String(), private) != 1 || strings.Count(logs.String(), "readiness dependency recovered") != 1 {
		t.Fatalf("missing cause, rate limit or recovery log: %s", logs.String())
	}
}
