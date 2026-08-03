package jwtauth

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	jwtv5 "github.com/golang-jwt/jwt/v5"
	"github.com/gtkit/gojwt/claims"
)

func newKeyPair(t *testing.T, kid string) SigningKey {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	return SigningKey{KID: kid, Private: priv, Public: pub}
}

func newTestSetup(t *testing.T, cacheEntries int) (*Issuer, *Parser, *KeySet) {
	t.Helper()

	active := newKeyPair(t, "kid-active")
	keys, err := NewKeySet(active, nil)
	if err != nil {
		t.Fatalf("NewKeySet: %v", err)
	}

	parser, err := NewParser(keys, ParserOptions{
		Issuer: "gin-api", Audiences: []string{"app"},
	}, cacheEntries)
	if err != nil {
		t.Fatalf("NewParser: %v", err)
	}

	issuer, err := NewIssuer(keys, parser, IssuerOptions{
		Issuer: "gin-api", Audiences: []string{"app"}, TTL: time.Hour, Prv: "app",
	})
	if err != nil {
		t.Fatalf("NewIssuer: %v", err)
	}
	return issuer, parser, keys
}

// TestKIDValidation 覆盖 kid 字符集。
//
// 不允许路径分隔符是硬要求：kid 参与构造密钥文件名，允许 `/` 或 `..`
// 等于让配置能写到目录之外。
func TestKIDValidation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		kid  string
		want bool
	}{
		{kid: "k2026a", want: true},
		{kid: "key.2026-01_v2", want: true},
		{kid: "", want: false},
		{kid: "with/slash", want: false},
		{kid: "../escape", want: false},
		{kid: "with space", want: false},
		{kid: strings.Repeat("a", 65), want: false},
	}

	for _, tc := range cases {
		t.Run(tc.kid, func(t *testing.T) {
			t.Parallel()
			err := ValidateKID(tc.kid)
			if tc.want && err != nil {
				t.Fatalf("ValidateKID(%q) = %v, want nil", tc.kid, err)
			}
			if !tc.want && err == nil {
				t.Fatalf("ValidateKID(%q) = nil, want error", tc.kid)
			}
		})
	}
}

// TestKeySetRejectsMismatchedActivePair 钉住公私钥配对实测。
//
// 长度对但不配对的一组密钥会让签发成功、验签全败——而那在启动期
// 完全看不出来，只有第一个真实请求才会暴露。
func TestKeySetRejectsMismatchedActivePair(t *testing.T) {
	t.Parallel()

	a := newKeyPair(t, "kid-a")
	b := newKeyPair(t, "kid-b")

	_, err := NewKeySet(SigningKey{KID: "kid-a", Private: a.Private, Public: b.Public}, nil)
	if err == nil {
		t.Fatal("NewKeySet accepted a mismatched key pair")
	}
	if !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("error %q does not explain the mismatch", err)
	}
}

// TestKeySetRejectsDuplicateKID 钉住重复 kid 启动失败。
//
// 重复 kid 让"按 kid 精确选键"失去确定性：同一个 kid 对应两把不同公钥时，
// 命中哪一把取决于遍历顺序。
func TestKeySetRejectsDuplicateKID(t *testing.T) {
	t.Parallel()

	active := newKeyPair(t, "same")
	other := newKeyPair(t, "same")

	_, err := NewKeySet(active, []VerificationKey{{KID: "same", Public: other.Public}})
	if err == nil {
		t.Fatal("NewKeySet accepted a duplicate kid")
	}
	if !strings.Contains(err.Error(), "duplicate kid") {
		t.Fatalf("error %q does not report the duplicate", err)
	}
}

// TestKeySetBoundsPreviousKeys 钉住 previous 数量上限。
//
// 无 kid 的存量令牌只能按顺序逐个尝试，没有上限就是 O(N) 且 N 由配置决定。
func TestKeySetBoundsPreviousKeys(t *testing.T) {
	t.Parallel()

	active := newKeyPair(t, "active")
	previous := make([]VerificationKey, 0, maxPreviousKeys+1)
	for i := range maxPreviousKeys + 1 {
		k := newKeyPair(t, "prev-"+string(rune('a'+i)))
		previous = append(previous, VerificationKey{KID: k.KID, Public: k.Public})
	}

	if _, err := NewKeySet(active, previous); err == nil {
		t.Fatalf("NewKeySet accepted %d previous keys, the bound is %d", len(previous), maxPreviousKeys)
	}
}

