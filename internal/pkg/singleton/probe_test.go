package singleton

import (
	"context"
	"errors"
	"testing"
	"time"

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
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("client.Close(): %v", err)
		}
	})

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
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Errorf("client.Close(): %v", err)
		}
	})
	srv.Close()

	if err := Probe(context.Background(), client, "test"); err == nil {
		t.Fatal("Probe passed against an unreachable Redis: a PING-only check would do " +
			"the same, and the lock would fail exactly when it is first needed")
	}
}

func TestProbeReleasesTheLastAcquiredToken(t *testing.T) {
	t.Parallel()
	client := &fakeLeaseClient{evalResults: []leaseEvalResult{
		{val: []any{int64(1), int64(1)}}, {val: int64(1)}, {val: int64(0)},
		{val: []any{int64(0), int64(0)}}, {val: int64(0)},
		{val: []any{int64(0), int64(0)}}, {val: int64(1)},
		{val: []any{int64(1), int64(2)}}, {val: int64(1)},
	}}
	if err := Probe(t.Context(), client, "isolated"); err != nil {
		t.Fatal(err)
	}
	if len(client.evalArgs) != 9 {
		t.Fatalf("calls=%d", len(client.evalArgs))
	}
	first, last, released := client.evalArgs[0][0], client.evalArgs[7][0], client.evalArgs[8][0]
	if first == last || released != last {
		t.Fatalf("cleanup used wrong lease token")
	}
}

func TestProbeKeepsPrimaryAndCleanupFailures(t *testing.T) {
	t.Parallel()
	primary, cleanup := errors.New("renew failed"), errors.New("release failed")
	client := &fakeLeaseClient{evalResults: []leaseEvalResult{
		{val: []any{int64(1), int64(1)}}, {err: primary}, {err: cleanup},
	}}
	err := Probe(t.Context(), client, "isolated")
	if !errors.Is(err, primary) || !errors.Is(err, cleanup) {
		t.Fatalf("lost cause: %v", err)
	}
}

func TestRunnerReportsReleaseFailure(t *testing.T) {
	t.Parallel()
	primary, cleanup := errors.New("task failed"), errors.New("release failed")
	client := &fakeLeaseClient{evalResults: []leaseEvalResult{
		{val: []any{int64(1), int64(1)}}, {err: cleanup},
	}}
	observer := &recordingObserver{}
	runner, err := NewRunner(newFakeLease(t, client), func(context.Context, Held) error { return primary }, Config{
		Interval: time.Hour, Guarantee: GuaranteeIdempotent, Observer: observer,
	})
	if err != nil {
		t.Fatal(err)
	}
	runner.runOnce(t.Context())
	if len(observer.failed) != 1 || !errors.Is(observer.failed[0], primary) || len(observer.lost) != 1 || !errors.Is(observer.lost[0], cleanup) {
		t.Fatalf("lost=%v failed=%v", observer.lost, observer.failed)
	}
}
