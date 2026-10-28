package jwtauth

import (
	"crypto/ed25519"
	"crypto/rand"
	"strconv"
	"sync"
	"testing"
	"time"

	jwtv5 "github.com/golang-jwt/jwt/v5"
	"github.com/gtkit/gojwt/claims"
)

func cacheBenchmarkClaims(now time.Time) *claims.Claims {
	return &claims.Claims{
		UserID: 7, Roles: []string{"user", "reader"}, Audience: []string{"app"},
		ExpiresAt: &jwtv5.NumericDate{Time: now.Add(time.Hour)},
		IssuedAt:  &jwtv5.NumericDate{Time: now}, NotBefore: &jwtv5.NumericDate{Time: now},
		Custom: map[string]any{"tenant": map[string]any{"groups": []any{"one", "two", "three"}}},
	}
}

func BenchmarkClaimsCacheParallel(b *testing.B) {
	for _, mixed := range []bool{false, true} {
		name := "read"
		if mixed {
			name = "mixed"
		}
		b.Run(name, func(b *testing.B) {
			now := time.Now()
			original := cacheBenchmarkClaims(now)
			cache := newClaimsCache(1024)
			keys := make([]string, 128)
			for i := range keys {
				keys[i] = "token-" + strconv.Itoa(i)
				cache.put(keys[i], original)
			}
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				i := 0
				for pb.Next() {
					key := keys[i%len(keys)]
					if mixed && i%16 == 0 {
						cache.put(key, original)
					} else if _, ok := cache.get(key, now); !ok {
						b.Error("cache miss")
					}
					i++
				}
			})
		})
	}
}

func BenchmarkJWTVerificationParallel(b *testing.B) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		b.Fatal(err)
	}
	keys, err := NewKeySet(SigningKey{KID: "bench", Public: pub, Private: priv}, nil)
	if err != nil {
		b.Fatal(err)
	}
	for _, entries := range []int{0, 1024} {
		b.Run(strconv.Itoa(entries), func(b *testing.B) {
			parser, err := NewParser(keys, ParserOptions{Issuer: "benchmark", Audiences: []string{"app"}}, entries)
			if err != nil {
				b.Fatal(err)
			}
			issuer, err := NewIssuer(keys, parser, IssuerOptions{Issuer: "benchmark", Audiences: []string{"app"}, TTL: time.Hour, Prv: "app"})
			if err != nil {
				b.Fatal(err)
			}
			token, err := issuer.Issue(b.Context(), 7, "user")
			if err != nil {
				b.Fatal(err)
			}
			if _, err := parser.ParseToken(b.Context(), token); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				for pb.Next() {
					if _, err := parser.ParseToken(b.Context(), token); err != nil {
						b.Error(err)
					}
				}
			})
		})
	}
}

func TestClaimsCacheConcurrentMutationIsolation(t *testing.T) {
	t.Parallel()
	now := time.Now()
	cache := newClaimsCache(2)
	original := cacheBenchmarkClaims(now)
	cache.put("token", original)
	var workers sync.WaitGroup
	for range 32 {
		workers.Go(func() {
			for range 50 {
				value, ok := cache.get("token", now)
				if !ok {
					t.Error("cache miss")
					return
				}
				value.Roles[0] = "changed"
				value.ExpiresAt.Time = now
				tenant, ok := value.Custom["tenant"].(map[string]any)
				if !ok {
					t.Error("tenant missing")
					return
				}
				groups, ok := tenant["groups"].([]any)
				if !ok {
					t.Error("groups missing")
					return
				}
				groups[0] = "changed"
				cache.put("token", original)
			}
		})
	}
	workers.Wait()
	value, ok := cache.get("token", now)
	if !ok || value.Roles[0] != "user" || !value.ExpiresAt.Equal(now.Add(time.Hour)) {
		t.Fatal("shared cache state was modified")
	}
	if _, ok := cache.get("token", now.Add(time.Hour)); ok {
		t.Fatal("expired entry returned")
	}
}
