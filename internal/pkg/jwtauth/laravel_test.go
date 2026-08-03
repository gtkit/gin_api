package jwtauth

import (
	"context"
	"strings"
	"testing"
	"time"

	jwtv5 "github.com/golang-jwt/jwt/v5"
)

const (
	testLaravelSecret = "test-laravel-jwt-secret"
	testLaravelRole   = "user"
)

func signLaravelToken(t *testing.T, secret string, method jwtv5.SigningMethod, mapClaims jwtv5.MapClaims) string {
	t.Helper()

	token, err := jwtv5.NewWithClaims(method, mapClaims).SignedString([]byte(secret))
	if err != nil {
		t.Fatalf("sign laravel token: %v", err)
	}

	return token
}

func signNoneToken(t *testing.T, mapClaims jwtv5.MapClaims) string {
	t.Helper()

	token, err := jwtv5.NewWithClaims(jwtv5.SigningMethodNone, mapClaims).SignedString(jwtv5.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("sign none token: %v", err)
	}

	return token
}

func TestNewLaravelVerifier(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		secret    string
		algorithm string
		role      string
		wantErr   bool
	}{
		{name: "default algorithm", secret: testLaravelSecret, algorithm: "", role: testLaravelRole},
		{name: "uppercase algorithm accepted", secret: testLaravelSecret, algorithm: "HS384", role: testLaravelRole},
		{name: "hs512 accepted", secret: testLaravelSecret, algorithm: "hs512", role: testLaravelRole},
		{name: "empty secret rejected", secret: "   ", algorithm: "hs256", role: testLaravelRole, wantErr: true},
		{name: "empty role rejected", secret: testLaravelSecret, algorithm: "hs256", role: "", wantErr: true},
		{name: "asymmetric algorithm rejected", secret: testLaravelSecret, algorithm: "rs256", role: testLaravelRole, wantErr: true},
		{name: "none algorithm rejected", secret: testLaravelSecret, algorithm: "none", role: testLaravelRole, wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			verifier, err := NewLaravelVerifier(tt.secret, tt.algorithm, tt.role)
			if tt.wantErr {
				if err == nil {
					t.Fatal("NewLaravelVerifier expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("NewLaravelVerifier returned error: %v", err)
			}
			if verifier == nil {
				t.Fatal("NewLaravelVerifier returned nil verifier")
			}
		})
	}
}

func TestLaravelVerifierParseToken(t *testing.T) {
	t.Parallel()

	now := time.Now()
	validExp := now.Add(time.Hour).Unix()

	tests := []struct {
		name       string
		token      func(t *testing.T) string
		wantUserID int64
		wantErr    string
	}{
		{
			name: "numeric sub accepted",
			token: func(t *testing.T) string {
				return signLaravelToken(t, testLaravelSecret, jwtv5.SigningMethodHS256,
					jwtv5.MapClaims{"sub": 123, "exp": validExp, "iat": now.Unix()})
			},
			wantUserID: 123,
		},
		{
			name: "string sub accepted",
			token: func(t *testing.T) string {
				return signLaravelToken(t, testLaravelSecret, jwtv5.SigningMethodHS256,
					jwtv5.MapClaims{"sub": "456", "exp": validExp})
			},
			wantUserID: 456,
		},
		{
			name: "expired token rejected",
			token: func(t *testing.T) string {
				return signLaravelToken(t, testLaravelSecret, jwtv5.SigningMethodHS256,
					jwtv5.MapClaims{"sub": 123, "exp": now.Add(-time.Hour).Unix()})
			},
			wantErr: "parse laravel token",
		},
		{
			name: "token without exp rejected",
			token: func(t *testing.T) string {
				return signLaravelToken(t, testLaravelSecret, jwtv5.SigningMethodHS256,
					jwtv5.MapClaims{"sub": 123})
			},
			wantErr: "parse laravel token",
		},
		{
			name: "wrong secret rejected",
			token: func(t *testing.T) string {
				return signLaravelToken(t, "other-secret", jwtv5.SigningMethodHS256,
					jwtv5.MapClaims{"sub": 123, "exp": validExp})
			},
			wantErr: "parse laravel token",
		},
		{
			name: "algorithm mismatch rejected",
			token: func(t *testing.T) string {
				return signLaravelToken(t, testLaravelSecret, jwtv5.SigningMethodHS512,
					jwtv5.MapClaims{"sub": 123, "exp": validExp})
			},
			wantErr: "parse laravel token",
		},
		{
			name: "none algorithm rejected",
			token: func(t *testing.T) string {
				return signNoneToken(t, jwtv5.MapClaims{"sub": 123, "exp": validExp})
			},
			wantErr: "parse laravel token",
		},
		{
			name: "missing sub rejected",
			token: func(t *testing.T) string {
				return signLaravelToken(t, testLaravelSecret, jwtv5.SigningMethodHS256,
					jwtv5.MapClaims{"exp": validExp})
			},
			wantErr: "sub claim is missing",
		},
		{
			name: "zero sub rejected",
			token: func(t *testing.T) string {
				return signLaravelToken(t, testLaravelSecret, jwtv5.SigningMethodHS256,
					jwtv5.MapClaims{"sub": 0, "exp": validExp})
			},
			wantErr: "must be positive",
		},
		{
			name: "negative string sub rejected",
			token: func(t *testing.T) string {
				return signLaravelToken(t, testLaravelSecret, jwtv5.SigningMethodHS256,
					jwtv5.MapClaims{"sub": "-5", "exp": validExp})
			},
			wantErr: "must be positive",
		},
		{
			name: "non-integer string sub rejected",
			token: func(t *testing.T) string {
				return signLaravelToken(t, testLaravelSecret, jwtv5.SigningMethodHS256,
					jwtv5.MapClaims{"sub": "abc", "exp": validExp})
			},
			wantErr: "is not an integer",
		},
		{
			name: "fractional sub rejected",
			token: func(t *testing.T) string {
				return signLaravelToken(t, testLaravelSecret, jwtv5.SigningMethodHS256,
					jwtv5.MapClaims{"sub": 12.5, "exp": validExp})
			},
			wantErr: "is not an integer",
		},
		{
			name: "unsupported sub type rejected",
			token: func(t *testing.T) string {
				return signLaravelToken(t, testLaravelSecret, jwtv5.SigningMethodHS256,
					jwtv5.MapClaims{"sub": true, "exp": validExp})
			},
			wantErr: "unsupported type",
		},
	}

	verifier, err := NewLaravelVerifier(testLaravelSecret, "hs256", testLaravelRole)
	if err != nil {
		t.Fatalf("NewLaravelVerifier returned error: %v", err)
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			parsed, err := verifier.ParseToken(context.Background(), tt.token(t))
			if tt.wantErr != "" {
				if err == nil {
					t.Fatalf("ParseToken expected error containing %q, got nil", tt.wantErr)
				}
				if !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("ParseToken error = %q, want containing %q", err.Error(), tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseToken returned error: %v", err)
			}

			if parsed.UserID != tt.wantUserID {
				t.Fatalf("UserID = %d, want %d", parsed.UserID, tt.wantUserID)
			}
			if err := parsed.VerifyRole(testLaravelRole); err != nil {
				t.Fatalf("VerifyRole(%q) returned error: %v", testLaravelRole, err)
			}
			if parsed.Subject == "" {
				t.Fatal("Subject is empty, want user id string")
			}
			if parsed.ExpiresAt == nil {
				t.Fatal("ExpiresAt is nil, want propagated exp")
			}
		})
	}
}
