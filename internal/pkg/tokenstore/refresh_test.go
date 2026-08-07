package tokenstore

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

// newTestStore 起一个进程内 Redis 并构造 RefreshStore。
//
// 用 miniredis 而不是手写替身：本包的核心逻辑（原子消费、重用检测、族撤销）
// **全部在 Lua 脚本里**，而手写替身只能返回预设的 code——那样测到的是
// Go 侧的分支映射，脚本本身一行都没执行。真正要防的错误（脚本写错、
// 键名对不上、并发下重复消费）恰恰全在替身看不见的那一侧。
//
// 代价是它要监听回环端口，因此受限环境跳过——与本仓其它 loopback 测试同一处置。
func newTestStore(t *testing.T, ttl time.Duration) (*RefreshStore, *miniredis.Miniredis) {
	t.Helper()
	testutil.RequireLoopback(t)

	srv := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: srv.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	store, err := NewRefreshStore(context.Background(), client, "test", ttl, strings.Repeat("r", 32))
	if err != nil {
		t.Fatalf("NewRefreshStore: %v", err)
	}
	return store, srv
}

// advanceBy 把 store 的注入时钟与 Redis 的时钟**一起**往前拨。
//
// 两个都要推进：窗口判断在 Go 侧（脚本刻意不取 Redis 的 TIME，那让它保持
// 确定性），而记录的过期在 Redis 侧。只推 Go 时钟的话，测试看不到"记录被
// 提前删掉"这类错误——而那正是最需要被发现的一种：记录一旦在重放窗口后
// 消失，泄漏令牌的迟到重放会走到"找不到"分支，什么也不撤销，且完全静默。
func advanceBy(store *RefreshStore, srv *miniredis.Miniredis, d time.Duration) {
	base := time.Now().Add(d)
	store.now = func() time.Time { return base }
	srv.FastForward(d)
}

func TestIssueAndConsumeRotates(t *testing.T) {
	t.Parallel()

	store, _ := newTestStore(t, time.Hour)
	ctx := context.Background()

	token, err := store.Issue(ctx, 42)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	uid, next, err := store.Consume(ctx, token)
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}
	if uid != 42 {
		t.Errorf("uid = %d, want 42", uid)
	}
	// 轮换：新令牌必须不同于旧的，否则"一次性"名存实亡。
	if next == token {
		t.Fatal("Consume returned the same token: it must rotate")
	}
	// 新令牌可用。
	if _, _, err := store.Consume(ctx, next); err != nil {
		t.Fatalf("the rotated token does not work: %v", err)
	}
}

// TestReuseRevokesTheWholeFamily 是本包最重要的一条。
//
// 一个已被消费的刷新令牌再次出现，意味着它被复制到了别处。只拒绝这一次，
// 攻击者仍持有链条上的其它令牌；撤销整族才能真正切断。
func TestReuseRevokesTheWholeFamily(t *testing.T) {
	t.Parallel()

	store, srv := newTestStore(t, time.Hour)
	ctx := context.Background()

	first, err := store.Issue(ctx, 7)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	_, second, err := store.Consume(ctx, first)
	if err != nil {
		t.Fatalf("first Consume: %v", err)
	}

	// 越过重放窗口：窗口**内**的重放是客户端在重试一个丢失的响应，
	// 会拿回同一份令牌（见 TestReplayWithinTheWindowReturnsTheSameToken）；
	// 只有窗口之外的重放才说明令牌被复制了出去。
	//
	// 推进的是 store 的注入时钟，不是 miniredis 的：窗口判断在 Go 侧完成
	// （脚本保持确定性，时刻由调用方传入），FastForward 影响不到它。
	advanceBy(store, srv, 2*defaultReplayWindow)

	// 重放已消费的令牌。
	if _, _, err := store.Consume(ctx, first); !errors.Is(err, ErrRefreshReused) {
		t.Fatalf("replaying a used token = %v, want ErrRefreshReused", err)
	}

	// 关键：**合法用户手里那个尚未使用的令牌也必须失效**。
	// 只让重放失败而放任链条继续，等于检测到了入侵却没有采取任何行动。
	if _, _, err := store.Consume(ctx, second); !errors.Is(err, ErrFamilyRevoked) {
		t.Fatalf("the still-unused token in a compromised family = %v, want ErrFamilyRevoked "+
			"(detecting the leak without cutting the chain achieves nothing)", err)
	}
}

