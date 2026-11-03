package redis

import (
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/redisx"
)

func TestSmallPoolKeepsValidInheritedIdleTarget(t *testing.T) {
	for _, tc := range []struct {
		pool, idle, wantPool, wantIdle int
	}{
		{0, 0, 10, 3}, {1, 0, 1, 1}, {2, 0, 2, 2},
		{3, 0, 3, 3}, {20, 0, 20, 3}, {2, 1, 2, 1},
	} {
		srv := miniredis.RunT(t)
		client, err := OpenRedis(t.Context(), config.RedisConfig{Addr: srv.Addr(), DBs: []int{0, 3}, PoolSize: tc.pool, MinIdleConns: tc.idle})
		if err != nil {
			t.Fatalf("pool=%d idle=%d: %v", tc.pool, tc.idle, err)
		}
		for _, db := range []int{0, 3} {
			conn, ok := client.GetClient(db)
			if !ok || conn.Options().PoolSize != tc.wantPool || conn.Options().MinIdleConns != tc.wantIdle {
				t.Errorf("pool=%d idle=%d db=%d: wrong effective pool", tc.pool, tc.idle, db)
			}
		}
		if err := client.Close(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPoolOptionUsesInheritedDefaults(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ pool, idle, inherited, want int }{
		{2, 0, 7, 2}, {20, 0, 7, 7}, {2, 1, 7, 1}, {2, 3, 7, 3},
	} {
		actual := redisx.Config{PoolSize: 10, MinIdleConns: tc.inherited}
		for _, option := range buildRedisOptions(config.RedisConfig{PoolSize: tc.pool, MinIdleConns: tc.idle}) {
			option(&actual)
		}
		if actual.MinIdleConns != tc.want {
			t.Errorf("pool=%d idle=%d inherited=%d: got %d want %d", tc.pool, tc.idle, tc.inherited, actual.MinIdleConns, tc.want)
		}
	}
}
