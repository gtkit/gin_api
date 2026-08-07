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

func newTestRevocationList(t *testing.T) (*RevocationList, *miniredis.Miniredis) {
	t.Helper()
	testutil.RequireLoopback(t)

	srv := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: srv.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	list, err := NewRevocationList(client, "test", 0)
	if err != nil {
		t.Fatalf("NewRevocationList: %v", err)
	}
	return list, srv
}

func TestRevokeMakesTokenRevoked(t *testing.T) {
	t.Parallel()

	list, _ := newTestRevocationList(t)
	ctx := context.Background()

	revoked, err := list.IsRevoked(ctx, "jti-1")
	if err != nil {
		t.Fatalf("IsRevoked: %v", err)
	}
	if revoked {
		t.Fatal("a token nobody revoked reports as revoked")
	}

	if err := list.Revoke(ctx, "jti-1", time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	revoked, err = list.IsRevoked(ctx, "jti-1")
	if err != nil {
		t.Fatalf("IsRevoked after revoke: %v", err)
	}
	if !revoked {
		t.Fatal("the token is still accepted after logout")
	}

	// 撤销一个令牌不影响别的令牌。
	other, err := list.IsRevoked(ctx, "jti-2")
	if err != nil {
		t.Fatalf("IsRevoked for another token: %v", err)
	}
	if other {
		t.Error("revoking one token revoked another: everyone would be logged out at once")
	}
}

// TestRevocationExpiresWithTheToken 钉住撤销表不会无限增长。
//
// 记录活得比令牌久没有任何用处——那个令牌早已因为过期而不被接受——
// 却会让这张表变成只增不减的一张表。
func TestRevocationExpiresWithTheToken(t *testing.T) {
	t.Parallel()

	list, srv := newTestRevocationList(t)
	ctx := context.Background()

	if err := list.Revoke(ctx, "short-lived", time.Now().Add(time.Minute)); err != nil {
		t.Fatalf("Revoke: %v", err)
	}

	srv.FastForward(2 * time.Minute)

	revoked, err := list.IsRevoked(ctx, "short-lived")
	if err != nil {
		t.Fatalf("IsRevoked: %v", err)
	}
	if revoked {
		t.Fatal("the revocation record outlived the token it revoked: the list would grow forever")
	}
}

// TestRevokingAnExpiredTokenIsANoOp 覆盖过期令牌的撤销。
//
// 它本来就不会再被接受，为它写一条立即过期的记录只是白白多一次往返。
func TestRevokingAnExpiredTokenIsANoOp(t *testing.T) {
	t.Parallel()

	list, srv := newTestRevocationList(t)
	ctx := context.Background()

	if err := list.Revoke(ctx, "already-expired", time.Now().Add(-time.Minute)); err != nil {
		t.Fatalf("revoking an already-expired token should succeed silently: %v", err)
	}
	if len(srv.Keys()) != 0 {
		t.Errorf("writing a record for an already-expired token: keys = %v", srv.Keys())
	}
}

// TestTokenWithoutIDIsAnError 钉住无 jti 令牌不被静默放行。
//
// 没有 jti 就无法撤销，也就无法证明它没被撤销。静默当作"未撤销"会让
// 一个签发侧的缺陷变成校验侧永久的绕过口。
func TestTokenWithoutIDIsAnError(t *testing.T) {
	t.Parallel()

	list, _ := newTestRevocationList(t)

	_, err := list.IsRevoked(context.Background(), "")
	if err == nil {
		t.Fatal("a token with no id passed the revocation check: it must be an error, " +
			"not a silent pass")
	}
}

// TestStoredKeyHoldsNoRawTokenID 钉住撤销表里不存原始 jti。
func TestStoredKeyHoldsNoRawTokenID(t *testing.T) {
	t.Parallel()

	list, srv := newTestRevocationList(t)
	const tokenID = "distinctive-token-id"

	if err := list.Revoke(context.Background(), tokenID, time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	for _, key := range srv.Keys() {
		if strings.Contains(key, tokenID) {
			t.Fatalf("key %q contains the raw token id", key)
		}
	}
}

// TestStoreFailureIsNotSilentlyTreatedAsNotRevoked 钉住故障不降级为放行。
//
// 把查询失败当作"未撤销"，会让一次 Redis 抖动变成"所有已登出的令牌
// 重新生效"——恰恰在最需要它生效的时候失效。
func TestStoreFailureIsNotSilentlyTreatedAsNotRevoked(t *testing.T) {
	t.Parallel()

	list, srv := newTestRevocationList(t)
	srv.Close() // 模拟 Redis 不可达

	_, err := list.IsRevoked(context.Background(), "jti")
	if err == nil {
		t.Fatal("a failed revocation lookup reported \"not revoked\": a Redis blip would " +
			"silently resurrect every logged-out token")
	}
	if errors.Is(err, ErrRefreshNotFound) {
		t.Error("infrastructure failure was mapped to a domain error")
	}
}

// TestRevocationOutlivesClockSkew 钉住撤销记录覆盖时钟偏移
// （local-authentication spec）：验签用 leeway 接受"刚过期一点"的令牌，
// 若记录只存到原始 exp，已登出的令牌会在偏移窗口内重新被接受——
// 撤销在它最需要生效的时刻静默失效。
func TestRevocationOutlivesClockSkew(t *testing.T) {
	t.Parallel()

	const skew = 30 * time.Second
	client := &recordingRevocationClient{}
	list, err := NewRevocationList(client, "test", skew)
	if err != nil {
		t.Fatalf("NewRevocationList: %v", err)
	}
	now := time.Date(2026, 8, 7, 12, 0, 0, 0, time.UTC)
	list.now = func() time.Time { return now }

	// 令牌 60 秒后过期：记录应活 60s + 30s。
	if err := list.Revoke(context.Background(), "jti-1", now.Add(time.Minute)); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if want := time.Minute + skew; client.lastTTL != want {
		t.Fatalf("record ttl = %s, want %s (exp + clock skew)", client.lastTTL, want)
	}

	// 令牌刚过期但仍在偏移窗口内：仍必须写记录，否则它会被验签重新接受。
	client.lastTTL = 0
	if err := list.Revoke(context.Background(), "jti-2", now.Add(-time.Second)); err != nil {
		t.Fatalf("Revoke within the skew window: %v", err)
	}
	if client.lastTTL <= 0 {
		t.Fatal("no record was written for a token still inside the skew window: " +
			"verification would accept it again")
	}

	// 整个窗口都已过去：不必再写。
	client.lastTTL = 0
	if err := list.Revoke(context.Background(), "jti-3", now.Add(-2*skew)); err != nil {
		t.Fatalf("Revoke past the skew window: %v", err)
	}
	if client.lastTTL != 0 {
		t.Fatalf("a record was written past the whole window (ttl=%s)", client.lastTTL)
	}
}

// recordingRevocationClient 记录最后一次写入的 TTL。
type recordingRevocationClient struct {
	lastTTL time.Duration
}

func (c *recordingRevocationClient) Set(
	ctx context.Context, _ string, _ any, ttl time.Duration,
) *redis.StatusCmd {
	c.lastTTL = ttl
	return redis.NewStatusResult("OK", nil)
}

func (c *recordingRevocationClient) Exists(ctx context.Context, _ ...string) *redis.IntCmd {
	return redis.NewIntResult(0, nil)
}