func TestConsumeRejectsUnknownAndMalformed(t *testing.T) {
	t.Parallel()

	store, _ := newTestStore(t, time.Hour)
	ctx := context.Background()

	tests := []struct {
		name  string
		token string
		want  error
	}{
		{name: "unknown token", token: "family-x.secret-y", want: ErrRefreshNotFound},
		{name: "no separator", token: "just-one-segment", want: ErrMalformedToken},
		{name: "empty", token: "", want: ErrMalformedToken},
		{name: "empty family", token: ".secret", want: ErrMalformedToken},
		{name: "empty secret", token: "family.", want: ErrMalformedToken},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, _, err := store.Consume(ctx, tt.token)
			if !errors.Is(err, tt.want) {
				t.Fatalf("Consume(%q) = %v, want %v", tt.token, err, tt.want)
			}
		})
	}
}

// TestLogoutRevokesFamily 覆盖登出路径。
func TestLogoutRevokesFamily(t *testing.T) {
	t.Parallel()

	store, _ := newTestStore(t, time.Hour)
	ctx := context.Background()

	token, err := store.Issue(ctx, 9)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if err := store.RevokeFamilyOfToken(ctx, token); err != nil {
		t.Fatalf("RevokeFamilyOfToken: %v", err)
	}

	if _, _, err := store.Consume(ctx, token); !errors.Is(err, ErrFamilyRevoked) {
		t.Fatalf("using a token after logout = %v, want ErrFamilyRevoked", err)
	}
}

// TestExpiredTokenIsGone 覆盖过期路径。
func TestExpiredTokenIsGone(t *testing.T) {
	t.Parallel()

	store, srv := newTestStore(t, time.Minute)
	ctx := context.Background()

	token, err := store.Issue(ctx, 3)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// miniredis 的时间由测试驱动，不必真的等待。
	srv.FastForward(2 * time.Minute)

	if _, _, err := store.Consume(ctx, token); !errors.Is(err, ErrRefreshNotFound) {
		t.Fatalf("an expired token = %v, want ErrRefreshNotFound", err)
	}
}

// TestStoredRecordHoldsNoUsableSecret 钉住存储里没有可直接使用的令牌。
//
// 存明文的话，一次快照泄漏就等同于交出所有在线用户的凭据。
func TestStoredRecordHoldsNoUsableSecret(t *testing.T) {
	t.Parallel()

	store, srv := newTestStore(t, time.Hour)
	ctx := context.Background()

	token, err := store.Issue(ctx, 5)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	_, secret, err := splitToken(token)
	if err != nil {
		t.Fatalf("splitToken: %v", err)
	}

	for _, key := range srv.Keys() {
		if strings.Contains(key, secret) {
			t.Fatalf("key %q contains the raw secret: a Redis snapshot would hand out "+
				"usable credentials", key)
		}
	}
}

// TestConcurrentConsumeRotatesExactlyOnce 钉住消费的原子性。
//
// 读取、判断已用、标记已用若不在同一个脚本里，两个并发请求会同时读到
// "未使用"，于是**轮换发生两次**——同一个族里凭空多出一条分叉链，
// 而重用检测再也无法判断哪条是正当的。
//
// 断言的是"轮换恰好一次"，不是"恰好一个调用者成功"：并发的重复请求全都
// 拿到**同一个**新令牌才是正确行为（那正是幂等的定义）。若断言只有一个
// 成功、其余失败，等于要求实现在并发重试时随机踢掉一部分客户端。
func TestConcurrentConsumeRotatesExactlyOnce(t *testing.T) {
	t.Parallel()

	store, _ := newTestStore(t, time.Hour)
	ctx := context.Background()

	token, err := store.Issue(ctx, 11)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	const racers = 8
	type outcome struct {
		next string
		err  error
	}
	results := make(chan outcome, racers)
	start := make(chan struct{})
	for range racers {
		go func() {
			<-start
			_, next, err := store.Consume(ctx, token)
			results <- outcome{next: next, err: err}
		}()
	}
	close(start)

	issued := make(map[string]struct{})
	for range racers {
		r := <-results
		if r.err != nil {
			t.Fatalf("a concurrent consume failed: %v", r.err)
		}
		issued[r.next] = struct{}{}
	}

	if len(issued) != 1 {
		t.Fatalf("%d distinct tokens were issued to %d concurrent identical requests, "+
			"want 1: the rotation is not atomic, and the family now has forked chains",
			len(issued), racers)
	}
}

