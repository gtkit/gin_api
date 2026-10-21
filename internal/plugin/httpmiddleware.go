package plugin

import (
	"errors"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/gin-api/internal/pkg/metrics"
)

// ErrMiddlewareAlreadyBuilt prevents reusing one lifecycle instance for engines.
var ErrMiddlewareAlreadyBuilt = errors.New("HTTP middleware chain was already built")

// MiddlewareDeps supplies the final observation instruments, after their provider.
// The default builder requires a Recorder, including an explicitly created no-op.
type MiddlewareDeps struct {
	Metrics        *metrics.Recorder
	TracingEnabled bool
	ServiceName    string
}

// MiddlewareBuilder returns an explicit ordered chain without lifecycle ownership.
type MiddlewareBuilder interface {
	Build(MiddlewareDeps) ([]gin.HandlerFunc, error)
}
