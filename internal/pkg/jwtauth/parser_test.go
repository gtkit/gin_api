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

	if _, err := NewFallbackParser(nil, nil); err == nil {
		t.Fatal("NewFallbackParser expected error for nil primary, got nil")
	}
}

func TestFallbackParserParseToken(t *testing.T) {
	t.Parallel()

	legacyVerifier, err := NewLaravelVerifier(testLegacySecret, "hs256", testLegacyRole)
	if err != nil {
		t.Fatalf("NewLaravelVerifier returned error: %v", err)
	}

	primaryClaims := &claims.Claims{UserID: 42}
	primaryErr := errors.New("primary parse failed")

	legacyToken := func(t *testing.T) string {
		return signLegacyToken(t, testLegacySecret, jwtv5.SigningMethodHS256,
			jwtv5.MapClaims{"sub": 7, "exp": time.Now().Add(time.Hour).Unix()})
	}

	tests := []struct {
		name       string
		primary    *stubTokenParser
		legacy     *LaravelVerifier
		token      func(t *testing.T) string
		wantUserID int64
		wantErrIs  error
		wantErrSub string
	}{
		{
			name:       "primary success returns primary claims",
			primary:    &stubTokenParser{parsed: primaryClaims},
			legacy:     legacyVerifier,
			token:      legacyToken,
			wantUserID: 42,
		},
		{
			name:       "primary failure falls back to legacy",
			primary:    &stubTokenParser{err: primaryErr},
			legacy:     legacyVerifier,
			token:      legacyToken,
			wantUserID: 7,
		},
		{
			name:      "primary failure without legacy returns primary error",
			primary:   &stubTokenParser{err: primaryErr},
			legacy:    nil,
			token:     legacyToken,
			wantErrIs: primaryErr,
		},
		{
			name:       "both failures return joined error",
			primary:    &stubTokenParser{err: primaryErr},
			legacy:     legacyVerifier,
			token:      func(*testing.T) string { return "not-a-jwt" },
			wantErrIs:  primaryErr,
			wantErrSub: "parse legacy laravel token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			parser, err := NewFallbackParser(tt.primary, tt.legacy)
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
