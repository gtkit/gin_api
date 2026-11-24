package httpcache

import (
	"context"
	"errors"
	"net"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gtkit/gin-api/internal/platform/data"
	"github.com/gtkit/gincache/persist"
	"github.com/redis/go-redis/v9"
)

type scriptedCacheStore struct {
	persist.CacheStore
	err   error
	reads int
	sets  int
}

func (s *scriptedCacheStore) Get(string, any) error {
	s.reads++
	return s.err
}

func (s *scriptedCacheStore) SetWithContext(context.Context, string, any, time.Duration) error {
	s.sets++
	return nil
}

func TestResilientCacheShortCircuitsAndRecovers(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		policy := data.DefaultAvailabilityPolicy()
		probes := 0
		health, err := data.NewAvailabilityTracker("test-cache", policy, func(context.Context) error {
			probes++
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		backend := &scriptedCacheStore{err: &net.OpError{Op: "read", Err: errors.New("offline")}}
		store := &resilientRedisStore{CacheStore: backend, health: health}
		assertGetError := func(key string, want error) {
			t.Helper()
			if err := store.Get(key, nil); !errors.Is(err, want) {
				t.Fatalf("Get(%q) returned %v, want %v", key, err, want)
			}
		}

		for range policy.FailureThreshold - 1 {
			assertGetError("hot", backend.err)
		}
		backend.err = persist.ErrCacheMiss
		assertGetError("absent", persist.ErrCacheMiss)
		backend.err = redis.ErrPoolTimeout
		assertGetError("hot", redis.ErrPoolTimeout)
		backend.err = &net.OpError{Op: "read", Err: errors.New("offline")}
		for range policy.FailureThreshold {
			assertGetError("hot", backend.err)
		}
		if got := health.State(); got != data.AvailabilityDegraded {
			t.Fatalf("after read failures, state=%v", got)
		}
		reads := backend.reads
		if err := store.Get("hot", nil); !errors.Is(err, persist.ErrCacheMiss) || backend.reads != reads || probes != 0 {
			t.Fatalf("during cooldown: err=%v reads=%d probes=%d", err, backend.reads, probes)
		}
		if err := store.SetWithContext(t.Context(), "hot", "body", time.Second); err != nil || backend.sets != 0 {
			t.Fatalf("optional cache write was attempted while down: %v, %d", err, backend.sets)
		}
		time.Sleep(policy.Cooldown)
		assertGetError("hot", persist.ErrCacheMiss)
		if probes != 1 || health.State() == data.AvailabilityHealthy {
			t.Fatalf("recovered after the first probe: probes=%d, state=%v", probes, health.State())
		}
		assertGetError("hot", persist.ErrCacheMiss)
		if probes != 1 {
			t.Fatalf("probe interval bypassed: probes=%d", probes)
		}
		time.Sleep(policy.ProbeInterval)
		assertGetError("hot", persist.ErrCacheMiss)
		if probes != 2 || health.State() != data.AvailabilityHealthy {
			t.Fatalf("second probe did not recover: probes=%d state=%v", probes, health.State())
		}
		backend.err = nil
		if err := store.Get("hot", nil); err != nil || backend.reads != reads+1 {
			t.Fatalf("cache did not resume reads: err=%v reads=%d", err, backend.reads)
		}
	})
}
