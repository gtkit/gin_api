package jwtauth

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	jwtv5 "github.com/golang-jwt/jwt/v5"
	"github.com/gtkit/gojwt/claims"
)

func TestLegacyParserRechecksCutoffAfterSuccessfulParse(t *testing.T) {
	t.Parallel()
	active, previous := newKeyPair(t, "active"), newKeyPair(t, "previous")
	keys, err := NewKeySet(active, []VerificationKey{{KID: previous.KID, Public: previous.Public}})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	cutoff := now.Add(time.Minute)
	parser, err := NewParser(keys, ParserOptions{
		Issuer: "gin-api", Audiences: []string{"app"}, UnkeyedTokenAcceptUntil: cutoff,
	}, 8)
	if err != nil {
		t.Fatal(err)
	}
	parser.now = func() time.Time { return now }
	token, err := jwtv5.NewWithClaims(jwtv5.SigningMethodEdDSA, &claims.Claims{
		ID: "legacy", TokenID: "legacy", UserID: 42,
		ExpiresAt: jwtv5.NewNumericDate(now.Add(time.Hour)),
		IssuedAt:  jwtv5.NewNumericDate(now.Add(-time.Minute)), NotBefore: jwtv5.NewNumericDate(now.Add(-time.Minute)),
	}).SignedString(previous.Private)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parser.ParseToken(t.Context(), token); err != nil {
		t.Fatalf("previous key rejected inside migration window: %v", err)
	}
	parser.now = func() time.Time { return cutoff }
	if _, err := parser.ParseToken(t.Context(), token); !errors.Is(err, ErrUnkeyedTokenRejected) {
		t.Fatalf("token accepted past migration cutoff: %v", err)
	}
}

func TestParserPreservesFractionalClaimsAndNumericExtensions(t *testing.T) {
	t.Parallel()
	_, parser, keys := newTestSetup(t, 8)
	now := time.Now().Truncate(time.Second)
	date := func(offset time.Duration) *jwtv5.NumericDate {
		return &jwtv5.NumericDate{Time: now.Add(offset + 123456789*time.Nanosecond)}
	}
	original := &claims.Claims{
		ID: "precise", TokenID: "precise", UserID: 42, Subject: "42", Issuer: "gin-api", Audience: []string{"app"},
		ExpiresAt: date(time.Hour), IssuedAt: date(-time.Minute), NotBefore: date(-time.Minute), AuthTime: date(-time.Hour),
		Ext:    map[string]any{"large": json.Number("9007199254740993")},
		Custom: map[string]any{"tenant_id": json.Number("9007199254740993")},
	}
	signed := jwtv5.NewWithClaims(jwtv5.SigningMethodEdDSA, original)
	signed.Header["kid"] = keys.ActiveKID()
	token, err := signed.SignedString(keys.SigningKey())
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		parsed, err := parser.ParseToken(t.Context(), token)
		if err != nil {
			t.Fatal(err)
		}
		if !parsed.ExpiresAt.Equal(original.ExpiresAt.Time) || !parsed.IssuedAt.Equal(original.IssuedAt.Time) ||
			!parsed.NotBefore.Equal(original.NotBefore.Time) || !parsed.AuthTime.Equal(original.AuthTime.Time) {
			t.Fatal("fractional claim time changed")
		}
		if parsed.Ext["large"] != json.Number("9007199254740993") || parsed.Custom["tenant_id"] != json.Number("9007199254740993") {
			t.Fatal("extension integer lost precision")
		}
	}
}

func TestParserUpgradeKeepsRequiredIDsAndIssuedAtPolicy(t *testing.T) {
	t.Parallel()
	_, parser, keys := newTestSetup(t, 0)
	for _, tc := range []struct {
		name, jti, tokenID string
		issuedInFuture     bool
	}{
		{name: "missing jti", tokenID: "id"},
		{name: "missing token_id", jti: "id"},
		{name: "mismatched IDs", jti: "id", tokenID: "other"},
		{name: "future iat cannot disable validation", jti: "id", tokenID: "id", issuedInFuture: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			now := time.Now()
			iat := now.Add(-time.Minute)
			if tc.issuedInFuture {
				iat = now.Add(time.Minute)
			}
			signed := jwtv5.NewWithClaims(jwtv5.SigningMethodEdDSA, &claims.Claims{
				ID: tc.jti, TokenID: tc.tokenID, UserID: 42, Subject: "42", Issuer: "gin-api", Audience: []string{"app"},
				ExpiresAt: jwtv5.NewNumericDate(now.Add(time.Hour)), IssuedAt: jwtv5.NewNumericDate(iat),
				NotBefore: jwtv5.NewNumericDate(now.Add(-time.Minute)),
			})
			signed.Header["kid"] = keys.ActiveKID()
			token, err := signed.SignedString(keys.SigningKey())
			if err != nil {
				t.Fatal(err)
			}
			if _, err := parser.ParseToken(t.Context(), token, jwtv5.WithoutClaimsValidation()); !errors.Is(err, ErrTokenInvalid) {
				t.Fatalf("expected invalid token, got %v", err)
			}
		})
	}
}
