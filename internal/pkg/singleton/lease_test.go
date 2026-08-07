package singleton

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"

	"github.com/gtkit/gin-api/internal/testutil"
)

// newTestLease 起一个进程内 Redis 并构造租约。
//
// 用 miniredis 而不是手写替身：本包的核心语义**全在三个 Lua 脚本里**
// （原子抢锁+INCR、compare-and-renew、compare-and-delete）。替身只能返回
// 预设的返回码，那样测到的是 Go 侧的分支映射，脚本本身一行都没执行——
// 而真正要防的错误（写成裸 PEXPIRE、抢锁失败仍 INCR、跨 slot）全在那一侧。
func newTestLease(t *testing.T, ttl time.Duration) (*Lease, *miniredis.Miniredis) {
	t.Helper()
	testutil.RequireLoopback(t)

	srv := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: srv.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	lease, err := NewLease(client, "test", "job", ttl)
	if err != nil {
		t.Fatalf("NewLease: %v", err)
	}
	return lease, srv
}

func TestAcquireGrantsExclusiveHold(t *testing.T) {
	t.Parallel()

	lease, _ := newTestLease(t, time.Minute)
	ctx := context.Background()

	held, err := lease.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}
	if held.Token == "" {
		t.Fatal("Acquire returned an empty token")
	}
	if held.Fence <= 0 {
		t.Fatalf("fence = %d, want a positive sequence number", held.Fence)
	}

	// 第二次必须拿不到——这就是"单实例"的全部含义。
	if _, err := lease.Acquire(ctx); !errors.Is(err, ErrNotAcquired) {
		t.Fatalf("second Acquire = %v, want ErrNotAcquired", err)
	}
}

// TestFenceIncreasesMonotonically 钉住 fence 的单调性。
//
// fencing 的全部作用是让下游能判断"谁更旧"。序号若不单调，下游拿到两个值
// 无从比较，fence 就退化成了另一个随机 token。
func TestFenceIncreasesMonotonically(t *testing.T) {
	t.Parallel()

	lease, _ := newTestLease(t, time.Minute)
	ctx := context.Background()

	var previous int64
	for i := range 5 {
		held, err := lease.Acquire(ctx)
		if err != nil {
			t.Fatalf("Acquire #%d: %v", i, err)
		}
		if held.Fence <= previous {
			t.Fatalf("fence #%d = %d, previous = %d: the sequence must strictly increase, "+
				"otherwise downstream cannot tell which holder is stale", i, held.Fence, previous)
		}
		previous = held.Fence

		if err := lease.Release(ctx, held); err != nil {
			t.Fatalf("Release #%d: %v", i, err)
		}
	}
}

// TestFailedAcquireDoesNotConsumeFence 钉住抢锁失败不消耗序号。
//
// 失败也 INCR 会让 fence 随失败次数虚增。那不破坏单调性，但让"序号跳了很多"
// 这个诊断信号失去意义——本来它说明发生过多次租约易主，虚增之后它只说明
// 有很多实例在抢。
func TestFailedAcquireDoesNotConsumeFence(t *testing.T) {
	t.Parallel()

	lease, _ := newTestLease(t, time.Minute)
	ctx := context.Background()

	first, err := lease.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	// 20 次失败的抢锁。
	for range 20 {
		if _, err := lease.Acquire(ctx); !errors.Is(err, ErrNotAcquired) {
			t.Fatalf("Acquire while held = %v, want ErrNotAcquired", err)
		}
	}

	if err := lease.Release(ctx, first); err != nil {
		t.Fatalf("Release: %v", err)
	}
	next, err := lease.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire after release: %v", err)
	}

	if next.Fence != first.Fence+1 {
		t.Fatalf("fence jumped from %d to %d after 20 failed attempts, want exactly +1: "+
			"failed acquisitions must not consume the sequence", first.Fence, next.Fence)
	}
}

