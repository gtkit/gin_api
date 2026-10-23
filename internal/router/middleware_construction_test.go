package router

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gtkit/gin-api/internal/middleware"
	"github.com/gtkit/gin-api/internal/pkg/metrics"
	"github.com/gtkit/logger/v2"
	"go.opentelemetry.io/otel"
)

func TestRouterReturnsRequestIDConstructionFailure(t *testing.T) {
	root := errors.New("random source failed")
	for _, tc := range []struct {
		name    string
		factory func() (*middleware.RequestIDGenerator, error)
		want    error
	}{
		{name: "factory error", factory: func() (*middleware.RequestIDGenerator, error) { return nil, root }, want: root},
		{name: "nil generator", factory: func() (*middleware.RequestIDGenerator, error) { return nil, nil }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine, err := newRouterForTest(t, "test", Options{RequestIDFactory: tc.factory})
			if engine != nil || err == nil || (tc.want != nil && !errors.Is(err, tc.want)) {
				t.Fatalf("New = (%v, %v), want no engine and construction failure %v", engine, err, tc.want)
			}
		})
	}
}

func TestRouterRequiresFinalMetricsBeforeConstructingRequestID(t *testing.T) {
	calls := 0
	engine, err := New("test", Options{RequestIDFactory: func() (*middleware.RequestIDGenerator, error) {
		calls++
		return middleware.NewRequestIDGenerator()
	}})
	if engine != nil || err == nil || calls != 0 {
		t.Fatalf("New = (%v, %v), factory calls = %d; want metrics rejection before construction", engine, err, calls)
	}
}

func TestRouterConstructsRequestIDForEachEngine(t *testing.T) {
	recorder, err := metrics.New(otel.Meter("middleware-construction"))
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	access := logger.MustNew(logger.WithConsole(true), logger.WithFile(false), logger.WithReplaceGlobals(false))
	t.Cleanup(access.Sync)
	opt := Options{AccessLogger: access, Metrics: recorder, RequestIDFactory: func() (*middleware.RequestIDGenerator, error) {
		calls++
		return middleware.NewRequestIDGenerator()
	}}
	for range 2 {
		engine, err := New("test", opt)
		if err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/health", nil))
		if response.Code != http.StatusOK || response.Header().Get("X-Request-ID") == "" {
			t.Fatalf("health response = %d, headers = %v", response.Code, response.Header())
		}
	}
	if calls != 2 {
		t.Fatalf("factory calls = %d, want one per engine", calls)
	}
}

func TestRouterCopiesAllowedOrigins(t *testing.T) {
	origins := []string{"https://example.com"}
	engine, err := newRouterForTest(t, "test", Options{AllowedOrigins: origins})
	if err != nil {
		t.Fatal(err)
	}
	origins[0] = "https://changed.invalid"
	for _, tc := range []struct {
		origin     string
		wantOrigin string
	}{
		{"https://example.com", "https://example.com"},
		{"https://changed.invalid", ""},
	} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/health", nil)
		req.Header.Set("Origin", tc.origin)
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, req)
		if response.Code != http.StatusOK || response.Header().Get("Access-Control-Allow-Origin") != tc.wantOrigin {
			t.Fatalf("origin %q: status = %d, allowed origin = %q, want %q", tc.origin, response.Code, response.Header().Get("Access-Control-Allow-Origin"), tc.wantOrigin)
		}
	}
}

func TestRouterRejectsMissingAccessBeforeRequestIDConstruction(t *testing.T) {
	recorder, err := metrics.New(otel.Meter("missing-access"))
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	engine, err := New("test", Options{Metrics: recorder, RequestIDFactory: func() (*middleware.RequestIDGenerator, error) {
		calls++
		return middleware.NewRequestIDGenerator()
	}})
	if engine != nil || err == nil || calls != 0 {
		t.Fatalf("engine=%v err=%v factory calls=%d", engine, err, calls)
	}
}