// TestIssueAndParseRoundTrip 验证签发的令牌能被验回，且 claims 齐全。
func TestIssueAndParseRoundTrip(t *testing.T) {
	t.Parallel()

	issuer, parser, keys := newTestSetup(t, 0)
	ctx := context.Background()

	token, err := issuer.Issue(ctx, 42, "user")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	parsed, err := parser.ParseToken(ctx, token)
	if err != nil {
		t.Fatalf("ParseToken: %v", err)
	}

	if parsed.UserID != 42 {
		t.Fatalf("uid = %d, want 42", parsed.UserID)
	}
	if parsed.Subject != "42" {
		t.Fatalf("sub = %q, want the decimal uid", parsed.Subject)
	}
	if parsed.Issuer != "gin-api" {
		t.Fatalf("iss = %q", parsed.Issuer)
	}
	// jti 与 token_id 必须相等：它们是撤销与审计的锚点，分叉意味着
	// "按 ID 撤销"会出现两个都对不上的候选值。
	if parsed.ID == "" || parsed.ID != parsed.TokenID {
		t.Fatalf("jti=%q token_id=%q, want both present and equal", parsed.ID, parsed.TokenID)
	}
	if len(parsed.Roles) != 1 || parsed.Roles[0] != "user" {
		t.Fatalf("roles = %v", parsed.Roles)
	}

	// header 必须带 active kid——否则轮换失去全部基础。
	unverified, _, err := jwtv5.NewParser().ParseUnverified(token, &claims.Claims{})
	if err != nil {
		t.Fatalf("ParseUnverified: %v", err)
	}
	if got := headerKID(unverified); got != keys.ActiveKID() {
		t.Fatalf("header kid = %q, want %q", got, keys.ActiveKID())
	}
}

// TestPreviousKeyStillVerifies 钉住轮换的核心收益：旧密钥签的令牌仍可验。
//
// 没有它，换一次公钥就会让全部存量令牌立刻失效——默认 TTL 30 天，
// 最坏是 30 天内签发的全体用户被强制重登。
func TestPreviousKeyStillVerifies(t *testing.T) {
	t.Parallel()

	oldKey := newKeyPair(t, "kid-old")
	oldSet, err := NewKeySet(oldKey, nil)
	if err != nil {
		t.Fatalf("NewKeySet(old): %v", err)
	}
	oldParser, err := NewParser(oldSet, ParserOptions{Issuer: "gin-api", Audiences: []string{"app"}}, 0)
	if err != nil {
		t.Fatalf("NewParser(old): %v", err)
	}
	oldIssuer, err := NewIssuer(oldSet, oldParser, IssuerOptions{
		Issuer: "gin-api", Audiences: []string{"app"}, TTL: time.Hour,
	})
	if err != nil {
		t.Fatalf("NewIssuer(old): %v", err)
	}

	ctx := context.Background()
	laravelToken, err := oldIssuer.Issue(ctx, 7)
	if err != nil {
		t.Fatalf("Issue with the old key: %v", err)
	}

	// 轮换：新 active，旧公钥进 previous。
	newKey := newKeyPair(t, "kid-new")
	rotated, err := NewKeySet(newKey, []VerificationKey{{KID: oldKey.KID, Public: oldKey.Public}})
	if err != nil {
		t.Fatalf("NewKeySet(rotated): %v", err)
	}
	rotatedParser, err := NewParser(rotated, ParserOptions{Issuer: "gin-api", Audiences: []string{"app"}}, 0)
	if err != nil {
		t.Fatalf("NewParser(rotated): %v", err)
	}

	if _, err := rotatedParser.ParseToken(ctx, laravelToken); err != nil {
		t.Fatalf("a token signed by the previous key no longer verifies: %v", err)
	}
}

// TestUnknownKIDIsRejectedWithoutFallback 钉住"未知 kid 直接拒绝"。
//
// 回退遍历会让 kid 形同虚设：攻击者把 header 里的 kid 改成任意值，
// 验签就退回"逐个试"，而那正是引入 kid 要消灭的行为。
func TestUnknownKIDIsRejectedWithoutFallback(t *testing.T) {
	t.Parallel()

	issuer, _, _ := newTestSetup(t, 0)
	ctx := context.Background()

	token, err := issuer.Issue(ctx, 1)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// 换一个 parser：它的密钥集里没有签发用的那个 kid。
	other := newKeyPair(t, "kid-other")
	otherSet, err := NewKeySet(other, nil)
	if err != nil {
		t.Fatalf("NewKeySet: %v", err)
	}
	otherParser, err := NewParser(otherSet, ParserOptions{Issuer: "gin-api", Audiences: []string{"app"}}, 0)
	if err != nil {
		t.Fatalf("NewParser: %v", err)
	}

	_, err = otherParser.ParseToken(ctx, token)
	if !errors.Is(err, ErrUnknownKID) {
		t.Fatalf("error = %v, want ErrUnknownKID (no fallback traversal)", err)
	}
}

