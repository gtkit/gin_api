package httpclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/logger/v2"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

type correlationTransport struct {
	roundTrip func(*http.Request) (*http.Response, error)
	closed    atomic.Int32
}

func (t *correlationTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return t.roundTrip(r)
}

func (t *correlationTransport) CloseIdleConnections() { t.closed.Add(1) }

func correlationContext(ctx context.Context, id string) context.Context {
	state, _ := trace.ParseTraceState("vendor=value")
	ctx = trace.ContextWithSpanContext(ctx, trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1, 2, 3}, SpanID: trace.SpanID{4, 5, 6},
		TraceFlags: trace.FlagsSampled, TraceState: state,
	}))
	return logger.ContextWithRequestID(ctx, id)
}

func TestPropagationPreservesOriginalRequestAndContext(t *testing.T) {
	t.Parallel()
	cause := errors.New("transport failure")
	ctx, cancel := context.WithCancel(correlationContext(t.Context(), "internal-id"))
	t.Cleanup(cancel)
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "http://downstream.test/", nil)
	req.Header.Set("X-Request-ID", "explicit-id")
	req.Header.Set("traceparent", "old-trace")
	req.Header.Set("tracestate", "old=value")
	req.Header.Set("Authorization", "explicit-auth")
	base := &correlationTransport{roundTrip: func(got *http.Request) (*http.Response, error) {
		if got == req || got.Context() != ctx || got.Body != req.Body {
			t.Fatal("request ownership or context changed")
		}
		if got.Header.Get("X-Request-ID") != "internal-id" || got.Header.Get("Authorization") != "explicit-auth" {
			t.Fatal("correlation or explicit headers lost")
		}
		extracted := propagation.TraceContext{}.Extract(t.Context(), propagation.HeaderCarrier(got.Header))
		want := trace.SpanContextFromContext(ctx)
		actual := trace.SpanContextFromContext(extracted)
		if actual.TraceID() != want.TraceID() || actual.SpanID() != want.SpanID() || actual.TraceState().String() != "vendor=value" {
			t.Fatal("trace context lost")
		}
		cancel()
		if !errors.Is(got.Context().Err(), context.Canceled) {
			t.Fatal("cancellation lost")
		}
		return nil, cause
	}}
	wrapped := &propagationTransport{base: base}
	if _, err := wrapped.RoundTrip(req); !errors.Is(err, cause) {
		t.Fatalf("transport error = %v", err)
	}
	if req.Header.Get("X-Request-ID") != "explicit-id" || req.Header.Get("traceparent") != "old-trace" || req.Header.Get("tracestate") != "old=value" {
		t.Fatal("original headers mutated")
	}
	wrapped.CloseIdleConnections()
	if base.closed.Load() != 1 {
		t.Fatal("idle connection cleanup not forwarded")
	}
}

func TestPropagationWithoutContextKeepsExplicitHeaders(t *testing.T) {
	t.Parallel()
	for _, header := range []http.Header{nil, {"X-Request-Id": {"explicit"}, "Traceparent": {"explicit-trace"}}} {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "http://downstream.test/", nil)
		req.Header = header
		wrapped := &propagationTransport{base: &correlationTransport{roundTrip: func(got *http.Request) (*http.Response, error) {
			if got.Header.Get("X-Request-ID") != header.Get("X-Request-ID") || got.Header.Get("traceparent") != header.Get("traceparent") {
				t.Fatal("invented or removed a caller header")
			}
			return nil, nil
		}}}
		if _, err := wrapped.RoundTrip(req); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPropagationOverridesEveryHeaderCasing(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequestWithContext(correlationContext(t.Context(), "current"), http.MethodGet, "http://downstream.test/", nil)
	req.Header = http.Header{
		"X-Request-ID": {"stale"}, "x-request-id": {"shadow"},
		"Traceparent": {"stale"}, "traceparent": {"shadow"},
		"Tracestate": {"stale"}, "TRACESTATE": {"shadow"},
	}
	wrapped := &propagationTransport{base: &correlationTransport{roundTrip: func(got *http.Request) (*http.Response, error) {
		if len(got.Header) != 3 || got.Header.Get("X-Request-ID") != "current" {
			t.Fatalf("duplicate or stale correlation headers: %v", got.Header)
		}
		extracted := propagation.TraceContext{}.Extract(t.Context(), propagation.HeaderCarrier(got.Header))
		if trace.SpanContextFromContext(extracted).TraceID() != trace.SpanContextFromContext(req.Context()).TraceID() {
			t.Fatal("trace context changed")
		}
		return nil, nil
	}}}
	if _, err := wrapped.RoundTrip(req); err != nil {
		t.Fatal(err)
	}
	if len(req.Header) != 6 {
		t.Fatal("original headers mutated")
	}
}

func TestPropagationConcurrentRequestsStaySeparate(t *testing.T) {
	t.Parallel()
	const count = 32
	seen := make(chan string, count)
	wrapped := &propagationTransport{base: &correlationTransport{roundTrip: func(req *http.Request) (*http.Response, error) {
		seen <- req.Header.Get("X-Request-ID")
		return nil, nil
	}}}
	var wg sync.WaitGroup
	for i := range count {
		wg.Go(func() {
			req := httptest.NewRequestWithContext(correlationContext(t.Context(), strconv.Itoa(i)), http.MethodGet, "http://downstream.test/", nil)
			if _, err := wrapped.RoundTrip(req); err != nil {
				t.Error(err)
			}
			if req.Header.Get("X-Request-ID") != "" {
				t.Error("original request mutated")
			}
		})
	}
	wg.Wait()
	close(seen)
	unique := make(map[string]bool, count)
	for id := range seen {
		unique[id] = true
	}
	if len(unique) != count {
		t.Fatalf("distinct request IDs = %d, want %d", len(unique), count)
	}
}

func TestFactoryPropagatesCorrelationToHTTPServer(t *testing.T) {
	t.Parallel()
	seen := make(chan http.Header, 1)
	server := httptest.NewTestServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen <- r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	}))
	factory, err := New(config.OutboundConfig{Clients: map[string]config.OutboundClientConfig{
		"test": {BaseURL: "http://downstream.example.com"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := factory.Close(); err != nil {
			t.Error(err)
		}
	})
	client, err := factory.Client("test")
	if err != nil {
		t.Fatal(err)
	}
	wrapped, ok := client.HTTPClient().Transport.(*propagationTransport)
	if !ok {
		t.Fatal("Factory did not install propagation")
	}
	// Route the real client through httptest's in-memory transport.
	wrapped.base = server.Client().Transport
	ctx := correlationContext(t.Context(), "factory-id")
	if _, status, err := client.GetRaw(ctx, "/", nil); err != nil || status != http.StatusNoContent {
		t.Fatalf("HTTP result = %d, %v", status, err)
	}
	header := <-seen
	if header.Get("X-Request-ID") != "factory-id" || header.Get("traceparent") == "" {
		t.Fatal("correlation did not reach the server")
	}
}
