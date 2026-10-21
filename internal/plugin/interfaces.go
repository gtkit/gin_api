// Package plugin contains the built-in compile-time plugin implementations.
package plugin

import (
	"context"
	"database/sql"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/gin-api/internal/core"
	"github.com/gtkit/gin-api/internal/pkg/httpcache"
	data "github.com/gtkit/gin-api/internal/platform/data"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// DatabasePlugin is the typed capability exposed by a MySQL plugin.
type DatabasePlugin interface {
	core.Plugin
	DB() *gorm.DB
	SQLDB() *sql.DB
	PingContext(context.Context) error
}

// RedisClientProvider is the narrow Redis capability needed by cache and
// other compile-time adapters. It intentionally has no Close method: the Redis
// plugin that owns the client is the only component allowed to close it.
type RedisClientProvider interface {
	Client(db int) *redis.Client
	DefaultClient() *redis.Client
	Configured() bool
}

// RedisLifecyclePlugin is the typed Redis capability exposed to the bootstrap layer.
type RedisLifecyclePlugin interface {
	core.Plugin
	RedisClientProvider
	data.RedisHandle
	Availability() *data.AvailabilityTracker
	Ping(context.Context) error
}

// CacheLifecyclePlugin is the typed response-cache capability exposed to router
// assembly. The stager remains owned by the plugin.
type CacheLifecyclePlugin interface {
	core.Plugin
	Stager() *httpcache.Stager
	Enabled() bool
	Middleware(time.Duration) gin.HandlerFunc
}
