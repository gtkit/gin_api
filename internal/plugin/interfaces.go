// Package plugin defines the replaceable capabilities consumed by the scaffold.
package plugin

import (
	"time"

	"github.com/gin-gonic/gin"
	data "github.com/gtkit/gin-api/internal/platform/data"
	"github.com/redis/go-redis/v9"
)

// DatabaseProvider exposes borrowed database handles, never lifecycle ownership.
type DatabaseProvider = data.DatabaseHandle

// RedisClientProvider is the narrow Redis capability needed by cache and
// other compile-time adapters. It intentionally has no Close method: the Redis
// plugin that owns the client is the only component allowed to close it.
type RedisClientProvider interface {
	Client(db int) *redis.Client
	DefaultClient() *redis.Client
	Configured() bool
}

// RedisProvider combines borrowed handles and observation, without lifecycle
// ownership or a required concrete availability tracker.
type RedisProvider interface {
	RedisClientProvider
	data.RedisHandle
}

// ResponseCache provides explicit route middleware without exposing store ownership.
type ResponseCache interface {
	Enabled() bool
	Middleware(time.Duration) gin.HandlerFunc
}
