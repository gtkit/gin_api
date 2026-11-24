//go:build integration

package http

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gtkit/gin-api/internal/pkg/ratelimit"
	"github.com/gtkit/gin-api/internal/router"
	"github.com/gtkit/gin-api/internal/testutil"
	"github.com/redis/go-redis/v9"
)

func TestIntegrationFileListSharesRedisQuotaAcrossRouters(t *testing.T) {
	client := testutil.RealRedisOrSkip(t)
	prefix := testutil.UniqueKeyPrefix(t, client)
	keys, err := ratelimit.NewKeyBuilder(strings.Repeat("s", 32), prefix)
	if err != nil {
		t.Fatal(err)
	}

	newReplica := func() (http.Handler, *redis.Client) {
		options := *client.Options()
		rc := redis.NewClient(&options)
		t.Cleanup(func() { _ = rc.Close() })
		backend, err := ratelimit.NewRedisBackend(t.Context(), rc, ratelimit.WithKeyPrefix(prefix))
		if err != nil {
			t.Fatal(err)
		}
		registry := ratelimit.NewRegistry()
		t.Cleanup(func() {
			if err := registry.Close(); err != nil {
				t.Error(err)
			}
		})
		user, err := ratelimit.NewFallbackBackend(backend, ratelimit.Policy{
			ID: "files.list.user", Rate: 0.1, Burst: 2, Scope: ratelimit.ScopeUser,
			Store: ratelimit.StoreRedis, FailureMode: ratelimit.FailureLocalFallback, MaxKeys: 100,
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := registry.Register(user); err != nil {
			t.Fatal(err)
		}
		for _, id := range []string{"files.list.ip", "upload.merge.ip", "upload.merge.user"} {
			scope := ratelimit.ScopeIP
			if id == "upload.merge.user" {
				scope = ratelimit.ScopeUser
			}
			limiter, err := ratelimit.NewLocalBackend(ratelimit.Policy{
				ID: id, Rate: 10, Burst: 20, Scope: scope, Store: ratelimit.StoreLocal,
				FailureMode: ratelimit.FailureClosed, MaxKeys: 100,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := registry.Register(limiter); err != nil {
				t.Fatal(err)
			}
		}
		registry.Freeze()
		stage, _, check := router.NewRateLimitStager(router.Options{RateLimits: registry, RateLimitKeys: keys})
		replica := newTestRouterWithHooks(t, true, nil, nil, func(deps *Deps) { deps.RateLimit = stage })
		if err := check(); err != nil {
			t.Fatal(err)
		}
		return replica, rc
	}

	first, firstRedis := newReplica()
	second, _ := newReplica()
	request := func(replica http.Handler, owner string) int {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/v1/files", nil)
		req.Header.Set("X-Test-Owner", owner)
		rec := httptest.NewRecorder()
		replica.ServeHTTP(rec, req)
		return rec.Code
	}
	for _, tc := range []struct {
		replica http.Handler
		owner   string
		want    int
	}{
		{first, "", http.StatusOK},
		{second, "", http.StatusOK},
		{first, "", http.StatusTooManyRequests},
		{second, "other", http.StatusOK},
	} {
		if status := request(tc.replica, tc.owner); status != tc.want {
			t.Fatalf("list status=%d, want %d", status, tc.want)
		}
	}
	if err := firstRedis.Close(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []int{http.StatusOK, http.StatusOK, http.StatusTooManyRequests} {
		if status := request(first, ""); status != want {
			t.Fatalf("list after Redis transport failure: status=%d, want %d", status, want)
		}
	}
}
