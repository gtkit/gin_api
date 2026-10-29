package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"slices"

	"github.com/gtkit/gin-api/internal/pkg/metrics"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/platform/data"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

// Database borrows a named MySQL handle for module assembly. Runtime owns its
// lifetime; callers must not close it. Unknown names never fall back to default.
func (r *Runtime) Database(name string) (*gorm.DB, error) {
	if r == nil || r.owners == nil {
		return nil, errors.New("database connections are not initialized")
	}
	owner := r.owners.database
	if name != config.DefaultConnection {
		owner = r.owners.databases[name]
	}
	if owner == nil {
		return nil, fmt.Errorf("database connection %q is not configured", name)
	}
	db := owner.DB()
	if db == nil {
		return nil, fmt.Errorf("database connection %q is unavailable", name)
	}
	return db, nil
}

// Redis borrows one logical DB from a named Redis instance for module assembly.
// Runtime owns its lifetime; neither a missing instance nor DB falls back.
func (r *Runtime) Redis(name string, db int) (*redis.Client, error) {
	if r == nil || r.owners == nil {
		return nil, errors.New("Redis connections are not initialized")
	}
	owner := r.owners.redis
	if name != config.DefaultConnection {
		owner = r.owners.redisConnections[name]
	}
	if owner == nil {
		return nil, fmt.Errorf("Redis connection %q is not configured", name)
	}
	declared := r.config.Redis.DBs
	if name != config.DefaultConnection {
		declared = r.config.RedisConnections[name].DBs
	}
	if !slices.Contains(declared, db) {
		return nil, fmt.Errorf("Redis connection %q database %d is not configured", name, db)
	}
	client, ok := owner.GetClient(db)
	if !ok || client == nil {
		return nil, fmt.Errorf("Redis connection %q database %d is unavailable or not configured", name, db)
	}
	return client, nil
}

func (r *Runtime) instrumentConnections() error {
	if err := r.resources.InstrumentTracing(r.config.Database.Name); err != nil {
		return err
	}
	for _, name := range slices.Sorted(maps.Keys(r.owners.databases)) {
		if err := data.InstrumentDatabaseTracing(r.owners.databases[name].DB(), r.config.DatabaseConnections[name].Name); err != nil {
			return fmt.Errorf("database[%s]: %w", name, err)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(r.owners.redisConnections)) {
		if err := data.InstrumentRedisTracing(r.owners.redisConnections[name]); err != nil {
			return fmt.Errorf("redis[%s]: %w", name, err)
		}
	}
	return nil
}

func registerNamedConnectionMetrics(inf *infra, cleanup *providerCleanup) error {
	if inf.connections == nil {
		return nil
	}
	for _, name := range slices.Sorted(maps.Keys(inf.connections.databases)) {
		stop, err := registerDatabaseMetrics(inf.recorder, inf.connections.databases[name].DB(), name)
		if err != nil {
			return fmt.Errorf("database[%s] pool metrics: %w", name, err)
		}
		if stop == nil {
			return fmt.Errorf("database[%s] pool metrics: SQL handle is unavailable", name)
		}
		inf.metricsCleanup = append(inf.metricsCleanup, stop)
		cleanup.push("database["+name+"] metrics", cleanupMetrics, func(context.Context) error { return stop() })
	}
	for _, name := range slices.Sorted(maps.Keys(inf.connections.redisConnections)) {
		owner := inf.connections.redisConnections[name]
		stop, err := inf.recorder.RegisterRedisPoolStats(slices.Sorted(maps.Keys(owner.PoolStats())), func(db int) metrics.RedisPoolStats {
			client := owner.Client(db)
			if client == nil {
				return metrics.RedisPoolStats{}
			}
			stats := client.PoolStats()
			return metrics.RedisPoolStats{ConnectionName: name, Client: client, Main: *stats, Pipeline: (*redis.PoolStats)(stats.PipelineStats)}
		})
		if err != nil {
			return fmt.Errorf("redis[%s] pool metrics: %w", name, err)
		}
		inf.metricsCleanup = append(inf.metricsCleanup, stop)
		cleanup.push("redis["+name+"] metrics", cleanupMetrics, func(context.Context) error { return stop() })
	}
	return nil
}
