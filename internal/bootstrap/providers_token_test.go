package bootstrap

import (
	"context"
	"errors"
	"testing"

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
			laravel:      config.LaravelJWTConfig{Enabled: true, Secret: "laravel-secret", Algorithm: "hs256"},
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
