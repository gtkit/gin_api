//go:build integration

package redis

import (
	"context"
	"errors"
	"net"
	"os"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/testutil"
	goredis "github.com/redis/go-redis/v9"
)

func TestCappedPoolRealRedisWaitAndRecovery(t *testing.T) {
	addr := os.Getenv("REDIS_ITEST_ADDR")
	if addr != "" {
		host, _, err := net.SplitHostPort(addr)
		if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
			t.Skip("this test requires a loopback Redis endpoint")
		}
	}
	probe := testutil.RealRedisOrSkip(t)
	options := probe.Options()
	owner, err := Open(t.Context(), Options{Config: &config.Config{Redis: config.RedisConfig{
		Addr: options.Addr, Password: options.Password, DBs: []int{2},
		MaxActiveConns: 2, PoolSize: 2, ReadTimeoutMS: 100,
	}}, Required: func(*config.Config) bool { return true }})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owner.Close(context.Background()); err != nil {
			t.Error(err)
		}
	})
	ids := make(map[int64]bool)
	var pipelineIDCount int
	for _, db := range []int{0, 2} {
		conn := owner.Client(db)
		held := [2]*goredis.Conn{conn.Conn(), conn.Conn()}
		t.Cleanup(func() {
			for _, c := range held {
				if c != nil {
					if err := c.Close(); err != nil {
						t.Error(err)
					}
				}
			}
		})
		for _, c := range held {
			id, err := c.ClientID(t.Context()).Result()
			if err != nil || ids[id] {
				t.Fatalf("db %d: duplicate or invalid server connection %d: %v", db, id, err)
			}
			ids[id] = true
		}
		waitCtx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
		err := conn.Ping(waitCtx).Err()
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) || conn.PoolStats().TotalConns != 2 {
			t.Fatalf("db %d: pool saturation error=%v connections=%d", db, err, conn.PoolStats().TotalConns)
		}
		var pipelineID *goredis.IntCmd
		if _, err := conn.Pipelined(t.Context(), func(pipe goredis.Pipeliner) error {
			pipelineID = pipe.ClientID(t.Context())
			return nil
		}); err != nil {
			t.Fatalf("db %d: separate pipeline pool while main pool is full: %v", db, err)
		}
		if id := pipelineID.Val(); id <= 0 || ids[id] {
			t.Fatalf("db %d: pipeline did not borrow a separate physical connection", db)
		}
		pipelineIDCount++
		err = held[0].Close()
		held[0] = nil
		if err != nil {
			t.Fatal(err)
		}
		if err := conn.Ping(t.Context()).Err(); err != nil {
			t.Fatalf("db %d: request after releasing a connection: %v", db, err)
		}
	}
	t.Logf("Redis reported %d main and %d pipeline connections across DB 0 and 2; both capped main pools rejected a third borrower", len(ids), pipelineIDCount)
}
