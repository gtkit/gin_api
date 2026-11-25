package models

import (
	"bytes"
	"encoding/json"
	"testing"
)

func TestUserBinaryOmitsPasswordVerifier(t *testing.T) {
	v := User{Password: "test-verifier", Nickname: "test"}
	data, err := v.MarshalBinary()
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	if _, ok := fields["Password"]; ok || bytes.Contains(data, []byte(v.Password)) {
		t.Fatal("verifier serialized")
	}
	got := User{Password: "stale"}
	if err := got.UnmarshalBinary([]byte(`{"Nickname":"test","Password":"old-verifier"}`)); err != nil || got.Nickname != "test" || got.Password != "" {
		t.Fatal("legacy record decode failed")
	}
}
