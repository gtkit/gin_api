package apisign

import (
	"testing"
)

func TestSignCanonicalRoundTrip(t *testing.T) {
	const secret = "0123456789abcdef0123456789abcdef"
	canonical := CanonicalString("POST", "/api/pay", "appId=svc_a&nonce=abc123&timestamp=1700000000",
		`{"amount":100}`, "svc_a", "1700000000", "abc123")
	sig := Sign(secret, canonical)

	if !ConstantTimeEquals(sig, Sign(secret, canonical)) {
		t.Fatal("re-signing the same canonical string must produce an identical signature")
	}
	if ConstantTimeEquals(sig, Sign("another-secret-0123456789abcdef", canonical)) {
		t.Fatal("a different secret must not produce the same signature")
	}
}

func TestCanonicalStringBoundaries(t *testing.T) {
	// 换行分隔消除边界歧义：不同字段不能拼出同一条串。
	x := CanonicalString("GET", "/q", "a=1", "b=2", "app", "1", "n")
	y := CanonicalString("GET", "/q", "a=1b", "=2", "app", "1", "n")
	if x == y {
		t.Fatal("newline delimiters must keep field boundaries unambiguous")
	}
}

func TestConstantTimeEquals(t *testing.T) {
	if !ConstantTimeEquals("abc123", "abc123") {
		t.Fatal("equal strings must compare equal")
	}
	if ConstantTimeEquals("abc123", "abc124") {
		t.Fatal("differing strings must compare unequal")
	}
	if ConstantTimeEquals("abc123", "abc1234") {
		t.Fatal("strings of different length must compare unequal")
	}
}
