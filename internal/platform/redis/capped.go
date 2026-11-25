package redis

import (
	"cmp"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/gtkit/gin-api/internal/platform/config"
	goredis "github.com/redis/go-redis/v9"
)

// cappedClient owns one go-redis pool set per logical DB.
type cappedClient struct {
	dbs     []int
	clients map[int]*goredis.Client
}

func openCappedRedis(ctx context.Context, cfg config.RedisConfig, tlsConfig *tls.Config) (Client, error) {
	dbs := cfg.PoolDatabases()
	if len(dbs) == 0 || dbs[0] < 0 {
		return nil, errors.New("redis address and nonnegative database numbers are required")
	}
	if cfg.MaxActiveConns <= 0 || cfg.MaxActiveConns > math.MaxInt32 {
		return nil, errors.New("redis max_active_conns is outside the supported range")
	}
	// Match the locked redisx defaults for the explicitly capped path.
	poolSize := cfg.CappedPoolSize()
	idle := cmp.Or(cfg.MinIdleConns, min(3, poolSize))
	if poolSize <= 0 || poolSize > cfg.MaxActiveConns || idle < 0 || idle > poolSize {
		return nil, errors.New("redis pool sizes must satisfy min_idle_conns <= pool_size <= max_active_conns")
	}
	if min(cfg.DialTimeoutMS, cfg.ReadTimeoutMS, cfg.WriteTimeoutMS) < 0 {
		return nil, errors.New("redis timeouts must be nonnegative")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	options := goredis.Options{
		Addr:                  cfg.Addr,
		Password:              cfg.Password,
		PoolSize:              poolSize,
		MinIdleConns:          idle,
		MaxActiveConns:        cfg.MaxActiveConns,
		MaxRetries:            3,
		DialTimeout:           cmp.Or(time.Duration(cfg.DialTimeoutMS)*time.Millisecond, 5*time.Second),
		ReadTimeout:           cmp.Or(time.Duration(cfg.ReadTimeoutMS)*time.Millisecond, 3*time.Second),
		WriteTimeout:          cmp.Or(time.Duration(cfg.WriteTimeoutMS)*time.Millisecond, 3*time.Second),
		ConnMaxIdleTime:       5 * time.Minute,
		ContextTimeoutEnabled: true,
		TLSConfig:             tlsConfig,
	}
	client := &cappedClient{dbs: dbs, clients: make(map[int]*goredis.Client, len(dbs))}
	for _, db := range dbs {
		dbOptions := options
		dbOptions.DB = db
		client.clients[db] = goredis.NewClient(&dbOptions)
	}
	if err := client.HealthCheck(ctx); err != nil {
		return nil, closeAndWrap(client.Close, err)
	}
	return client, nil
}

func (c *cappedClient) GetClient(db int) (*goredis.Client, bool) {
	client, ok := c.clients[db]
	return client, ok
}

func (c *cappedClient) DefaultClient() *goredis.Client { return c.clients[0] }

func (c *cappedClient) PoolStats() map[int]*goredis.PoolStats {
	stats := make(map[int]*goredis.PoolStats, len(c.clients))
	for db, client := range c.clients {
		stats[db] = client.PoolStats()
	}
	return stats
}

func (c *cappedClient) HealthCheck(ctx context.Context) error {
	errs := make([]error, len(c.dbs))
	var wg sync.WaitGroup
	for i, db := range c.dbs {
		wg.Go(func() {
			if err := c.clients[db].Ping(ctx).Err(); err != nil {
				errs[i] = fmt.Errorf("redis db %d ping: %w", db, err)
			}
		})
	}
	wg.Wait()
	return errors.Join(errs...)
}

func (c *cappedClient) Close() error {
	var errs []error
	for _, db := range c.dbs {
		if err := c.clients[db].Close(); err != nil {
			errs = append(errs, fmt.Errorf("close redis db %d: %w", db, err))
		}
	}
	return errors.Join(errs...)
}
