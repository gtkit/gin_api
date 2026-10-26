package outbound

import (
	"net/http"
	"strings"
	"sync/atomic"

	"github.com/gtkit/logger/v2"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

type retryMetadataKey struct{}
type retryMetadata struct{ value atomic.Pointer[string] }

func (m *retryMetadata) read() string {
	if value := m.value.Load(); value != nil {
		return *value
	}
	return ""
}

// propagationTransport adds request-local correlation headers without changing
// the transport that owns connection pooling and cancellation.
type propagationTransport struct {
	base http.RoundTripper
}

func (t *propagationTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	cloned := req.Clone(req.Context())
	if cloned.Header == nil {
		cloned.Header = make(http.Header)
	}
	if trace.SpanContextFromContext(req.Context()).IsValid() {
		deleteHeader(cloned.Header, "traceparent")
		deleteHeader(cloned.Header, "tracestate")
		propagation.TraceContext{}.Inject(req.Context(), propagation.HeaderCarrier(cloned.Header))
	}
	if id := logger.RequestIDFromContext(req.Context()); id != "" {
		deleteHeader(cloned.Header, "X-Request-ID")
		cloned.Header.Set("X-Request-ID", id)
	}
	response, err := t.base.RoundTrip(cloned)
	if metadata, ok := req.Context().Value(retryMetadataKey{}).(*retryMetadata); ok {
		after := ""
		if response != nil {
			after = response.Header.Get("Retry-After")
		}
		metadata.value.Store(new(after))
	}
	return response, err
}

// deleteHeader also handles keys inserted directly without Header.Set.
func deleteHeader(header http.Header, name string) {
	for key := range header {
		if strings.EqualFold(key, name) {
			delete(header, key)
		}
	}
}

func (t *propagationTransport) CloseIdleConnections() {
	if closer, ok := t.base.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}