// TestReplayWithinTheWindowReturnsTheSameToken 覆盖丢失响应后的重试。
//
// 这是重放窗口存在的全部理由：刷新的响应在网络上丢失时，客户端手里仍是
// 旧令牌，重试是它唯一合理的动作。没有窗口的话服务端会把它判成泄漏、
// 撤销整族——一次网络抖动因此升级成一次强制登出。
func TestReplayWithinTheWindowReturnsTheSameToken(t *testing.T) {
	t.Parallel()

	store, _ := newTestStore(t, time.Hour)
	ctx := context.Background()

	token, err := store.Issue(ctx, 21)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	uid, first, err := store.Consume(ctx, token)
	if err != nil {
		t.Fatalf("first Consume: %v", err)
	}

	// 客户端没收到响应，立刻用同一个令牌重试。
	replayUID, replayed, err := store.Consume(ctx, token)
	if err != nil {
		t.Fatalf("replaying within the window = %v, want the same result as the first call "+
			"(a lost response must not look like a leak)", err)
	}
	if replayed != first {
		t.Fatalf("replay returned %q, want the same token as the first call (%q)", replayed, first)
	}
	if replayUID != uid {
		t.Errorf("replay uid = %d, want %d", replayUID, uid)
	}

	// 族没有被撤销：那个令牌仍然可用。
	if _, _, err := store.Consume(ctx, first); err != nil {
		t.Fatalf("the rotated token stopped working after a replay: %v", err)
	}
}

// TestStorageNeverHoldsAUsableSecret 钉住"存储里不出现可直接使用的秘密段"
// （local-authentication spec）：族标识在键里明文可见，若轮换出的秘密段也
// 明文落库，一次存储快照泄漏即可拼出当前有效的刷新令牌。
func TestStorageNeverHoldsAUsableSecret(t *testing.T) {
	t.Parallel()

	store, srv := newTestStore(t, time.Hour)
	ctx := context.Background()

	token, err := store.Issue(ctx, 7)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	_, rotated, err := store.Consume(ctx, token)
	if err != nil {
		t.Fatalf("Consume: %v", err)
	}

	_, rotatedSecret, err := splitToken(rotated)
	if err != nil {
		t.Fatalf("splitToken: %v", err)
	}

	// 遍历 Redis 全部键的全部字段，断言轮换出的秘密段不以明文存在于任何一处。
	for _, key := range srv.Keys() {
		if srv.Type(key) != "hash" {
			continue
		}
		fields, err := srv.HKeys(key)
		if err != nil {
			t.Fatalf("HKeys(%s): %v", key, err)
		}
		for _, field := range fields {
			if srv.HGet(key, field) == rotatedSecret {
				t.Fatalf("field %q of key %q holds the rotated secret in plaintext: "+
					"a storage snapshot would yield a live refresh token", field, key)
			}
		}
	}
}

// TestUndecryptableReplayMaterialIsAPlainRejection 钉住解密失败的处置：
// 判为普通拒绝（重新登录），**不**当成重用——解密失败无法区分"密钥轮换了"
// 与"记录被篡改"，而误判重用会连带撤销合法用户的整族。
func TestUndecryptableReplayMaterialIsAPlainRejection(t *testing.T) {
	t.Parallel()

	store, srv := newTestStore(t, time.Hour)
	ctx := context.Background()

	token, err := store.Issue(ctx, 7)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if _, _, err := store.Consume(ctx, token); err != nil {
		t.Fatalf("first Consume: %v", err)
	}

	// 模拟密钥轮换：把记录里的密文改成解不开的内容。
	family, secret, err := splitToken(token)
	if err != nil {
		t.Fatalf("splitToken: %v", err)
	}
	srv.HSet(store.recordKey(family, secret), "next", "not-a-valid-ciphertext")

	_, _, err = store.Consume(ctx, token)
	if !errors.Is(err, ErrRefreshNotFound) {
		t.Fatalf("Consume with undecryptable replay material = %v, want ErrRefreshNotFound "+
			"(a plain rejection, not a reuse verdict)", err)
	}
	if errors.Is(err, ErrRefreshReused) {
		t.Fatal("undecryptable material was judged a reuse: that would revoke the legitimate " +
			"user's whole family over a key rotation")
	}
}
