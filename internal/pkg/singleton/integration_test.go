//go:build integration

// 真实 Redis 集成测试：单实例租约。
//
// 覆盖的轴——**只有真实 Redis 能回答的那些**：
//   - 互斥在真实服务上成立。这条契约的失效后果是"两个副本同时跑同一个单实例任务"，
//     而那在 miniredis 上永远看不出来：内嵌实例每个测试一份，压根没有跨进程竞争。
//   - 租约脚本在真实 Lua 5.1 下的语义（持有者判定、fence 递增、续期与释放）。
//     miniredis 跑的是 gopher-lua，类型转换是另一套实现。
//   - SCRIPT FLUSH 之后的自愈（opt-in，理由见 testutil.RequireScriptFlushPermission）。
//
// 安全设计：连专用 db（默认 15），租约名带本次运行独占前缀，结束只删自己的键。
//
// 运行方式：
//
//	go test -tags integration -run TestIntegration ./internal/pkg/singleton/
//	ALLOW_REDIS_SCRIPT_FLUSH=1 go test -tags integration ./internal/pkg/singleton/
package singleton_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/pkg/singleton"
	"github.com/gtkit/gin-api/internal/testutil"

	"github.com/redis/go-redis/v9"
)

const leaseTTL = 5 * time.Second

// twoContenders 造两个**独立**的 Lease 实例，指向同一个租约名。
//
// 必须是两个实例：单实例内部的重复 Acquire 可能被自身状态短路，
// 那测不到"另一个副本抢不到"这件事——而后者才是这个组件存在的理由。
func twoContenders(t *testing.T) (a, b *singleton.Lease, client *redis.Client) {
	t.Helper()

	client = testutil.RealRedisOrSkip(t)
	prefix := testutil.UniqueKeyPrefix(t, client)

	a, err := singleton.NewLease(client, prefix, "task", leaseTTL)
	if err != nil {
		t.Fatalf("NewLease(a): %v", err)
	}
	b, err = singleton.NewLease(client, prefix, "task", leaseTTL)
	if err != nil {
		t.Fatalf("NewLease(b): %v", err)
	}
	return a, b, client
}

// TestIntegrationLeaseIsMutuallyExclusive 钉住互斥、续期与释放的完整生命周期。
func TestIntegrationLeaseIsMutuallyExclusive(t *testing.T) {
	ctx := context.Background()
	a, b, _ := twoContenders(t)

	held, err := a.Acquire(ctx)
	if err != nil {
		t.Fatalf("a.Acquire: %v", err)
	}
	if held.Token == "" {
		t.Fatal("Acquire returned an empty token")
	}

	// 另一个竞争者必须抢不到。
	if _, err := b.Acquire(ctx); !errors.Is(err, singleton.ErrNotAcquired) {
		t.Fatalf("b.Acquire while a holds = %v, want ErrNotAcquired", err)
	}

	// 持有者能续期。
	if err := a.Renew(ctx, held); err != nil {
		t.Fatalf("a.Renew: %v", err)
	}

	// 释放之后另一个竞争者才拿得到。
	if err := a.Release(ctx, held); err != nil {
		t.Fatalf("a.Release: %v", err)
	}
	if _, err := b.Acquire(ctx); err != nil {
		t.Fatalf("b.Acquire after release: %v", err)
	}
}

// TestIntegrationRenewRejectsAStaleHolder 钉住续期认持有者，不只认租约存在。
//
// 若续期只判"键还在"，一个已经失去租约的旧实例会把新持有者的租约一直续下去——
// 表现为两个实例都认为自己持有。
func TestIntegrationRenewRejectsAStaleHolder(t *testing.T) {
	ctx := context.Background()
	a, b, _ := twoContenders(t)

	held, err := a.Acquire(ctx)
	if err != nil {
		t.Fatalf("a.Acquire: %v", err)
	}
	if err := a.Release(ctx, held); err != nil {
		t.Fatalf("a.Release: %v", err)
	}

	// b 成为新持有者。
	if _, err := b.Acquire(ctx); err != nil {
		t.Fatalf("b.Acquire: %v", err)
	}

	// a 拿着过期的 Held 续期必须失败。
	if err := a.Renew(ctx, held); err == nil {
		t.Fatal("a stale holder successfully renewed a lease owned by someone else")
	}
}

// TestIntegrationReleaseByNonHolderIsRejected 钉住释放同样认持有者。
//
// 否则任何实例都能把别人的租约释放掉，互斥形同虚设。
func TestIntegrationReleaseByNonHolderIsRejected(t *testing.T) {
	ctx := context.Background()
	a, b, _ := twoContenders(t)

	held, err := a.Acquire(ctx)
	if err != nil {
		t.Fatalf("a.Acquire: %v", err)
	}

	// b 用一个伪造的 Held 释放。
	forged := singleton.Held{Token: "forged-token", Fence: held.Fence}
	if err := b.Release(ctx, forged); err == nil {
		// 释放"成功"本身可能被实现为幂等的 no-op，因此再验一次真实后果：
		// a 的租约还在不在。
		if _, acquireErr := b.Acquire(ctx); acquireErr == nil {
			t.Fatal("a forged release freed the lease held by another instance")
		}
	}
}

// TestIntegrationLeaseSurvivesScriptFlush 钉住脚本缓存被清空后的自愈。
//
// 主从切换与 SCRIPT FLUSH 之后脚本缓存就是空的。缺了 NOSCRIPT 重载，
// 单实例任务会在故障恢复之后持续拿不到租约——而根因只是缓存没了。
//
// opt-in：SCRIPT FLUSH 是服务器级操作，会清掉同一实例上别人的脚本缓存。
func TestIntegrationLeaseSurvivesScriptFlush(t *testing.T) {
	testutil.RequireScriptFlushPermission(t)

	ctx := context.Background()
	a, _, client := twoContenders(t)

	held, err := a.Acquire(ctx)
	if err != nil {
		t.Fatalf("a.Acquire: %v", err)
	}

	if err := client.ScriptFlush(ctx).Err(); err != nil {
		t.Fatalf("SCRIPT FLUSH: %v", err)
	}

	if err := a.Renew(ctx, held); err != nil {
		t.Fatalf("Renew after SCRIPT FLUSH: %v (the NOSCRIPT reload path did not recover)", err)
	}
	if err := a.Release(ctx, held); err != nil {
		t.Fatalf("Release after SCRIPT FLUSH: %v", err)
	}
}
