package bootstrap

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/testutil"
	"github.com/redis/go-redis/v9"
)

func TestNamedRedisUsesIndependentTCPServers(t *testing.T) {
	testutil.RequireLoopback(t)
	one, two := miniredis.RunT(t), miniredis.RunT(t)
	f := newLifecycleFixture(t)
	cfg := f.components[0].cfg
	cfg.RedisConnections = map[string]config.RedisConfig{
		"one": {Addr: one.Addr(), DBs: []int{1}},
		"two": {Addr: two.Addr(), DBs: []int{1}},
	}
	factories := f.factories()
	defaultRedis, openRedis := factories.redis, defaultRuntimeFactories().redis
	factories.redis = func(ctx context.Context, selected *config.Config) (redisOwner, error) {
		if selected.Redis.Addr == cfg.Redis.Addr {
			return defaultRedis(ctx, selected)
		}
		return openRedis(ctx, selected)
	}
	runtime, err := NewRuntime(t.Context(), Options{factories: &factories})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := runtime.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	})
	first, err := runtime.Redis("one", 1)
	if err != nil {
		t.Fatal(err)
	}
	second, err := runtime.Redis("two", 1)
	if err != nil {
		t.Fatal(err)
	}
	for client, value := range map[*redis.Client]string{first: "first", second: "second"} {
		if err := client.Set(t.Context(), "same-key", value, time.Minute).Err(); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := first.Get(t.Context(), "same-key").Result(); err != nil || got != "first" {
		t.Fatalf("first server value=%q error=%v", got, err)
	}
	if got, err := second.Get(t.Context(), "same-key").Result(); err != nil || got != "second" {
		t.Fatalf("second server value=%q error=%v", got, err)
	}
	if _, err := runtime.Redis("one", 0); err == nil {
		t.Fatal("implicit library DB0 was exposed without configuration")
	}
	if err := runtime.Close(context.WithoutCancel(t.Context())); err != nil {
		t.Fatal(err)
	}
	if _, err := runtime.Redis("one", 1); err == nil {
		t.Fatal("closed Redis handle was published")
	}
}
