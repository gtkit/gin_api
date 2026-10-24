package ratelimit

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/redis/go-redis/v9"
)

// alwaysNoScript 恒返回 NOSCRIPT，逼每个请求都走脚本重载路径。
//
// 这正是主从切换、故障恢复与 SCRIPT FLUSH 之后的真实形态——
// 大量并发请求同时发现脚本缓存没了，同时去重载。
type alwaysNoScript struct{ cause error }

func (alwaysNoScript) ScriptLoad(ctx context.Context, _ string) *redis.StringCmd {
	cmd := redis.NewStringCmd(ctx)
	cmd.SetVal("reloaded-sha")
	return cmd
}

func (f alwaysNoScript) Process(_ context.Context, cmd redis.Cmder) error {
	cmd.SetErr(f.cause)
	return f.cause
}

func (alwaysNoScript) Del(ctx context.Context, _ ...string) *redis.IntCmd {
	return redis.NewIntCmd(ctx)
}

// TestConcurrentScriptReloadIsRaceFree 钉住脚本缓存标识的并发读写安全。
//
// 必须配 `-race` 才有意义（本仓 make check 的测试一律带 -race）。
//
// 此前 sha 是普通字符串字段：每个请求读它，NOSCRIPT 时并发写它。
// 实测 race detector 报 read/write 与 write/write 两类竞争，而 Go 内存模型下
// 那是未定义行为——最坏情况是读到撕裂的 string header。
//
// 触发条件一点也不罕见：Redis 主从切换、故障恢复、运维执行 SCRIPT FLUSH，
// 每一次都会让在飞的全部请求同时进入重载路径。
func TestConcurrentScriptReloadIsRaceFree(t *testing.T) {
	t.Parallel()

	failure := errors.New("NOSCRIPT No matching script")
	backend := &RedisBackend{
		client:   alwaysNoScript{cause: failure},
		health:   NewHealthState(DefaultHealthPolicy(), nil),
		observer: nopObserver{},
	}
	initial := "initial-sha"
	backend.sha.Store(&initial)

	policy := Policy{
		ID: "concurrent.reload", Rate: 10, Burst: 5,
		Scope: ScopeIP, Store: StoreRedis,
		FailureMode: FailureClosed, MaxKeys: 100,
	}

	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			// 首批执行返回NOSCRIPT原因，降级后的短路返回不可用判定。
			decision, err := backend.check(context.Background(), policy, "subject")
			if err != nil && !errors.Is(err, failure) {
				t.Errorf("unexpected reload failure: %v", err)
			} else if err == nil && decision.Outcome != OutcomeBackendUnavailable {
				t.Errorf("degraded request returned %v", decision.Outcome)
			}
		})
	}
	wg.Wait()

	// 重载后标识必须已更新，否则说明重载路径根本没跑到。
	if got := *backend.sha.Load(); got != "reloaded-sha" {
		t.Fatalf("sha = %q, want the reloaded value: the reload path did not run", got)
	}
}

// TestBucketTTLNeverOverflows 钉住键存活时长不会因溢出变成负数。
//
// 此前 `time.Duration(秒数) * time.Second` 直接溢出：Rate 极小而 Burst 较大时，
// 补满一桶所需秒数轻易超过 time.Duration（纳秒计的 int64）能表示的范围。
//
// 而策略校验只查了 Lua 的精确整数范围——**那是另一个边界**。两个边界各查各的，
// 于是一组通过了校验的合法策略产出了 -2346317h46m53s，
// 负 TTL 传给 Redis 会让键立即过期或被拒绝，限流因此静默失效。
func TestBucketTTLNeverOverflows(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		rate  float64
		burst int
	}{
		{name: "ordinary policy", rate: 10, burst: 100},
		{name: "slow refill", rate: 0.5, burst: 10},
		// 此前实测溢出的那一组：校验通过，TTL 为负。
		{name: "the combination that used to overflow", rate: 0.000001, burst: 10000},
		{name: "extremely slow refill", rate: 1e-9, burst: 1},
		{name: "large burst with slow refill", rate: 1e-6, burst: 1_000_000},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			policy := Policy{
				ID: "ttl.probe", Rate: tt.rate, Burst: tt.burst,
				Scope: ScopeIP, Store: StoreRedis,
				FailureMode: FailureClosed, MaxKeys: 100,
			}

			ttl := bucketTTL(policy)
			if ttl <= 0 {
				t.Fatalf("bucketTTL = %v: a non-positive TTL makes redis drop or reject the key, "+
					"and the limiter silently stops working", ttl)
			}
			if ttl > maxBucketTTL {
				t.Fatalf("bucketTTL = %v, exceeds the %v cap: a key nobody touches would keep "+
					"occupying memory long after its quota refilled", ttl, maxBucketTTL)
			}
			// 毫秒数同样必须为正——那才是真正传给 Lua 的值。
			if ttl.Milliseconds() <= 0 {
				t.Fatalf("ttl.Milliseconds() = %d, want positive", ttl.Milliseconds())
			}
		})
	}
}
