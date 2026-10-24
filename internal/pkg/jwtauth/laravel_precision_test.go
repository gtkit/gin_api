package jwtauth

import (
	"encoding/base64"
	"errors"
	"math"
	"strconv"
	"testing"
	"time"

	jwtv5 "github.com/golang-jwt/jwt/v5"
)

// Sign the literal payload so neither a float64 nor JSON re-encoding can change
// the subject before the production parser sees it.
func signLaravelPayload(t *testing.T, payload, secret string) string {
	t.Helper()
	encoded := base64.RawURLEncoding
	input := encoded.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`)) + "." + encoded.EncodeToString([]byte(payload))
	signature, err := jwtv5.SigningMethodHS256.Sign(input, []byte(secret))
	if err != nil {
		t.Fatalf("sign literal laravel payload: %v", err)
	}
	return input + "." + encoded.EncodeToString(signature)
}

func TestLaravelVerifierPreservesIntegerSubject(t *testing.T) {
	t.Parallel()
	verifier, err := NewLaravelVerifier(testLaravelSecret, "hs256", testLaravelRole, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name      string
		sub       string
		wantID    int64
		rejected  bool
		wrongKey  bool
		wantCause error
	}{
		{name: "one", sub: "1", wantID: 1},
		{name: "below float exact limit", sub: "9007199254740991", wantID: 1<<53 - 1},
		{name: "float exact limit", sub: "9007199254740992", wantID: 1 << 53},
		{name: "above float exact limit", sub: "9007199254740993", wantID: 1<<53 + 1},
		{name: "max int64", sub: "9223372036854775807", wantID: math.MaxInt64},
		{name: "int64 overflow", sub: "9223372036854775808", rejected: true, wantCause: strconv.ErrRange},
		{name: "zero", sub: "0", rejected: true},
		{name: "negative", sub: "-1", rejected: true},
		{name: "fraction", sub: "1.5", rejected: true, wantCause: strconv.ErrSyntax},
		{name: "integer looking decimal", sub: "1.0", rejected: true, wantCause: strconv.ErrSyntax},
		{name: "exponent", sub: "1e3", rejected: true, wantCause: strconv.ErrSyntax},
		{name: "large exponent", sub: "9.007199254740993e15", rejected: true, wantCause: strconv.ErrSyntax},
		{name: "missing", rejected: true},
		{name: "null", sub: "null", rejected: true},
		{name: "boolean", sub: "true", rejected: true},
		{name: "integer string", sub: `"9007199254740993"`, wantID: 1<<53 + 1},
		{name: "trimmed integer string", sub: `" \t9007199254740993\n "`, wantID: 1<<53 + 1},
		{name: "max int64 string", sub: `"9223372036854775807"`, wantID: math.MaxInt64},
		{name: "overflow string", sub: `"9223372036854775808"`, rejected: true, wantCause: strconv.ErrRange},
		{name: "empty string", sub: `""`, rejected: true, wantCause: strconv.ErrSyntax},
		{name: "zero string", sub: `"0"`, rejected: true},
		{name: "negative string", sub: `"-1"`, rejected: true},
		{name: "fraction string", sub: `"1.5"`, rejected: true, wantCause: strconv.ErrSyntax},
		{name: "exponent string", sub: `"1e3"`, rejected: true, wantCause: strconv.ErrSyntax},
		{name: "wrong signature", sub: "9007199254740993", rejected: true, wrongKey: true, wantCause: jwtv5.ErrTokenSignatureInvalid},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			payload := `{"exp":1767229200,"iat":1767225600`
			if tc.sub != "" {
				payload += `,"sub":` + tc.sub
			}
			payload += "}"
			secret := testLaravelSecret
			if tc.wrongKey {
				secret += "-other"
			}
			token := signLaravelPayload(t, payload, secret)
			for _, resetParser := range []bool{false, true} {
				t.Run(strconv.FormatBool(resetParser), func(t *testing.T) {
					opts := []jwtv5.ParserOption{}
					if resetParser {
						opts = append(opts, func(p *jwtv5.Parser) { *p = *jwtv5.NewParser() })
					}
					opts = append(opts, jwtv5.WithTimeFunc(func() time.Time { return time.Unix(1767225600, 0) }))
					parsed, err := verifier.ParseToken(t.Context(), token, opts...)
					if tc.rejected {
						if err == nil || parsed != nil {
							t.Fatalf("untrusted subject returned claims=%+v, error=%v", parsed, err)
						}
						if tc.wantCause != nil && !errors.Is(err, tc.wantCause) {
							t.Fatalf("error = %v, want cause %v", err, tc.wantCause)
						}
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					if parsed == nil || parsed.UserID != tc.wantID || parsed.Subject != strconv.FormatInt(tc.wantID, 10) {
						t.Fatalf("claims = %+v, want exact user ID %d", parsed, tc.wantID)
					}
					if err := parsed.VerifyRole(testLaravelRole); err != nil {
						t.Fatalf("adapted role: %v", err)
					}
					if parsed.ExpiresAt == nil || parsed.ExpiresAt.Unix() != 1767229200 || parsed.IssuedAt == nil || parsed.IssuedAt.Unix() != 1767225600 {
						t.Fatal("adapted temporal claims changed")
					}
				})
			}
		})
	}
}
