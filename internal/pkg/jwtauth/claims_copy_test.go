package jwtauth

import (
	"testing"
	"time"

	jwtv5 "github.com/golang-jwt/jwt/v5"
	"github.com/gtkit/gojwt/claims"
)

func TestClaimsCacheReturnsIndependentDatesAndCustomValues(t *testing.T) {
	t.Parallel()
	now := time.Now()
	date := func(offset time.Duration) *jwtv5.NumericDate {
		return &jwtv5.NumericDate{Time: now.Add(offset)}
	}
	original := &claims.Claims{
		ExpiresAt: date(time.Hour), IssuedAt: date(-time.Minute), NotBefore: date(-time.Minute),
		AuthTime: date(-time.Hour),
		Custom:   map[string]any{"tenant": map[string]any{"groups": []any{"original"}}},
	}
	cache := newClaimsCache(1)
	cache.put("token", original)

	mutate := func(value *claims.Claims) {
		value.ExpiresAt.Time = now
		value.IssuedAt.Time = now
		value.NotBefore.Time = now
		value.AuthTime.Time = now
		tenant, ok := value.Custom["tenant"].(map[string]any)
		if !ok {
			t.Fatal("custom tenant missing")
		}
		groups, ok := tenant["groups"].([]any)
		if !ok {
			t.Fatal("custom groups missing")
		}
		groups[0] = "changed"
	}
	mutate(original)
	for range 2 {
		cached, ok := cache.get("token", now)
		if !ok {
			t.Fatal("cached token unexpectedly expired")
		}
		if !cached.ExpiresAt.Equal(now.Add(time.Hour)) || !cached.IssuedAt.Equal(now.Add(-time.Minute)) ||
			!cached.NotBefore.Equal(now.Add(-time.Minute)) || !cached.AuthTime.Equal(now.Add(-time.Hour)) {
			t.Fatal("claim dates share mutable cache state")
		}
		tenant, ok := cached.Custom["tenant"].(map[string]any)
		if !ok {
			t.Fatal("custom tenant missing")
		}
		groups, ok := tenant["groups"].([]any)
		if !ok || len(groups) != 1 || groups[0] != "original" {
			t.Fatal("custom values share mutable cache state")
		}
		mutate(cached)
	}
}
