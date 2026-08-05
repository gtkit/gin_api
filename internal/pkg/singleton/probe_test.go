package singleton

import (
	"context"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/gtkit/gin-api/internal/testutil"
)

// TestProbeAcceptsAWorkingRedis 覆盖探测的正常路径。
func TestProbeAcceptsAWorkingRedis(t *testing.T) {
	t.Parallel()
	testutil.RequireLoopback(t)

	srv := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: srv.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	if err := Probe(context.Background(), client, "test"); err != nil {
		t.Fatalf("Probe against a working Redis: %v", err)
	}

	// 探测不得留下垃圾：所有键要么已删除，要么带短 TTL。
	for _, key := range srv.Keys() {
		if srv.TTL(key) == 0 {
			t.Errorf("probe left key %q without a TTL", key)
		}
	}
}

// TestProbeRejectsAnUnreachableRedis 覆盖不可达路径。
//
// 这是探测存在的理由：不可达必须在**启动期**暴露，而不是等到第一次
// 真正需要锁的时候——那时通常已经有两个实例在跑同一个任务。
func TestProbeRejectsAnUnreachableRedis(t *testing.T) {
	t.Parallel()
	testutil.RequireLoopback(t)

	srv := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: srv.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	srv.Close()

	if err := Probe(context.Background(), client, "test"); err == nil {
		t.Fatal("Probe passed against an unreachable Redis: a PING-only check would do " +
			"the same, and the lock would fail exactly when it is first needed")
	}
}
