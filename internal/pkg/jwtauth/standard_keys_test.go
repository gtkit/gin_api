package jwtauth

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"path/filepath"
	"strings"
	"testing"
	"time"

	jwtv5 "github.com/golang-jwt/jwt/v5"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gojwt"
	"github.com/gtkit/gojwt/claims"
)

func TestStandardPEMAssemblyAndGoJWTInterop(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name, mode string
		hours      int
		ttl        time.Duration
	}{
		{"local default", "local", 0, time.Hour},
		{"explicit lifetime", "local", 12, 12 * time.Hour},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			dir := t.TempDir()
			privatePath, publicPath := filepath.Join(dir, "private.pem"), filepath.Join(dir, "public.pem")
			if err := gojwt.GenerateEd25519Keys(privatePath, publicPath); err != nil {
				t.Fatal(err)
			}
			cfg := &config.Config{}
			cfg.Auth.Mode = config.AuthMode(tc.mode)
			cfg.JWT = config.JWTConfig{Active: config.JWTKeyConfig{KID: "interop", PrivateKeyPath: privatePath, PublicKeyPath: publicPath}, Issuer: "test-issuer", Audiences: []string{"test-client"}, TokenTTLHours: tc.hours}
			assembly, err := Assemble(cfg)
			if err != nil {
				t.Fatal(err)
			}
			if assembly.Issuer == nil {
				t.Fatal("local assembly returned no issuer")
			}
			token, err := assembly.Issuer.Issue(t.Context(), 42, "user")
			if err != nil {
				t.Fatal(err)
			}
			c, err := assembly.Parser.ParseToken(t.Context(), token)
			if err != nil {
				t.Fatal(err)
			}
			if c.ExpiresAt.Sub(c.IssuedAt.Time) != tc.ttl {
				t.Fatalf("token TTL = %v, want %v", c.ExpiresAt.Sub(c.IssuedAt.Time), tc.ttl)
			}
			if c.Subject != "42" || c.UserID != 42 || c.ID != c.TokenID || len(c.ID) != 32 || len(c.Roles) != 1 || c.Roles[0] != "user" {
				t.Fatal("identity claims changed")
			}
			verifier, err := gojwt.NewJwtEd25519VerifierFromFile(publicPath)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := verifier.ParseToken(token, jwtv5.WithIssuer("test-issuer"), jwtv5.WithAudience("test-client")); err != nil {
				t.Fatalf("gojwt verifier: %v", err)
			}
			public, err := LoadEd25519PublicKey(publicPath)
			if err != nil {
				t.Fatal(err)
			}
			parts := strings.Split(token, ".")
			signature, err := base64.RawURLEncoding.DecodeString(parts[2])
			if err != nil {
				t.Fatal(err)
			}
			if !ed25519.Verify(public, []byte(parts[0]+"."+parts[1]), signature) {
				t.Fatal("standard library rejected signature")
			}
		})
	}
}

func TestCacheCopiesGoJWTExpandedClaims(t *testing.T) {
	t.Parallel()
	_, parser, keys := newTestSetup(t, 8)
	signer, err := gojwt.NewJwtEd25519FromKeys(keys.SigningKey(), keys.active.Public, gojwt.WithKeyID(keys.ActiveKID()))
	if err != nil {
		t.Fatal(err)
	}
	token, err := signer.GenerateToken(7, claims.WithIssuer("gin-api"), claims.WithAudience("app"), claims.WithSubject("7"), claims.WithExt("tenant", map[string]any{"roles": []any{"user"}}))
	if err != nil {
		t.Fatal(err)
	}
	first, err := parser.ParseToken(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}
	authTime := first.AuthTime.Time
	firstTenant, ok := first.Ext["tenant"].(map[string]any)
	if !ok {
		t.Fatalf("tenant type %T", first.Ext["tenant"])
	}
	firstRoles, ok := firstTenant["roles"].([]any)
	if !ok || len(firstRoles) != 1 {
		t.Fatalf("roles=%v", firstTenant["roles"])
	}
	firstRoles[0] = "admin"
	first.AuthTime.Time = time.Time{}
	second, err := parser.ParseToken(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}
	secondTenant, ok := second.Ext["tenant"].(map[string]any)
	if !ok {
		t.Fatalf("cached tenant type %T", second.Ext["tenant"])
	}
	secondRoles, ok := secondTenant["roles"].([]any)
	if !ok || len(secondRoles) != 1 || secondRoles[0] != "user" || !second.AuthTime.Equal(authTime) {
		t.Fatal("caller mutation reached cached claims")
	}
}

func TestNewIssuerRejectsInconsistentPrivateSeed(t *testing.T) {
	t.Parallel()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	private[0] ^= 1
	keys, err := NewKeySet(SigningKey{KID: "bad-seed", Private: private, Public: public}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := NewIssuer(keys, nil, IssuerOptions{Issuer: "test", Audiences: []string{"test"}, TTL: time.Hour}); err == nil {
		t.Fatal("issuer accepted a private key with an inconsistent seed")
	}
}
