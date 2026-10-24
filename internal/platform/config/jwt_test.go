package config

import (
	"strings"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/shared/env"
)

func rotationConfig(mutate func(*JWTConfig)) JWTConfig {
	c := JWTConfig{
		Active: JWTKeyConfig{
			KID:            "k2026a",
			PrivateKeyPath: "config/pem/k2026a.private.pem",
			PublicKeyPath:  "config/pem/k2026a.public.pem",
		},
		Issuer:    "gin-api",
		Audiences: []string{"app"},
	}
	if mutate != nil {
		mutate(&c)
	}
	return c
}

func TestJWTRotationPrivateKeyRequirementFollowsAuthMode(t *testing.T) {
	t.Parallel()
	for _, mode := range []AuthMode{AuthModeLocal, AuthModeExternal} {
		cfg := rotationConfig(func(c *JWTConfig) { c.Active.PrivateKeyPath = "" })
		err := validateJWTRotation(cfg, mode, env.Test)
		if (err != nil) != (mode == AuthModeLocal) {
			t.Fatalf("mode=%s missing private key error=%v", mode, err)
		}
		cfg.Active.PublicKeyPath = ""
		if err := validateJWTRotation(cfg, mode, env.Test); err == nil {
			t.Fatalf("mode=%s accepted missing verification key", mode)
		}
	}
}

// TestValidateJWTRotation 覆盖轮换配置的全部约束。
func TestValidateJWTRotation(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		mutate   func(*JWTConfig)
		wantErr  bool
		wantHint string
	}{
		{name: "minimal valid config", mutate: nil},
		{
			// 静默忽略废弃键比报错危险得多：使用者会以为自己配置的密钥
			// 正在生效，而实际用的是另一组。
			name:    "deprecated single-key paths rejected",
			mutate:  func(c *JWTConfig) { c.PrivateKeyPath = "config/pem/jwtpri.pem" },
			wantErr: true, wantHint: "no longer supported",
		},
		{
			name:    "missing active kid",
			mutate:  func(c *JWTConfig) { c.Active.KID = "" },
			wantErr: true, wantHint: "jwt.active.kid",
		},
		{
			name:    "missing active private key",
			mutate:  func(c *JWTConfig) { c.Active.PrivateKeyPath = "" },
			wantErr: true, wantHint: "jwt.active.private_key_path",
		},
		{
			name:    "missing issuer",
			mutate:  func(c *JWTConfig) { c.Issuer = "" },
			wantErr: true, wantHint: "jwt.issuer",
		},
		{
			name:    "missing audiences",
			mutate:  func(c *JWTConfig) { c.Audiences = nil },
			wantErr: true, wantHint: "jwt.audiences",
		},
		{
			// 留着私钥意味着一把已经退役的密钥仍能签发新令牌，
			// 而那正是轮换要终结的能力。
			name: "previous key carrying a private key rejected",
			mutate: func(c *JWTConfig) {
				c.Previous = []JWTKeyConfig{{
					KID: "k2025b", PublicKeyPath: "b.pem", PrivateKeyPath: "b.key",
				}}
			},
			wantErr: true, wantHint: "must not carry a private key",
		},
		{
			name: "previous key without a kid rejected",
			mutate: func(c *JWTConfig) {
				c.Previous = []JWTKeyConfig{{PublicKeyPath: "b.pem"}}
			},
			wantErr: true, wantHint: "kid is required",
		},
		{
			name: "too many previous keys rejected",
			mutate: func(c *JWTConfig) {
				for i := range maxJWTPreviousKeys + 1 {
					c.Previous = append(c.Previous, JWTKeyConfig{
						KID: "k" + string(rune('a'+i)), PublicKeyPath: "p.pem",
					})
				}
			},
			wantErr: true, wantHint: "at most",
		},
		{
			name:    "negative clock skew rejected",
			mutate:  func(c *JWTConfig) { c.ClockSkewSeconds = -1 },
			wantErr: true, wantHint: "clock_skew_seconds",
		},
		{
			// 偏移窗口直接放宽 exp/nbf 判定，一个写成一天的配置
			// 等于把过期检查关掉。
			name:    "excessive clock skew rejected",
			mutate:  func(c *JWTConfig) { c.ClockSkewSeconds = 86400 },
			wantErr: true, wantHint: "ceiling",
		},
		{
			name:    "negative cache size rejected",
			mutate:  func(c *JWTConfig) { c.CacheMaxEntries = -1 },
			wantErr: true, wantHint: "cache_max_entries",
		},
		{name: "zero cache size is valid", mutate: func(c *JWTConfig) { c.CacheMaxEntries = 0 }},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := validateJWTRotation(rotationConfig(tc.mutate), AuthModeLocal, env.Dev)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("validateJWTRotation() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("validateJWTRotation() = nil, want error")
			}
			if !strings.Contains(err.Error(), tc.wantHint) {
				t.Fatalf("error %q does not mention %q", err, tc.wantHint)
			}
		})
	}
}

// TestValidateJWTCutoffs 钉住两条兼容截止时刻的取值约束。
//
// 用 RFC3339 绝对时刻而不是时长：时长会让每次重启自动顺延，
// 兼容口因此变成永久的。而一个"45 天以后"的截止时刻与永久的无法区分。
func TestValidateJWTCutoffs(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		mutate   func(*JWTConfig)
		wantErr  bool
		wantHint string
	}{
		{name: "both empty means not accepted", mutate: nil},
		{
			name: "valid near-future no-kid cutoff",
			mutate: func(c *JWTConfig) {
				c.UnkeyedTokenAcceptUntil = time.Now().Add(24 * time.Hour).Format(time.RFC3339)
			},
		},
		{
			name: "valid near-future laravel cutoff",
			mutate: func(c *JWTConfig) {
				c.LaravelAcceptUntil = time.Now().Add(24 * time.Hour).Format(time.RFC3339)
			},
		},
		{
			name:    "non-RFC3339 rejected",
			mutate:  func(c *JWTConfig) { c.UnkeyedTokenAcceptUntil = "2026-08-03" },
			wantErr: true, wantHint: "RFC3339",
		},
		{
			name: "far-future cutoff rejected",
			mutate: func(c *JWTConfig) {
				c.UnkeyedTokenAcceptUntil = time.Now().Add(365 * 24 * time.Hour).Format(time.RFC3339)
			},
			wantErr: true, wantHint: "indistinguishable from a permanent one",
		},
		{
			// 两条分别校验：一条非法不该被另一条掩盖。
			name: "laravel far-future rejected independently",
			mutate: func(c *JWTConfig) {
				c.LaravelAcceptUntil = time.Now().Add(365 * 24 * time.Hour).Format(time.RFC3339)
			},
			wantErr: true, wantHint: "laravel_accept_until",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := validateJWTRotation(rotationConfig(tc.mutate), AuthModeLocal, env.Dev)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("validateJWTRotation() = %v, want nil", err)
				}
				return
			}
			if err == nil {
				t.Fatal("validateJWTRotation() = nil, want error")
			}
			if !strings.Contains(err.Error(), tc.wantHint) {
				t.Fatalf("error %q does not mention %q", err, tc.wantHint)
			}
		})
	}
}
