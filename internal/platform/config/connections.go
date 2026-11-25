package config

import (
	"cmp"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
)

// DefaultConnection names the existing database and Redis configuration.
const DefaultConnection = "default"

// CappedPoolSize is the initial size of a pool with a positive hard limit.
func (c RedisConfig) CappedPoolSize() int {
	const redisxPoolSize = 10
	return cmp.Or(c.PoolSize, min(redisxPoolSize, c.MaxActiveConns))
}

// PoolDatabases includes DB 0, which Redis initializes even when it isn't declared for borrowing.
func (c RedisConfig) PoolDatabases() []int {
	if strings.TrimSpace(c.Addr) == "" {
		return nil
	}
	dbs := append([]int{0}, c.DBs...)
	slices.Sort(dbs)
	return slices.Compact(dbs)
}

func (c *Config) validateNamedConnections() error {
	var errs []error
	for _, name := range slices.Sorted(maps.Keys(c.DatabaseConnections)) {
		if err := validateConnectionName(name); err != nil {
			errs = append(errs, fmt.Errorf("database_connections.%s: %w", name, err))
		}
		if err := validateDatabaseConfig(c.DatabaseConnections[name]); err != nil {
			errs = append(errs, fmt.Errorf("database_connections.%s: %w", name, err))
		}
	}
	for _, name := range slices.Sorted(maps.Keys(c.RedisConnections)) {
		cfg := c.RedisConnections[name]
		if err := validateConnectionName(name); err != nil {
			errs = append(errs, fmt.Errorf("redis_connections.%s: %w", name, err))
		}
		if err := mustNonEmpty("addr", cfg.Addr); err != nil {
			errs = append(errs, fmt.Errorf("redis_connections.%s: %w", name, err))
		}
		if err := validateRedisConfig(cfg); err != nil {
			errs = append(errs, fmt.Errorf("redis_connections.%s: %w", name, err))
		}
	}
	return errors.Join(errs...)
}

func validateConnectionName(name string) error {
	if name == DefaultConnection {
		return errors.New("name default is reserved for the legacy connection")
	}
	if len(name) == 0 || len(name) > 64 || name[0] < 'a' || name[0] > 'z' {
		return errors.New("connection name must match [a-z][a-z0-9_]{0,63}")
	}
	for _, c := range name {
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' {
			return errors.New("connection name must match [a-z][a-z0-9_]{0,63}")
		}
	}
	return nil
}
