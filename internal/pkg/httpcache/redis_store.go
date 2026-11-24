package httpcache

import (
	"context"
	"errors"
	"io"
	"net"
	"time"

	"github.com/gtkit/gin-api/internal/platform/data"
	"github.com/gtkit/gincache/persist"
	"github.com/gtkit/logger/v2"
	"github.com/redis/go-redis/v9"
)

const cacheRedisReadBudget = 50 * time.Millisecond
const cacheRedisWriteBudget = 100 * time.Millisecond

type resilientRedisStore struct {
	persist.CacheStore
	health *data.AvailabilityTracker
}

func newResilientRedisStore(client *redis.Client, prefix string) (*resilientRedisStore, error) {
	health, err := data.NewAvailabilityTracker("response-cache", data.DefaultAvailabilityPolicy(), func(ctx context.Context) error {
		err := client.Get(ctx, prefix+":health").Err()
		if errors.Is(err, redis.Nil) {
			return nil
		}
		return err
	})
	if err != nil {
		return nil, err
	}
	return &resilientRedisStore{
		CacheStore: persist.NewRedisStore(client, persist.WithKeyPrefix(prefix),
			persist.WithReadTimeout(cacheRedisReadBudget), persist.WithWriteTimeout(cacheRedisWriteBudget)),
		health: health,
	}, nil
}

func (s *resilientRedisStore) Get(key string, value any) error {
	if s.health.State() != data.AvailabilityHealthy {
		ctx, cancel := context.WithTimeout(context.Background(), cacheRedisReadBudget)
		defer cancel()
		if probed, err := s.health.MaybeProbe(ctx); probed && err == nil && s.health.State() == data.AvailabilityHealthy {
			logger.Info("Redis response cache recovered")
		}
		return persist.ErrCacheMiss
	}
	err := s.CacheStore.Get(key, value)
	if err == nil || errors.Is(err, persist.ErrCacheMiss) {
		s.health.ObserveWhileHealthy(nil)
	} else if isCacheRedisFailure(err) && s.health.ObserveWhileHealthy(err) {
		logger.Error("Redis response cache degraded; requests will use bounded origin access")
	}
	return err
}

func (s *resilientRedisStore) Set(key string, value any, expire time.Duration) error {
	return s.SetWithContext(context.Background(), key, value, expire)
}

func (s *resilientRedisStore) SetWithContext(ctx context.Context, key string, value any, expire time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.health.State() != data.AvailabilityHealthy {
		return nil // A cache write is optional during the recovery window.
	}
	ctx, cancel := context.WithTimeout(ctx, cacheRedisWriteBudget)
	defer cancel()
	return s.CacheStore.SetWithContext(ctx, key, value, expire)
}

func isCacheRedisFailure(err error) bool {
	_, networkFailure := errors.AsType[net.Error](err)
	return !errors.Is(err, redis.ErrPoolTimeout) && (networkFailure ||
		errors.Is(err, redis.ErrClosed) || errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		redis.HasErrorPrefix(err, "LOADING") || redis.HasErrorPrefix(err, "MASTERDOWN") ||
		redis.HasErrorPrefix(err, "CLUSTERDOWN"))
}