// TestNoKIDTokenRequiresExplicitCutoff 钉住无 kid 存量令牌的接受窗口。
//
// 默认不接受：一个没有截止时刻的兼容口会变成永久的。
func TestNoKIDTokenRequiresExplicitCutoff(t *testing.T) {
	t.Parallel()

	key := newKeyPair(t, "kid-a")
	set, err := NewKeySet(key, nil)
	if err != nil {
		t.Fatalf("NewKeySet: %v", err)
	}

	// 手工签一个**不带 kid** 的令牌，模拟存量。
	now := time.Now()
	laravel := jwtv5.NewWithClaims(jwtv5.SigningMethodEdDSA, &claims.Claims{
		RegisteredClaims: jwtv5.RegisteredClaims{
			ID:        "abc",
			IssuedAt:  jwtv5.NewNumericDate(now),
			NotBefore: jwtv5.NewNumericDate(now),
			ExpiresAt: jwtv5.NewNumericDate(now.Add(time.Hour)),
		},
		UserID:  9,
		TokenID: "abc",
	})
	token, err := laravel.SignedString(key.Private)
	if err != nil {
		t.Fatalf("sign laravel token: %v", err)
	}

	ctx := context.Background()

	t.Run("rejected without a cutoff", func(t *testing.T) {
		parser, err := NewParser(set, ParserOptions{Issuer: "gin-api", Audiences: []string{"app"}}, 0)
		if err != nil {
			t.Fatalf("NewParser: %v", err)
		}
		if _, err := parser.ParseToken(ctx, token); !errors.Is(err, ErrUnkeyedTokenRejected) {
			t.Fatalf("error = %v, want ErrUnkeyedTokenRejected", err)
		}
	})

	t.Run("accepted before the cutoff", func(t *testing.T) {
		parser, err := NewParser(set, ParserOptions{
			Issuer: "gin-api", Audiences: []string{"app"},
			UnkeyedTokenAcceptUntil: time.Now().Add(time.Hour),
		}, 0)
		if err != nil {
			t.Fatalf("NewParser: %v", err)
		}
		if _, err := parser.ParseToken(ctx, token); err != nil {
			t.Fatalf("a laravel token was rejected inside the acceptance window: %v", err)
		}
	})

	t.Run("rejected after the cutoff", func(t *testing.T) {
		parser, err := NewParser(set, ParserOptions{
			Issuer: "gin-api", Audiences: []string{"app"},
			UnkeyedTokenAcceptUntil: time.Now().Add(-time.Hour),
		}, 0)
		if err != nil {
			t.Fatalf("NewParser: %v", err)
		}
		if _, err := parser.ParseToken(ctx, token); !errors.Is(err, ErrUnkeyedTokenRejected) {
			t.Fatalf("error = %v, want ErrUnkeyedTokenRejected after the window closed", err)
		}
	})
}

// TestParserRejectsWrongAudienceAndIssuer 钉住作用域校验。
func TestParserRejectsWrongAudienceAndIssuer(t *testing.T) {
	t.Parallel()

	issuer, _, keys := newTestSetup(t, 0)
	ctx := context.Background()

	token, err := issuer.Issue(ctx, 5)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	cases := []struct {
		name string
		opts ParserOptions
	}{
		{name: "wrong issuer", opts: ParserOptions{Issuer: "other", Audiences: []string{"app"}}},
		{name: "wrong audience", opts: ParserOptions{Issuer: "gin-api", Audiences: []string{"admin"}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			parser, err := NewParser(keys, tc.opts, 0)
			if err != nil {
				t.Fatalf("NewParser: %v", err)
			}
			if _, err := parser.ParseToken(ctx, token); err == nil {
				t.Fatal("a token with a mismatched scope was accepted")
			}
		})
	}
}

// TestParserRejectsExcessiveClockSkew 钉住偏移上限。
//
// 偏移窗口直接放宽 exp/nbf 判定，一个写成 24h 的配置等于把过期检查关掉。
func TestParserRejectsExcessiveClockSkew(t *testing.T) {
	t.Parallel()

	_, _, keys := newTestSetup(t, 0)

	_, err := NewParser(keys, ParserOptions{
		Issuer: "gin-api", Audiences: []string{"app"}, ClockSkew: time.Hour,
	}, 0)
	if err == nil {
		t.Fatal("NewParser accepted a clock skew above the ceiling")
	}
	if !strings.Contains(err.Error(), "ceiling") {
		t.Fatalf("error %q does not explain the ceiling", err)
	}
}

