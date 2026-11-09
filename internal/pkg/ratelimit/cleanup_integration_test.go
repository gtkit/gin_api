//go:build integration

package ratelimit

import (
	"strconv"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/testutil"
	"github.com/redis/go-redis/v9"
)

func TestRealRedisRegistryCleanupIsBounded(t *testing.T) {
	client := testutil.RealRedisOrSkip(t)
	prefix := testutil.UniqueKeyPrefix(t, client)
	backend, err := NewRedisBackend(t.Context(), client, WithKeyPrefix(prefix))
	if err != nil {
		t.Fatal(err)
	}
	policy := luaTestPolicy()
	policy.MaxKeys = 300
	if _, err := backend.check(t.Context(), policy, "live"); err != nil {
		t.Fatal(err)
	}
	now, err := client.Time(t.Context()).Result()
	if err != nil {
		t.Fatal(err)
	}
	expired := make([]redis.Z, 257)
	for i := range expired {
		expired[i] = redis.Z{Score: float64(now.Add(-2 * bucketTTL(policy)).UnixMilli()), Member: "old-" + strconv.Itoa(i)}
	}
	registry := backend.registryKey(policy)
	if err := client.ZAdd(t.Context(), registry, expired...).Err(); err != nil {
		t.Fatal(err)
	}
	for i, want := range []int64{131, 4, 4} {
		key := "new-" + strconv.Itoa(i)
		decision, err := backend.check(t.Context(), policy, key)
		if err != nil || !decision.Allowed() {
			t.Fatalf("decision=%+v error=%v", decision, err)
		}
		count, err := client.ZCard(t.Context(), registry).Result()
		if err != nil || count != want {
			t.Fatalf("registry count=%d want=%d error=%v", count, want, err)
		}
	}
	if _, err := client.ZScore(t.Context(), registry, backend.qualifiedKey("live")).Result(); err != nil {
		t.Fatal("live member was removed", err)
	}
	if ttl, err := client.PTTL(t.Context(), registry).Result(); err != nil || ttl <= 0 || ttl > bucketTTL(policy)+time.Second {
		t.Fatalf("registry TTL=%s error=%v", ttl, err)
	}
}
