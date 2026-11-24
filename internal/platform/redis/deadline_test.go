package redis

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/alicebob/miniredis/v2/server"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/testutil"
	goredis "github.com/redis/go-redis/v9"
)

// The server completes the real TCP handshake and startup PINGs, then deliberately
// withholds the business response. This exercises socket deadlines, not a mock command.
func TestRedisRequestDeadlineBoundsSocketRead(t *testing.T) {
	testutil.RequireLoopback(t)
	srv := miniredis.RunT(t)
	owned, err := OpenRedis(t.Context(), config.RedisConfig{Addr: srv.Addr(), DBs: []int{0, 3}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owned.Close(); err != nil {
			t.Error(err)
		}
	})
	srv.Server().SetPreHook(func(_ *server.Peer, command string, _ ...string) bool {
		return strings.EqualFold(command, "get")
	})
	for _, db := range []int{0, 3} {
		client, ok := owned.GetClient(db)
		if !ok || !client.Options().ContextTimeoutEnabled {
			t.Fatalf("db %d does not honor context timeouts", db)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
		started := time.Now()
		err := client.Get(ctx, "stalled-response").Err()
		elapsed := time.Since(started)
		cancel()
		t.Logf("db=%d request=100ms socket=%s elapsed=%s error=%v", db, client.Options().ReadTimeout, elapsed, err)
		if err == nil || elapsed > 500*time.Millisecond {
			t.Fatalf("db %d exceeded request budget: elapsed=%s err=%v", db, elapsed, err)
		}
		if timeout, ok := errors.AsType[net.Error](err); !errors.Is(err, context.DeadlineExceeded) && (!ok || !timeout.Timeout()) {
			t.Fatalf("db %d did not return a timeout: %v", db, err)
		}
	}
}

func TestRedisWithoutRequestDeadlineRetainsSocketTimeout(t *testing.T) {
	testutil.RequireLoopback(t)
	srv := miniredis.RunT(t)
	owned, err := OpenRedis(t.Context(), config.RedisConfig{Addr: srv.Addr(), ReadTimeoutMS: 50})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := owned.Close(); err != nil {
			t.Error(err)
		}
	})
	srv.Server().SetPreHook(func(_ *server.Peer, command string, _ ...string) bool {
		return strings.EqualFold(command, "get")
	})
	client := owned.DefaultClient()
	started := time.Now()
	err = client.Get(context.WithoutCancel(t.Context()), "stalled-response").Err()
	elapsed := time.Since(started)
	if timeout, ok := errors.AsType[net.Error](err); !ok || !timeout.Timeout() {
		t.Fatalf("expected configured socket timeout, got %v", err)
	}
	if elapsed < 50*time.Millisecond || elapsed > 2*time.Second {
		t.Fatalf("socket timeout/retry budget drifted: %s", elapsed)
	}
	t.Logf("no request deadline: socket=%s elapsed=%s", client.Options().ReadTimeout, elapsed)
}

func TestRedisPoolLimitsBoundWait(t *testing.T) {
	addr := os.Getenv("GIN_REDIS_POOL_TEST_ADDR")
	if addr == "" {
		testutil.RequireLoopback(t)
		addr = miniredis.RunT(t).Addr()
	} else {
		host, _, err := net.SplitHostPort(addr)
		if ip := net.ParseIP(host); err != nil || ip == nil || !ip.IsLoopback() {
			t.Fatal("Redis pool verification requires a loopback address")
		}
		t.Log("using the configured loopback Redis server")
	}

	for _, tc := range []struct {
		name         string
		pipelineSize int
	}{
		{"shared pipeline pool", -1},
		{"dedicated pipeline pool", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			owned, err := OpenRedis(t.Context(), config.RedisConfig{
				Addr:             addr,
				DBs:              []int{2},
				PoolSize:         1,
				MaxActiveConns:   1,
				PoolTimeoutMS:    20,
				PipelinePoolSize: tc.pipelineSize,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := owned.Close(); err != nil {
					t.Error(err)
				}
			})
			for _, db := range []int{0, 2} {
				client, ok := owned.GetClient(db)
				if !ok {
					t.Fatalf("db %d was not initialized", db)
				}
				options := client.Options()
				if options.MaxActiveConns != 1 || options.PoolTimeout != 20*time.Millisecond ||
					options.PipelinePoolSize != tc.pipelineSize ||
					(client.PoolStats().PipelineStats != nil) != (tc.pipelineSize > 0) {
					t.Fatalf("db %d did not receive the configured pool or pipeline budget", db)
				}
				verifyFullPool(t, client, db)
				if _, err := client.Pipelined(t.Context(), func(pipe goredis.Pipeliner) error {
					pipe.Ping(t.Context())
					return nil
				}); err != nil {
					t.Fatalf("db %d pipeline after releasing the main connection: %v", db, err)
				}
			}
		})
	}
}

func verifyFullPool(t *testing.T, client *goredis.Client, db int) {
	t.Helper()
	conn := client.Conn()
	defer func() {
		if err := conn.Close(); err != nil {
			t.Error(err)
		}
	}()
	budget, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	if err := conn.Ping(budget).Err(); err != nil {
		t.Fatal(err)
	}
	if err := client.Ping(budget).Err(); !errors.Is(err, goredis.ErrPoolTimeout) {
		t.Fatalf("db %d ping on saturated pool = %v, want pool timeout", db, err)
	}
	short, cancelShort := context.WithTimeout(budget, 5*time.Millisecond)
	defer cancelShort()
	if err := client.Ping(short).Err(); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("db %d ping with earlier deadline = %v, want deadline exceeded", db, err)
	}
	if connections := client.PoolStats().TotalConns; connections != 1 {
		t.Fatalf("db %d has %d main connections under saturation, want 1", db, connections)
	}
}
