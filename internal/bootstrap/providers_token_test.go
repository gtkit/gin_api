package bootstrap

import (
	"context"
	"errors"
	"testing"
	"time"

	jwtv5 "github.com/golang-jwt/jwt/v5"
	"github.com/gtkit/gojwt/claims"

	jwtauth "github.com/gtkit/gin-api/internal/pkg/jwtauth"
	"github.com/gtkit/gin-api/internal/platform/config"
)

type stubTokenParser struct{}

func (stubTokenParser) ParseToken(_ context.Context, _ string, _ ...jwtv5.ParserOption) (*claims.Claims, error) {
	return nil, errors.New("stub parse")
}

func TestNewTokenParser(t *testing.T) {
	t.Parallel()

	issuer := stubTokenParser{}

	tests := []struct {
		name         string
		laravel      config.LaravelJWTConfig
		wantFallback bool
		wantErr      bool
	}{
		{
			name:    "laravel disabled keeps bare issuer",
			laravel: config.LaravelJWTConfig{},
		},
		{
			name:         "laravel enabled wraps fallback parser",
			laravel:      config.LaravelJWTConfig{Enabled: true, Secret: "laravel-secret-0123456789abcdefgh", Algorithm: "hs256"},
			wantFallback: true,
		},
		{
			name:    "laravel enabled with empty secret fails",
			laravel: config.LaravelJWTConfig{Enabled: true},
			wantErr: true,
		},
		{
			name:    "laravel enabled with invalid algorithm fails",
			laravel: config.LaravelJWTConfig{Enabled: true, Secret: "laravel-secret", Algorithm: "rs256"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := &config.Config{}
			cfg.JWT.Laravel = tt.laravel
			// 启用回退必须给出终点。用例本身要验证的是别的字段，
			// 缺终点会先被那条规则挡下，于是想测的分支根本走不到。
			if tt.laravel.Enabled {
				cfg.JWT.LaravelAcceptUntil = time.Now().Add(24 * time.Hour).Format(time.RFC3339)
			}

			parser, err := newTokenParser(cfg, issuer)
			if tt.wantErr {
				if err == nil {
					t.Fatal("newTokenParser expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("newTokenParser returned error: %v", err)
			}

			if _, isFallback := parser.(*jwtauth.FallbackParser); isFallback != tt.wantFallback {
				t.Fatalf("parser type = %T, want fallback=%v", parser, tt.wantFallback)
			}
			if !tt.wantFallback && parser != jwtauth.TokenParser(issuer) {
				t.Fatalf("parser = %v, want the bare issuer passed through", parser)
			}
		})
	}
}

// TestNewTokenParserRequiresLaravelCutoff 钉住启用回退必须给出终点。
//
// 截止时刻此前只在配置层被校验格式，装配层完全不读——于是只要开关不关，
// Laravel 令牌永久有效，而配置里那个日期给了运维一个**虚假的**下线承诺。
//
// 现在缺终点会在装配期失败，而不是静默地一直生效。
func TestNewTokenParserRequiresLaravelCutoff(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	cfg.JWT.Laravel = config.LaravelJWTConfig{
		Enabled: true, Secret: "laravel-secret-0123456789abcdefgh", Algorithm: "hs256",
	}
	// 刻意不设 LaravelAcceptUntil。

	if _, err := newTokenParser(cfg, stubTokenParser{}); err == nil {
		t.Fatal("enabling the laravel fallback without a cutoff was accepted: " +
			"a compatibility window without an end date never ends")
	}
}
