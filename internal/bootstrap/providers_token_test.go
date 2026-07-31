package bootstrap

import (
	"errors"
	"testing"

	jwtv5 "github.com/golang-jwt/jwt/v5"
	"github.com/gtkit/gojwt/claims"

	legacyjwtauth "github.com/gtkit/gin-api/internal/pkg/jwtauth"
	"github.com/gtkit/gin-api/internal/platform/config"
)

type stubTokenParser struct{}

func (stubTokenParser) ParseToken(string, ...jwtv5.ParserOption) (*claims.Claims, error) {
	return nil, errors.New("stub parse")
}

func TestNewTokenParser(t *testing.T) {
	t.Parallel()

	issuer := stubTokenParser{}

	tests := []struct {
		name         string
		legacy       config.LegacyLaravelJWTConfig
		wantFallback bool
		wantErr      bool
	}{
		{
			name:   "legacy disabled keeps bare issuer",
			legacy: config.LegacyLaravelJWTConfig{},
		},
		{
			name:         "legacy enabled wraps fallback parser",
			legacy:       config.LegacyLaravelJWTConfig{Enabled: true, Secret: "laravel-secret", Algorithm: "hs256"},
			wantFallback: true,
		},
		{
			name:    "legacy enabled with empty secret fails",
			legacy:  config.LegacyLaravelJWTConfig{Enabled: true},
			wantErr: true,
		},
		{
			name:    "legacy enabled with invalid algorithm fails",
			legacy:  config.LegacyLaravelJWTConfig{Enabled: true, Secret: "laravel-secret", Algorithm: "rs256"},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := &config.Config{}
			cfg.JWT.LegacyLaravel = tt.legacy

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

			if _, isFallback := parser.(*legacyjwtauth.FallbackParser); isFallback != tt.wantFallback {
				t.Fatalf("parser type = %T, want fallback=%v", parser, tt.wantFallback)
			}
			if !tt.wantFallback && parser != legacyjwtauth.TokenParser(issuer) {
				t.Fatalf("parser = %v, want the bare issuer passed through", parser)
			}
		})
	}
}
