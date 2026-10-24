package jwtauth

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gojwt"
)

func assemblyKeyConfig(t *testing.T) *config.Config {
	t.Helper()
	dir := t.TempDir()
	cfg := &config.Config{Env: "test", Auth: config.AuthConfig{Mode: config.AuthModeLocal}}
	cfg.JWT = config.JWTConfig{Issuer: "assembly-test", Audiences: []string{"app"}, Active: config.JWTKeyConfig{KID: "active", PrivateKeyPath: filepath.Join(dir, "active.private.pem"), PublicKeyPath: filepath.Join(dir, "active.public.pem")}}
	if err := gojwt.GenerateEd25519Keys(cfg.JWT.Active.PrivateKeyPath, cfg.JWT.Active.PublicKeyPath); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestExternalAssemblyOnlyLoadsVerificationKeys(t *testing.T) {
	t.Parallel()
	for _, unusedPrivate := range []string{"", "missing", "invalid"} {
		t.Run(unusedPrivate, func(t *testing.T) {
			t.Parallel()
			cfg := assemblyKeyConfig(t)
			local, err := Assemble(cfg)
			if err != nil {
				t.Fatal(err)
			}
			token, err := local.Issuer.Issue(t.Context(), 9007199254740993, "user")
			if err != nil {
				t.Fatal(err)
			}
			previous := config.JWTKeyConfig{KID: "previous", PrivateKeyPath: filepath.Join(t.TempDir(), "previous.private.pem"), PublicKeyPath: filepath.Join(t.TempDir(), "previous.public.pem")}
			if err := gojwt.GenerateEd25519Keys(previous.PrivateKeyPath, previous.PublicKeyPath); err != nil {
				t.Fatal(err)
			}
			oldCfg := *cfg
			oldCfg.JWT.Active = previous
			old, err := Assemble(&oldCfg)
			if err != nil {
				t.Fatal(err)
			}
			oldToken, err := old.Issuer.Issue(t.Context(), 7, "user")
			if err != nil {
				t.Fatal(err)
			}
			cfg.Auth.Mode = config.AuthModeExternal
			cfg.JWT.Previous = []config.JWTKeyConfig{{KID: previous.KID, PublicKeyPath: previous.PublicKeyPath}}
			cfg.JWT.Active.PrivateKeyPath = ""
			if unusedPrivate != "" {
				cfg.JWT.Active.PrivateKeyPath = filepath.Join(t.TempDir(), "unused.private.pem")
				if unusedPrivate == "invalid" {
					if err := os.WriteFile(cfg.JWT.Active.PrivateKeyPath, []byte("not a private key"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			assembly, err := Assemble(cfg)
			if err != nil || assembly.Issuer != nil || len(assembly.KeySet.SigningKey()) != 0 {
				t.Fatalf("verification assembly=%+v err=%v", assembly, err)
			}
			for _, signed := range []string{token, oldToken} {
				if parsed, err := assembly.Parser.ParseToken(t.Context(), signed); err != nil || parsed.UserID == 0 {
					t.Fatalf("verification-only parser rejected valid rotation token: claims=%+v err=%v", parsed, err)
				}
			}
			if _, err := NewIssuer(assembly.KeySet, assembly.Parser, IssuerOptions{Issuer: cfg.JWT.Issuer, Audiences: cfg.JWT.Audiences, TTL: time.Hour}); err == nil {
				t.Fatal("verification-only key set created a signer")
			}
		})
	}
}

func TestAssemblyRetainsModeAndKeyValidation(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"local missing private", "local mismatched pair", "local insecure private", "external duplicate kid", "external invalid previous public", "unknown mode"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			cfg := assemblyKeyConfig(t)
			switch scenario {
			case "local missing private":
				cfg.JWT.Active.PrivateKeyPath = ""
			case "local mismatched pair":
				cfg.JWT.Active.PrivateKeyPath = assemblyKeyConfig(t).JWT.Active.PrivateKeyPath
			case "local insecure private":
				cfg.Env = "prod"
				if err := os.Chmod(cfg.JWT.Active.PrivateKeyPath, 0o644); err != nil {
					t.Fatal(err)
				}
			case "external duplicate kid":
				cfg.Auth.Mode = config.AuthModeExternal
				cfg.JWT.Previous = []config.JWTKeyConfig{{KID: cfg.JWT.Active.KID, PublicKeyPath: cfg.JWT.Active.PublicKeyPath}}
			case "external invalid previous public":
				cfg.Auth.Mode = config.AuthModeExternal
				bad := filepath.Join(t.TempDir(), "bad.public.pem")
				if err := os.WriteFile(bad, []byte("bad public key"), 0o644); err != nil {
					t.Fatal(err)
				}
				cfg.JWT.Previous = []config.JWTKeyConfig{{KID: "previous", PublicKeyPath: bad}}
			default:
				cfg.Auth.Mode = "unknown"
			}
			if _, err := Assemble(cfg); err == nil {
				t.Fatal("invalid key or mode accepted")
			}
		})
	}
}