// TestParserRejectsNegativeCacheSize 验证负容量即配置错误。
func TestParserRejectsNegativeCacheSize(t *testing.T) {
	t.Parallel()

	_, _, keys := newTestSetup(t, 0)
	if _, err := NewParser(keys, ParserOptions{Issuer: "i", Audiences: []string{"a"}}, -1); err == nil {
		t.Fatal("NewParser accepted a negative cache size")
	}
}

// TestRefreshRotatesToActiveKID 验证刷新把旧 kid 的令牌换成新 kid。
//
// 这是轮换的收敛机制：活跃用户在重叠窗口内自然迁移到新密钥，无需强制重登。
func TestRefreshRotatesToActiveKID(t *testing.T) {
	t.Parallel()

	oldKey := newKeyPair(t, "kid-old")
	oldSet, _ := NewKeySet(oldKey, nil)
	oldParser, _ := NewParser(oldSet, ParserOptions{Issuer: "gin-api", Audiences: []string{"app"}}, 0)
	oldIssuer, _ := NewIssuer(oldSet, oldParser, IssuerOptions{
		Issuer: "gin-api", Audiences: []string{"app"}, TTL: time.Hour,
	})

	ctx := context.Background()
	laravelToken, err := oldIssuer.Issue(ctx, 11, "user")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	newKey := newKeyPair(t, "kid-new")
	rotated, _ := NewKeySet(newKey, []VerificationKey{{KID: oldKey.KID, Public: oldKey.Public}})
	rotatedParser, _ := NewParser(rotated, ParserOptions{Issuer: "gin-api", Audiences: []string{"app"}}, 0)
	rotatedIssuer, _ := NewIssuer(rotated, rotatedParser, IssuerOptions{
		Issuer: "gin-api", Audiences: []string{"app"}, TTL: time.Hour,
	})

	refreshed, err := rotatedIssuer.Refresh(ctx, laravelToken)
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}

	unverified, _, err := jwtv5.NewParser().ParseUnverified(refreshed, &claims.Claims{})
	if err != nil {
		t.Fatalf("ParseUnverified: %v", err)
	}
	if got := headerKID(unverified); got != "kid-new" {
		t.Fatalf("refreshed token kid = %q, want kid-new", got)
	}

	parsed, err := rotatedParser.ParseToken(ctx, refreshed)
	if err != nil {
		t.Fatalf("parse refreshed token: %v", err)
	}
	if parsed.UserID != 11 {
		t.Fatalf("uid = %d, want 11", parsed.UserID)
	}
	// 刷新必须轮换 ID：沿用旧 ID 会让"撤销旧令牌"连带撤销新令牌。
	if parsed.ID == "" || parsed.ID != parsed.TokenID {
		t.Fatalf("refreshed jti=%q token_id=%q", parsed.ID, parsed.TokenID)
	}
}

// TestCacheHitReturnsIndependentCopies 钉住缓存返回深拷贝。
//
// 共享指针的话，任何一个调用方修改返回值都会污染后续所有命中，
// 而那种污染取决于请求顺序，在测试里几乎不可能复现。
func TestCacheHitReturnsIndependentCopies(t *testing.T) {
	t.Parallel()

	issuer, parser, _ := newTestSetup(t, 8)
	ctx := context.Background()

	token, err := issuer.Issue(ctx, 3, "user")
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	first, err := parser.ParseToken(ctx, token)
	if err != nil {
		t.Fatalf("first parse: %v", err)
	}
	first.Roles[0] = "tampered"
	first.UserID = 999

	second, err := parser.ParseToken(ctx, token)
	if err != nil {
		t.Fatalf("second parse: %v", err)
	}
	if second.UserID != 3 {
		t.Fatalf("cached uid = %d, want 3: the cache returned a shared pointer", second.UserID)
	}
	if second.Roles[0] != "user" {
		t.Fatalf("cached roles = %v, want the original value", second.Roles)
	}
}

