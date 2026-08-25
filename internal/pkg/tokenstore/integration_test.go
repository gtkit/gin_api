//go:build integration

// 真实 Redis 集成测试：刷新令牌的轮换与重用检测。
//
// 覆盖的轴——**只有真实 Redis 能回答的那些**：
//   - 轮换与重用检测的**原子性**在真实服务上成立。这条契约的失效后果是两种，
//     且都不会报错：一个已用过的令牌还能再换（重放），或者一次正常刷新被误判成
//     重用而把整个 family 撤销（用户被踢下线）。
//   - 三段脚本（签发 / 窥视 / 消费）在真实 Lua 5.1 下的语义与返回值编排。
//     miniredis 跑的是 gopher-lua，类型转换是另一套实现。
//   - SCRIPT FLUSH 之后的自愈（opt-in，理由见 testutil.RequireScriptFlushPermission）。
//     包内的 NOSCRIPT 分支此前只对着手写替身验过——替身返回的错误字符串是
//     测试自己编的，那证明不了真实 Redis 会那样回复。
//
// 安全设计：连专用 db（默认 15），键带本次运行独占前缀，结束只删自己的键。
//
// 运行方式：
//
//	go test -tags integration -run TestIntegration ./internal/pkg/tokenstore/
//	ALLOW_REDIS_SCRIPT_FLUSH=1 go test -tags integration ./internal/pkg/tokenstore/
package tokenstore_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/pkg/tokenstore"
	"github.com/gtkit/gin-api/internal/testutil"

	"github.com/redis/go-redis/v9"
)

// replaySecret 只用于本测试；长度需满足包的下界校验。
const replaySecret = "integration-test-replay-secret-0123456789"

func newRefreshStore(t *testing.T) (store *tokenstore.RefreshStore, client *redis.Client, prefix string) {
	t.Helper()

	client = testutil.RealRedisOrSkip(t)
	prefix = testutil.UniqueKeyPrefix(t, client)

	store, err := tokenstore.NewRefreshStore(
		context.Background(), client, prefix, time.Hour, replaySecret)
	if err != nil {
		t.Fatalf("NewRefreshStore against real redis: %v", err)
	}
	return store, client, prefix
}

// TestIntegrationRefreshRotationAndReplayWindow 钉住轮换，以及**窗口内重放**的容忍。
//
// 窗口内的第二次 Consume 不是重用，而是"客户端在重试一个丢失的响应"：
// 它必须返回与首次**完全一致**的令牌，而不是报错、也不是再轮换一次。
// 判错任一侧的代价都不小：报错会让一次网络抖动变成用户被登出；
// 再轮换一次会让客户端手里那枚刚拿到的令牌立刻作废。
//
// 这条契约在真实 Redis 上尤其值得钉：窗口靠脚本比较 Go 传入的时间戳与
// 存储在 hash 里的 replay_until，而首次轮换出的令牌是**加密后**存进去的，
// 重放时再解密还原——一整条链路都在存储与 Lua 之间来回。
func TestIntegrationRefreshRotationAndReplayWindow(t *testing.T) {
	ctx := context.Background()
	store, _, _ := newRefreshStore(t)
	const uid int64 = 4242

	token, err := store.Issue(ctx, uid)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if token == "" {
		t.Fatal("Issue returned an empty token")
	}

	// Peek 是只读的：它不该消费令牌。
	peeked, err := store.Peek(ctx, token)
	if err != nil {
		t.Fatalf("Peek: %v", err)
	}
	if peeked != uid {
		t.Fatalf("Peek uid = %d, want %d", peeked, uid)
	}

	gotUID, newToken, err := store.Consume(ctx, token)
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if gotUID != uid {
		t.Fatalf("Consume uid = %d, want %d", gotUID, uid)
	}
	if newToken == "" || newToken == token {
		t.Fatalf("Consume must rotate: new token %q, old %q", newToken, token)
	}

	// 窗口内重放：与首次调用等价。
	replayUID, replayToken, err := store.Consume(ctx, token)
	if err != nil {
		t.Fatalf("replay within the window = %v, want the first call's result", err)
	}
	if replayUID != uid {
		t.Fatalf("replay uid = %d, want %d", replayUID, uid)
	}
	if replayToken != newToken {
		t.Fatalf("replay returned %q, want the same token as the first call (%q)", replayToken, newToken)
	}
}

