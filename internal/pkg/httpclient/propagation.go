package httpclient

import (
	"net/http"
	"strings"

	"github.com/gtkit/logger/v2"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

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
	return t.base.RoundTrip(cloned)
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
