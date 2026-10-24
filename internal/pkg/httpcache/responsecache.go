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
