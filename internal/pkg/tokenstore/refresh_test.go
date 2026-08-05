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

	store, err := NewRefreshStore(context.Background(), client, "test", ttl)
	if err != nil {
		t.Fatalf("NewRefreshStore: %v", err)
	}
	return store, srv
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

	store, _ := newTestStore(t, time.Hour)
	ctx := context.Background()

	first, err := store.Issue(ctx, 7)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	_, second, err := store.Consume(ctx, first)
	if err != nil {
		t.Fatalf("first Consume: %v", err)
	}

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

// TestConcurrentConsumeYieldsExactlyOneWinner 钉住消费的原子性。
//
// 读取、判断已用、标记已用若不在同一个脚本里，两个并发请求会同时读到
// "未使用"，于是同一个令牌被消费两次——重用检测本身就成了它要检测的漏洞。
func TestConcurrentConsumeYieldsExactlyOneWinner(t *testing.T) {
	t.Parallel()

	store, _ := newTestStore(t, time.Hour)
	ctx := context.Background()

	token, err := store.Issue(ctx, 11)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	const racers = 8
	results := make(chan error, racers)
	start := make(chan struct{})
	for range racers {
		go func() {
			<-start
			_, _, err := store.Consume(ctx, token)
			results <- err
		}()
	}
	close(start)

	var succeeded int
	for range racers {
		if err := <-results; err == nil {
			succeeded++
		}
	}

	if succeeded != 1 {
		t.Fatalf("%d concurrent consumes succeeded, want exactly 1: the token is not "+
			"single-use under concurrency", succeeded)
	}
}
