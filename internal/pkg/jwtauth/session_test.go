package jwtauth

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/gtkit/gojwt/claims"
)

func TestSessionVersionSurvivesIssuanceAndRefresh(t *testing.T) {
	issuer, parser, _ := newTestSetup(t, 8)
	ctx := context.Background()
	const version int64 = 9007199254740993
	token, err := issuer.IssueForSession(ctx, 42, version, "user")
	if err != nil {
		t.Fatal(err)
	}
	refreshed, err := issuer.Refresh(ctx, token)
	if err != nil {
		t.Fatal(err)
	}
	for _, value := range []string{token, refreshed} {
		parsed, err := parser.ParseToken(ctx, value)
		if err != nil {
			t.Fatal(err)
		}
		got, err := SessionVersion(parsed)
		if err != nil || got != version || parsed.UserID != 42 {
			t.Fatalf("uid=%d version=%d err=%v", parsed.UserID, got, err)
		}
	}
	if _, err := issuer.IssueForSession(ctx, 42, -1); err == nil {
		t.Fatal("negative session version accepted")
	}
}

func TestSessionVersionValidation(t *testing.T) {
	for _, tc := range []struct {
		name    string
		value   any
		present bool
		want    int64
		bad     bool
	}{
		{"missing", nil, false, 0, false}, {"zero", "0", true, 0, false}, {"positive", "42", true, 42, false},
		{"full precision", "9007199254740993", true, 9007199254740993, false},
		{"negative", "-1", true, 0, true}, {"leading zero", "01", true, 0, true}, {"numeric", json.Number("42"), true, 0, true},
		{"null", nil, true, 0, true}, {"overflow", "9223372036854775808", true, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			value := &claims.Claims{Ext: map[string]any{}}
			if tc.present {
				value.Ext[SessionVersionKey] = tc.value
			}
			got, err := SessionVersion(value)
			if (err != nil) != tc.bad || (!tc.bad && got != tc.want) {
				t.Fatalf("version=%d err=%v", got, err)
			}
		})
	}
}
