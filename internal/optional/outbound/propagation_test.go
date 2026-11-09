package outbound

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gtkit/httpc"
	"github.com/gtkit/logger/v2"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

func TestRetryMetadataIsolatedPerAttempt(t *testing.T) {
	var previous *retryMetadata
	calls := 0
	_, err := retryAttempts(t.Context(), RetryPolicy{Attempts: 2}, nil, func(ctx context.Context, _ *httpc.Client) (int, error) {
		metadata, ok := ctx.Value(retryMetadataKey{}).(*retryMetadata)
		if !ok || metadata == previous || metadata.read() != "" {
			t.Fatal("attempt reused response metadata")
		}
		previous = metadata
		calls++
		metadata.value.Store(new("0"))
		return 503, nil
	})
	if err != nil || calls != 3 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}

func TestConcurrentRetryMetadataDoesNotCrossRequests(t *testing.T) {
	wrapper := &propagationTransport{base: &correlationTransport{roundTrip: func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 503, Header: http.Header{"Retry-After": []string{req.URL.Path[1:]}}, Body: http.NoBody}, nil
	}}}
	var wg sync.WaitGroup
	for i := range 16 {
		wg.Go(func() {
			metadata := new(retryMetadata)
			value := strconv.Itoa(i)
			ctx := context.WithValue(t.Context(), retryMetadataKey{}, metadata)
			req := httptest.NewRequestWithContext(ctx, "GET", "http://downstream.test/"+value, nil)
			resp, err := wrapper.RoundTrip(req)
			if err != nil {
				t.Error(err)
				return
			}
			if err := resp.Body.Close(); err != nil {
				t.Error(err)
			}
			if metadata.read() != value {
				t.Errorf("metadata crossed requests: %s want %s", metadata.read(), value)
			}
		})
	}
	wg.Wait()
}

type correlationTransport struct {
	roundTrip func(*http.Request) (*http.Response, error)
	closed    atomic.Int32
}

func (t *correlationTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return t.roundTrip(r)
}

func (t *correlationTransport) CloseIdleConnections() { t.closed.Add(1) }

func correlationContext(t testing.TB, ctx context.Context, id string) context.Context {
	t.Helper()
	state, err := trace.ParseTraceState("vendor=value")
	if err != nil {
		t.Fatal(err)
	}
	ctx = trace.ContextWithSpanContext(ctx, trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1, 2, 3}, SpanID: trace.SpanID{4, 5, 6},
		TraceFlags: trace.FlagsSampled, TraceState: state,
	}))
	return logger.ContextWithRequestID(ctx, id)
}

func TestPropagationPreservesOriginalRequestAndContext(t *testing.T) {
	t.Parallel()
	cause := errors.New("transport failure")
	ctx, cancel := context.WithCancel(correlationContext(t, t.Context(), "internal-id"))
	t.Cleanup(cancel)
	req := httptest.NewRequestWithContext(ctx, http.MethodGet, "http://downstream.test/", nil)
	req.Header.Set("X-Request-ID", "explicit-id")
	req.Header.Set("Traceparent", "old-trace")
	req.Header.Set("Tracestate", "old=value")
	req.Header.Set("Authorization", "explicit-auth")
	base := &correlationTransport{roundTrip: func(got *http.Request) (*http.Response, error) {
		if got == req || got.Context() != ctx || got.Body != req.Body {
			t.Fatal("request ownership or context changed")
		}
		if got.Header.Get("X-Request-ID") != "internal-id" || got.Header.Get("Authorization") != "explicit-auth" {
			t.Fatal("correlation or explicit headers lost")
		}
		// Reuse the verified request context so inheritance stays explicit.
		extractionContext := trace.ContextWithSpanContext(ctx, trace.SpanContext{})
		extracted := propagation.TraceContext{}.Extract(extractionContext, propagation.HeaderCarrier(got.Header))
		want := trace.SpanContextFromContext(ctx)
		actual := trace.SpanContextFromContext(extracted)
		if actual.TraceID() != want.TraceID() || actual.SpanID() != want.SpanID() || actual.TraceState().String() != "vendor=value" {
			t.Fatal("trace context lost")
		}
		for _, tc := range []struct {
			name   string
			header http.Header
		}{
			{name: "missing"},
			{name: "invalid", header: http.Header{"Traceparent": {"invalid"}}},
		} {
			extracted := propagation.TraceContext{}.Extract(extractionContext, propagation.HeaderCarrier(tc.header))
			if trace.SpanContextFromContext(extracted).IsValid() {
				t.Fatalf("%s traceparent inherited the original span", tc.name)
			}
		}
		cancel()
		if !errors.Is(got.Context().Err(), context.Canceled) {
			t.Fatal("cancellation lost")
		}
		return nil, cause
	}}
	wrapped := &propagationTransport{base: base}
	response, err := wrapped.RoundTrip(req)
	if response != nil && response.Body != nil {
		if err := response.Body.Close(); err != nil {
			t.Error(err)
		}
	}
	if response != nil || !errors.Is(err, cause) {
		t.Fatalf("transport response = %v, error = %v", response, err)
	}
	if req.Header.Get("X-Request-ID") != "explicit-id" || req.Header.Get("Traceparent") != "old-trace" || req.Header.Get("Tracestate") != "old=value" {
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
			if got.Header.Get("X-Request-ID") != header.Get("X-Request-ID") || got.Header.Get("Traceparent") != header.Get("Traceparent") {
				t.Fatal("invented or removed a caller header")
			}
			return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody}, nil
		}}}
		response, err := wrapped.RoundTrip(req)
		if err != nil {
			t.Fatal(err)
		}
		if response == nil || response.Body == nil {
			t.Fatal("missing response body")
		}
		if err := response.Body.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPropagationOverridesEveryHeaderCasing(t *testing.T) {
	t.Parallel()
	req := httptest.NewRequestWithContext(correlationContext(t, t.Context(), "current"), http.MethodGet, "http://downstream.test/", nil)
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
		return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody}, nil
	}}}
	response, err := wrapped.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	if response == nil || response.Body == nil {
		t.Fatal("missing response body")
	}
	if err := response.Body.Close(); err != nil {
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
		return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody}, nil
	}}}
	var wg sync.WaitGroup
	for i := range count {
		ctx := correlationContext(t, t.Context(), strconv.Itoa(i))
		wg.Go(func() {
			req := httptest.NewRequestWithContext(ctx, http.MethodGet, "http://downstream.test/", nil)
			response, err := wrapped.RoundTrip(req)
			if err != nil {
				t.Error(err)
				return
			}
			if response == nil || response.Body == nil {
				t.Error("missing response body")
				return
			}
			if err := response.Body.Close(); err != nil {
				t.Error(err)
				return
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
	factory, err := New(Config{Clients: map[string]ClientConfig{
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
	ctx := correlationContext(t, t.Context(), "factory-id")
	if _, status, err := client.GetRaw(ctx, "/", nil); err != nil || status != http.StatusNoContent {
		t.Fatalf("HTTP result = %d, %v", status, err)
	}
	header := <-seen
	if header.Get("X-Request-ID") != "factory-id" || header.Get("Traceparent") == "" {
		t.Fatal("correlation did not reach the server")
	}
}
