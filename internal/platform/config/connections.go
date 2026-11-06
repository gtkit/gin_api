package config

import (
	"errors"
	"fmt"
	"maps"
	"slices"
)

// DefaultConnection names the existing database and Redis configuration.
const DefaultConnection = "default"

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