// TestRenewOnlyExtendsOwnLease 是本包最重要的一条。
//
// 裸 PEXPIRE 会在"旧持有者租约已过期、新持有者已抢到锁"时错误地延长
// **新持有者**的锁：旧实例因此以为自己仍持有，两个实例同时在跑，
// 而其中一个的租约还被另一个不断续期。
func TestRenewOnlyExtendsOwnLease(t *testing.T) {
	t.Parallel()

	lease, srv := newTestLease(t, time.Second)
	ctx := context.Background()

	stale, err := lease.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	// 租约过期，另一个实例接手。
	srv.FastForward(2 * time.Second)
	fresh, err := lease.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire after expiry: %v", err)
	}

	// 旧持有者续租：必须失败。
	if err := lease.Renew(ctx, stale); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("a stale holder renewed successfully (%v): with a bare PEXPIRE it would "+
			"have extended the *new* holder's lease while believing it still owns one", err)
	}

	// 新持有者不受影响。
	if err := lease.Renew(ctx, fresh); err != nil {
		t.Fatalf("the current holder could not renew: %v", err)
	}
}

// TestReleaseDoesNotDeleteAnotherHolderLock 钉住释放不误伤。
//
// 无条件 DEL 会把别人正在用的锁删掉，让第三个实例立刻抢到——
// 一次超时因此放大成多个实例并发。
func TestReleaseDoesNotDeleteAnotherHolderLock(t *testing.T) {
	t.Parallel()

	lease, srv := newTestLease(t, time.Second)
	ctx := context.Background()

	stale, err := lease.Acquire(ctx)
	if err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	srv.FastForward(2 * time.Second)
	if _, err := lease.Acquire(ctx); err != nil {
		t.Fatalf("Acquire after expiry: %v", err)
	}

	// 旧持有者释放：不得删掉现任的锁。
	if err := lease.Release(ctx, stale); err != nil {
		t.Fatalf("Release by a stale holder returned an error: %v", err)
	}

	if _, err := lease.Acquire(ctx); !errors.Is(err, ErrNotAcquired) {
		t.Fatal("a stale holder's release removed the current holder's lock: one timeout " +
			"would escalate into several instances running at once")
	}
}

// TestKeysShareAClusterHashTag 钉住集群语义。
//
// 抢锁脚本同时访问锁与 fence 计数器。Redis 集群要求一次调用里的所有键落在
// 同一个 slot，否则直接拒绝执行——而那是**只在集群部署上**出现的失败，
// 单机测试永远发现不了。
func TestKeysShareAClusterHashTag(t *testing.T) {
	t.Parallel()

	lease, _ := newTestLease(t, time.Minute)

	lockTag := hashTagOf(t, lease.lockKey())
	fenceTag := hashTagOf(t, lease.fenceKey())
	if lockTag != fenceTag {
		t.Fatalf("lock key %q and fence key %q land in different cluster slots: "+
			"a Redis Cluster would reject the acquire script outright",
			lease.lockKey(), lease.fenceKey())
	}
	if lockTag != "job" {
		t.Errorf("hash tag = %q, want the task name", lockTag)
	}
}

func hashTagOf(t *testing.T, key string) string {
	t.Helper()
	open := strings.Index(key, "{")
	closeIdx := strings.Index(key, "}")
	if open < 0 || closeIdx < open {
		t.Fatalf("key %q has no cluster hash tag", key)
	}
	return key[open+1 : closeIdx]
}

// TestLeaseExpiresOnItsOwn 钉住租约会自然过期。
//
// 不过期的话，一个崩溃的实例会让任务永久停摆——而那比重复执行更糟。
func TestLeaseExpiresOnItsOwn(t *testing.T) {
	t.Parallel()

	lease, srv := newTestLease(t, time.Second)
	ctx := context.Background()

	if _, err := lease.Acquire(ctx); err != nil {
		t.Fatalf("Acquire: %v", err)
	}

	srv.FastForward(2 * time.Second)

	if _, err := lease.Acquire(ctx); err != nil {
		t.Fatalf("the lease did not expire, so a crashed instance would stall the task "+
			"forever: %v", err)
	}
}

func TestNewLeaseValidatesItsInputs(t *testing.T) {
	t.Parallel()

	testutil.RequireLoopback(t)
	srv := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: srv.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	tests := []struct {
		name   string
		client LeaseClient
		lease  string
		ttl    time.Duration
	}{
		{name: "no client", client: nil, lease: "job", ttl: time.Minute},
		{name: "no name", client: client, lease: "", ttl: time.Minute},
		{name: "zero ttl", client: client, lease: "job", ttl: 0},
		{name: "negative ttl", client: client, lease: "job", ttl: -time.Second},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, err := NewLease(tt.client, "p", tt.lease, tt.ttl); err == nil {
				t.Fatal("NewLease accepted an invalid input")
			}
		})
	}
}
