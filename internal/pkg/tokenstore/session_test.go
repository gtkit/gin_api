package tokenstore

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func TestRefreshSessionVersionSurvivesRotationAndReplay(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	})
	store, err := NewRefreshStore(t.Context(), client, "session", time.Hour, strings.Repeat("s", 32))
	if err != nil {
		t.Fatal(err)
	}
	token, err := store.IssueForSession(t.Context(), 7, 9007199254740993)
	if err != nil {
		t.Fatal(err)
	}
	uid, version, err := store.PeekSession(t.Context(), token)
	if err != nil || uid != 7 || version != 9007199254740993 {
		t.Fatalf("peek=%d/%d %v", uid, version, err)
	}
	uid, version, next, err := store.ConsumeSession(t.Context(), token)
	if err != nil || uid != 7 || version != 9007199254740993 {
		t.Fatalf("consume=%d/%d %v", uid, version, err)
	}
	_, againVersion, replayed, err := store.ConsumeSession(t.Context(), token)
	if err != nil || replayed != next || againVersion != version {
		t.Fatalf("replay changed session: %v", err)
	}
	if _, v, err := store.PeekSession(t.Context(), next); err != nil || v != version {
		t.Fatalf("next lost version: %d %v", v, err)
	}
	family, secret, err := splitToken(next)
	if err != nil {
		t.Fatal(err)
	}
	if err := client.HDel(t.Context(), store.recordKey(family, secret), "session_version").Err(); err != nil {
		t.Fatal(err)
	}
	if _, v, err := store.PeekSession(t.Context(), next); err != nil || v != 0 {
		t.Fatalf("missing version compatibility: %d %v", v, err)
	}
	if err := client.HSet(t.Context(), store.recordKey(family, secret), "session_version", "-1").Err(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := store.PeekSession(t.Context(), next); !errors.Is(err, ErrMalformedToken) {
		t.Fatalf("malformed version accepted: %v", err)
	}
}
