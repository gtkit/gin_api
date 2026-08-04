package jwtauth

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	jwtv5 "github.com/golang-jwt/jwt/v5"
	"github.com/gtkit/gojwt/claims"
)

type stubTokenParser struct {
	parsed *claims.Claims
	err    error
	calls  int
}

func (s *stubTokenParser) ParseToken(_ context.Context, tokenString string, _ ...jwtv5.ParserOption) (*claims.Claims, error) {
	s.calls++
	return s.parsed, s.err
}

func TestNewFallbackParserRequiresPrimary(t *testing.T) {
	t.Parallel()

	if _, err := NewFallbackParser(nil, nil, time.Time{}); err == nil {
		t.Fatal("NewFallbackParser expected error for nil primary, got nil")
	}
}

func TestFallbackParserParseToken(t *testing.T) {
	t.Parallel()

	laravelVerifier, err := NewLaravelVerifier(testLaravelSecret, "hs256", testLaravelRole)
	if err != nil {
		t.Fatalf("NewLaravelVerifier returned error: %v", err)
	}

	primaryClaims := &claims.Claims{UserID: 42}
	primaryErr := errors.New("primary parse failed")

	laravelToken := func(t *testing.T) string {
		return signLaravelToken(t, testLaravelSecret, jwtv5.SigningMethodHS256,
			jwtv5.MapClaims{"sub": 7, "exp": time.Now().Add(time.Hour).Unix()})
	}

	tests := []struct {
		name       string
		primary    *stubTokenParser
		laravel    *LaravelVerifier
		token      func(t *testing.T) string
		wantUserID int64
		wantErrIs  error
		wantErrSub string
	}{
		{
			name:       "primary success returns primary claims",
			primary:    &stubTokenParser{parsed: primaryClaims},
			laravel:    laravelVerifier,
			token:      laravelToken,
			wantUserID: 42,
		},
		{
			name:       "primary failure falls back to laravel",
			primary:    &stubTokenParser{err: primaryErr},
			laravel:    laravelVerifier,
			token:      laravelToken,
			wantUserID: 7,
		},
		{
			name:      "primary failure without laravel returns primary error",
			primary:   &stubTokenParser{err: primaryErr},
			laravel:   nil,
			token:     laravelToken,
			wantErrIs: primaryErr,
		},
		{
			name:       "both failures return joined error",
			primary:    &stubTokenParser{err: primaryErr},
			laravel:    laravelVerifier,
			token:      func(*testing.T) string { return "not-a-jwt" },
			wantErrIs:  primaryErr,
			wantErrSub: "parse laravel token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// 启用回退时才需要截止时刻；不启用时零值是合法的。
			cutoff := time.Time{}
			if tt.laravel != nil {
				cutoff = time.Now().Add(time.Hour)
			}
			parser, err := NewFallbackParser(tt.primary, tt.laravel, cutoff)
			if err != nil {
				t.Fatalf("NewFallbackParser returned error: %v", err)
			}

			parsed, err := parser.ParseToken(context.Background(), tt.token(t))
			if tt.wantErrIs != nil {
				if err == nil {
					t.Fatal("ParseToken expected error, got nil")
				}
				if !errors.Is(err, tt.wantErrIs) {
					t.Fatalf("ParseToken error = %v, want errors.Is %v", err, tt.wantErrIs)
				}
				if tt.wantErrSub != "" && !strings.Contains(err.Error(), tt.wantErrSub) {
					t.Fatalf("ParseToken error = %q, want containing %q", err.Error(), tt.wantErrSub)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseToken returned error: %v", err)
			}

			if parsed.UserID != tt.wantUserID {
				t.Fatalf("UserID = %d, want %d", parsed.UserID, tt.wantUserID)
			}
			if tt.primary.calls != 1 {
				t.Fatalf("primary calls = %d, want 1", tt.primary.calls)
			}
		})
	}
}
