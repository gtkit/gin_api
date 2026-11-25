package sign

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"testing"
	"time"
)

func TestSignatureWireFormat(t *testing.T) {
	s := New("test", "fixture", time.Minute)
	values := url.Values{"b": {"two words"}, "a": {"1"}}
	auth, date, err := s.Generate("/echo", "post", values)
	if err != nil {
		t.Fatal(err)
	}
	m := hmac.New(sha256.New, []byte("fixture"))
	m.Write([]byte("/echo|POST|a=1&b=two words|" + date))
	if auth != "test "+base64.StdEncoding.EncodeToString(m.Sum(nil)) {
		t.Fatal("wire format changed")
	}
	if ok, err := s.Verify(auth, date, "/echo", "POST", values); !ok || err != nil {
		t.Fatal(err)
	}
	if ok, _ := s.Verify(auth, date, "/other", "POST", values); ok {
		t.Fatal("invalid signature accepted")
	}
	if ok, _ := s.Verify(auth, time.Now().Add(time.Hour).In(signatureZone).Format(time.DateTime), "/echo", "POST", values); ok {
		t.Fatal("future date accepted")
	}
}
