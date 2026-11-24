//go:build integration

package redis

import (
	"context"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/testutil"
	goredis "github.com/redis/go-redis/v9"
)

func TestIntegrationRedisPoolBudgetDuringReplicaOverlap(t *testing.T) {
	server := testutil.RealRedisOrSkip(t)
	parent := server.Options()
	settings := config.RedisConfig{
		Addr: parent.Addr, Password: parent.Password, DBs: []int{0, 2},
		PoolSize: 8, MaxActiveConns: 8, MinIdleConns: 1, PoolTimeoutMS: 20, PipelinePoolSize: -1,
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()

	const replicas = 6
	ids := make(map[int64]struct{}, replicas*len(settings.DBs)*settings.PoolSize)
	var held []*goredis.Conn
	var owners []func() error
	// Return the checked-out connections before closing their owners.
	defer func() {
		for _, conn := range held {
			if err := conn.Close(); err != nil {
				t.Error(err)
			}
		}
		for _, closeOwner := range owners {
			if err := closeOwner(); err != nil {
				t.Error(err)
			}
		}
	}()
	for range replicas {
		owner, err := OpenRedis(ctx, settings)
		if err != nil {
			t.Fatal(err)
		}
		owners = append(owners, owner.Close)
		for _, db := range settings.DBs {
			client, ok := owner.GetClient(db)
			if !ok {
				t.Fatalf("Redis DB %d missing", db)
			}
			for range settings.PoolSize {
				conn := client.Conn()
				held = append(held, conn)
				id, err := conn.ClientID(ctx).Result()
				if err != nil {
					t.Fatal(err)
				}
				ids[id] = struct{}{}
			}
			if stats := client.PoolStats(); stats.TotalConns != uint32(settings.PoolSize) || stats.PipelineStats != nil {
				t.Fatalf("Redis DB %d connections=%d, dedicated pipeline=%t", db, stats.TotalConns, stats.PipelineStats != nil)
			}
		}
	}
	if got, want := len(ids), replicas*len(settings.DBs)*settings.PoolSize; got != want {
		t.Fatalf("distinct Redis connection IDs during overlap=%d, want %d", got, want)
	}
	t.Logf("Redis reported %d distinct sockets across %d replicas and %d databases", len(ids), replicas, len(settings.DBs))
}
