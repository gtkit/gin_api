package httpcache

import (
	"time"

	"github.com/gin-gonic/gin"
)

// ResponseCache is the route-level view of a response store. Its owner closes it.
type ResponseCache interface {
	Enabled() bool
	Middleware(time.Duration) gin.HandlerFunc
}

// RouteHandlers mounts caching before origin admission, so replayed responses
// and eligible requests sharing a response do not take an origin slot.
func RouteHandlers(cache ResponseCache, ttl time.Duration) []gin.HandlerFunc {
	if cache == nil {
		return nil
	}
	if owner, ok := cache.(interface {
		RouteHandlers(time.Duration) []gin.HandlerFunc
	}); ok {
		return owner.RouteHandlers(ttl)
	}
	return []gin.HandlerFunc{cache.Middleware(ttl)}
}