// TestCacheIsBounded 验证 LRU 容量上限生效。
func TestCacheIsBounded(t *testing.T) {
	t.Parallel()

	const capacity = 3
	issuer, parser, _ := newTestSetup(t, capacity)
	ctx := context.Background()

	for i := range capacity * 3 {
		token, err := issuer.Issue(ctx, int64(i+1))
		if err != nil {
			t.Fatalf("Issue %d: %v", i, err)
		}
		if _, err := parser.ParseToken(ctx, token); err != nil {
			t.Fatalf("parse %d: %v", i, err)
		}
	}

	if got := parser.cache.len(); got > capacity {
		t.Fatalf("cache holds %d entries, the bound is %d", got, capacity)
	}
}

// TestParserOptionsBypassCache 钉住"带 ParserOption 时绕过缓存"。
//
// 那些选项会改变单次解析的判定条件，而缓存里的结果是按默认条件验出来的。
// 命中缓存等于用宽松条件的结果冒充严格条件的结论。
func TestParserOptionsBypassCache(t *testing.T) {
	t.Parallel()

	issuer, parser, _ := newTestSetup(t, 8)
	ctx := context.Background()

	token, err := issuer.Issue(ctx, 4)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	// 先填一次缓存。
	if _, err := parser.ParseToken(ctx, token); err != nil {
		t.Fatalf("warm parse: %v", err)
	}
	if parser.cache.len() != 1 {
		t.Fatalf("cache entries = %d, want 1 after a plain parse", parser.cache.len())
	}

	// 带一个更严格的选项：必须真正重新解析并失败，而不是命中缓存返回成功。
	_, err = parser.ParseToken(ctx, token, jwtv5.WithIssuer("someone-else"))
	if err == nil {
		t.Fatal("a stricter ParserOption was satisfied from the cache")
	}
}

// TestCacheDoesNotOutliveTokenExpiry 验证缓存不让过期令牌继续可用。
func TestCacheDoesNotOutliveTokenExpiry(t *testing.T) {
	t.Parallel()

	active := newKeyPair(t, "kid")
	keys, _ := NewKeySet(active, nil)
	parser, err := NewParser(keys, ParserOptions{Issuer: "gin-api", Audiences: []string{"app"}}, 8)
	if err != nil {
		t.Fatalf("NewParser: %v", err)
	}

	// 直接往缓存里放一个已经过期的条目。
	expired := &claims.Claims{
		RegisteredClaims: jwtv5.RegisteredClaims{
			ExpiresAt: jwtv5.NewNumericDate(time.Now().Add(-time.Minute)),
		},
		UserID: 1,
	}
	parser.cache.put("token", expired)

	if _, ok := parser.cache.get("token", time.Now()); ok {
		t.Fatal("the cache returned an entry past the token's expiry")
	}
}

// TestCacheIsRaceFree 在并发下访问缓存。
func TestCacheIsRaceFree(t *testing.T) {
	t.Parallel()

	issuer, parser, _ := newTestSetup(t, 16)
	ctx := context.Background()

	tokens := make([]string, 0, 8)
	for i := range 8 {
		token, err := issuer.Issue(ctx, int64(i+1))
		if err != nil {
			t.Fatalf("Issue: %v", err)
		}
		tokens = append(tokens, token)
	}

	var wg sync.WaitGroup
	wg.Add(len(tokens) * 4)
	for range 4 {
		for _, token := range tokens {
			go func() {
				defer wg.Done()
				if _, err := parser.ParseToken(ctx, token); err != nil {
					t.Errorf("concurrent parse: %v", err)
				}
			}()
		}
	}
	wg.Wait()
}

// TestParseRespectsCanceledContext 验证 ctx 取消时不做无谓工作。
func TestParseRespectsCanceledContext(t *testing.T) {
	t.Parallel()

	issuer, parser, _ := newTestSetup(t, 0)
	token, err := issuer.Issue(context.Background(), 1)
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if _, err := parser.ParseToken(ctx, token); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

// TestIssuerRequiresScope 验证 issuer/audiences 必填。
func TestIssuerRequiresScope(t *testing.T) {
	t.Parallel()

	_, _, keys := newTestSetup(t, 0)

	cases := []struct {
		name string
		opts IssuerOptions
	}{
		{name: "missing issuer", opts: IssuerOptions{Audiences: []string{"app"}, TTL: time.Hour}},
		{name: "missing audiences", opts: IssuerOptions{Issuer: "gin-api", TTL: time.Hour}},
		{name: "non-positive ttl", opts: IssuerOptions{Issuer: "gin-api", Audiences: []string{"app"}}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := NewIssuer(keys, nil, tc.opts); err == nil {
				t.Fatal("NewIssuer accepted an incomplete scope declaration")
			}
		})
	}
}
