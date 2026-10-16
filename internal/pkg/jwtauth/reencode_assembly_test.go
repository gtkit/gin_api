package jwtauth

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/platform/config"

	"github.com/gtkit/gojwt"
	"github.com/gtkit/gojwt/claims"
)

func TestReencodePreservesRotatingParserPolicy(t *testing.T) {
	t.Parallel()
	privatePath, publicPath := reencodingFixture(t, true)
	cfg := &config.Config{JWT: config.JWTConfig{
		Active: config.JWTKeyConfig{KID: "existing-kid", PrivateKeyPath: privatePath, PublicKeyPath: publicPath},
		Issuer: "existing-issuer", Audiences: []string{"existing-client"}, TokenTTLHours: 1,
	}}
	cfg.Auth.Mode = config.AuthModeLocal
	// 旧 raw PEM 用兼容旧格式的 gojwt 签发；应用加载器要求标准编码，
	// 必须在转换完成后才装配，才能真正验证旧 token 的迁移兼容性。
	original, err := gojwt.NewJwtEd25519(privatePath, publicPath,
		gojwt.WithKeyID("existing-kid"), gojwt.WithTokenDuration(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	token, err := original.GenerateToken(42,
		claims.WithIssuer("existing-issuer"), claims.WithAudience("existing-client"),
		claims.WithSubject("42"), claims.WithRole("user"))
	if err != nil {
		t.Fatal(err)
	}
	before, err := original.ParseToken(token)
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "standard")
	if reencodeErr := ReencodeEd25519Keys(privatePath, publicPath, out); reencodeErr != nil {
		t.Fatal(reencodeErr)
	}
	cfg.JWT.Active.PrivateKeyPath = filepath.Join(out, "jwtpri.pem")
	cfg.JWT.Active.PublicKeyPath = filepath.Join(out, "jwtpub.pem")
	converted, err := Assemble(cfg)
	if err != nil {
		t.Fatal(err)
	}
	after, err := converted.Parser.ParseToken(t.Context(), token)
	if err != nil {
		t.Fatal(err)
	}
	if after.UserID != before.UserID || after.ID != before.ID || after.Issuer != before.Issuer || !after.ExpiresAt.Equal(before.ExpiresAt.Time) {
		t.Fatal("old token identity or expiration changed after path switch")
	}
	if converted.Issuer.ActiveKID() != "existing-kid" {
		t.Fatal("kid changed")
	}
}

func TestReencodedKeysKeepVerificationPolicy(t *testing.T) {
	t.Parallel()
	privatePath, publicPath := reencodingFixture(t, true)
	original, err := gojwt.NewJwtEd25519(privatePath, publicPath, gojwt.WithKeyID("existing-kid"))
	if err != nil {
		t.Fatal(err)
	}
	token, err := original.GenerateToken(42, claims.WithIssuer("existing-issuer"),
		claims.WithAudience("existing-client"), claims.WithSubject("42"))
	if err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(t.TempDir(), "standard")
	if reencodeErr := ReencodeEd25519Keys(privatePath, publicPath, out); reencodeErr != nil {
		t.Fatal(reencodeErr)
	}
	for _, tc := range []struct {
		name, kid, issuer, audience string
		wantErr                     error
	}{
		{"matching policy", "existing-kid", "existing-issuer", "existing-client", nil},
		{"issuer mismatch", "existing-kid", "other-issuer", "existing-client", ErrTokenInvalid},
		{"audience mismatch", "existing-kid", "existing-issuer", "other-client", ErrTokenInvalid},
		{"unknown kid", "other-kid", "existing-issuer", "existing-client", ErrUnknownKID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := &config.Config{JWT: config.JWTConfig{
				Active: config.JWTKeyConfig{KID: tc.kid, PrivateKeyPath: filepath.Join(out, "jwtpri.pem"), PublicKeyPath: filepath.Join(out, "jwtpub.pem")},
				Issuer: tc.issuer, Audiences: []string{tc.audience}, TokenTTLHours: 1,
			}}
			cfg.Auth.Mode = config.AuthModeLocal
			assembly, assembleErr := Assemble(cfg)
			if assembleErr != nil {
				t.Fatal(assembleErr)
			}
			if _, parseErr := assembly.Parser.ParseToken(t.Context(), token); !errors.Is(parseErr, tc.wantErr) {
				t.Fatalf("verification error = %v, want %v", parseErr, tc.wantErr)
			}
		})
	}
}
