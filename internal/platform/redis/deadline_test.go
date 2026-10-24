package redis

import (
	"context"
	"errors"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/alicebob/miniredis/v2/server"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/testutil"
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