// TestIntegrationReuseOutsideTheWindowRevokesTheFamily 钉住窗口外的重放被判重用，
// 且**整族**作废。
//
// 重用检测的意义不在"拒绝这一次"：令牌被复制出去时攻击者与真实用户各持一枚，
// 只拒绝一次的话另一枚仍然可用。脚本因此就地撤销整族。
//
// 窗口是靠 hash 字段 replay_until 与传入时间比较实现的，所以这里**把那个字段
// 改成过去的时刻**来让窗口过去，而不是真的 sleep 掉默认的 10 秒——
// 一个每次跑都要等十几秒的用例迟早会被加上 -short 跳过。
// 改字段而不是改键 TTL 是因为窗口就存在字段里；这属于对存储的直接操作，
// 而本用例测的正是存储集成。
func TestIntegrationReuseOutsideTheWindowRevokesTheFamily(t *testing.T) {
	ctx := context.Background()
	store, client, prefix := newRefreshStore(t)
	const uid int64 = 4243

	token, err := store.Issue(ctx, uid)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	_, newToken, err := store.Consume(ctx, token)
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}

	closeReplayWindows(t, client, prefix)

	// 窗口外的重放 = 重用。
	if _, _, err := store.Consume(ctx, token); !errors.Is(err, tokenstore.ErrRefreshReused) {
		t.Fatalf("replay outside the window = %v, want ErrRefreshReused", err)
	}

	// 轮换出来的那枚也该失效——撤销的是整族，不是一枚。
	_, _, err = store.Consume(ctx, newToken)
	if err == nil {
		t.Fatal("the rotated token still worked after a reuse was detected")
	}
	if !errors.Is(err, tokenstore.ErrFamilyRevoked) && !errors.Is(err, tokenstore.ErrRefreshNotFound) {
		t.Fatalf("Consume of the rotated token = %v, want ErrFamilyRevoked or ErrRefreshNotFound", err)
	}
}

// closeReplayWindows 把本次运行下所有记录的重放窗口推到过去。
//
// 按"有没有 replay_until 字段"来找，而不是按键格式拼：记录键里含一段
// 未导出的哈希，外部测试算不出来。扫自己的前缀 + 认字段，两者都不依赖内部实现。
func closeReplayWindows(t *testing.T, client *redis.Client, prefix string) {
	t.Helper()

	ctx := context.Background()
	var cursor uint64
	touched := 0
	for {
		keys, next, err := client.Scan(ctx, cursor, prefix+"*", 256).Result()
		if err != nil {
			t.Fatalf("scan %s*: %v", prefix, err)
		}
		for _, key := range keys {
			exists, err := client.HExists(ctx, key, "replay_until").Result()
			if err != nil || !exists {
				continue
			}
			// 1ms after the epoch：任何"现在"都大于它，窗口必然已过。
			if err := client.HSet(ctx, key, "replay_until", "1").Err(); err != nil {
				t.Fatalf("close replay window on %s: %v", key, err)
			}
			touched++
		}
		if next == 0 {
			break
		}
		cursor = next
	}

	if touched == 0 {
		t.Fatal("no record carried a replay_until field; the window could not be closed " +
			"(has the storage layout changed?)")
	}
}

// TestIntegrationConsumeRejectsUnknownToken 钉住未知令牌不被当成有效。
func TestIntegrationConsumeRejectsUnknownToken(t *testing.T) {
	ctx := context.Background()
	store, _, _ := newRefreshStore(t)

	_, _, err := store.Consume(ctx, "definitely-not-a-real-token")
	if err == nil {
		t.Fatal("an unknown token was consumed successfully")
	}
	if !errors.Is(err, tokenstore.ErrRefreshNotFound) && !errors.Is(err, tokenstore.ErrMalformedToken) {
		t.Fatalf("Consume of an unknown token = %v, want ErrRefreshNotFound or ErrMalformedToken", err)
	}
}

// TestIntegrationRefreshSurvivesScriptFlush 钉住脚本缓存被清空后的自愈。
//
// 缺了 NOSCRIPT 重载，一次主从切换会让**所有人都刷新不了令牌**，
// 而根因只是脚本缓存没了。
//
// opt-in：SCRIPT FLUSH 是服务器级操作，会清掉同一实例上别人的脚本缓存。
func TestIntegrationRefreshSurvivesScriptFlush(t *testing.T) {
	testutil.RequireScriptFlushPermission(t)

	ctx := context.Background()
	store, client, _ := newRefreshStore(t)
	const uid int64 = 4244

	token, err := store.Issue(ctx, uid)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	if err := client.ScriptFlush(ctx).Err(); err != nil {
		t.Fatalf("SCRIPT FLUSH: %v", err)
	}

	// 消费路径与签发路径用的是两段不同的脚本，各有自己的 sha 缓存，
	// 因此两条自愈路径都要走一遍。
	if _, _, err := store.Consume(ctx, token); err != nil {
		t.Fatalf("Consume after SCRIPT FLUSH: %v (the NOSCRIPT reload path did not recover)", err)
	}
	if _, err := store.Issue(ctx, uid); err != nil {
		t.Fatalf("Issue after SCRIPT FLUSH: %v", err)
	}
}
