//go:build integration

package responsecache

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/testutil"
	"github.com/redis/go-redis/v9"
)

// redisView is only used by the real Redis integration contract below.
type redisView struct {
	client     *redis.Client
	configured bool
	wantDB     int
	t          *testing.T
}

func (p redisView) Client(db int) *redis.Client {
	if db != p.wantDB {
		p.t.Fatalf("Redis DB=%d want=%d", db, p.wantDB)
	}
	return p.client
}

func (p redisView) DefaultClient() *redis.Client { return p.client }
func (p redisView) Configured() bool             { return p.configured }

func TestIntegrationExplicitPrefixesIsolateResponseCaches(t *testing.T) {
	client := testutil.RealRedisOrSkip(t)
	if client == nil {
		return
	}
	owned := testutil.UniqueKeyPrefix(t, client)
	for _, suffix := range []string{"a", "b"} {
		prefix := owned + suffix
		cfg := &config.Config{Cache: config.CacheConfig{Enabled: true, Store: config.CacheStoreRedis, RedisDB: client.Options().DB, TTL: 7 * time.Second}}
		cfg.App.Name = "same-application"
		cfg.Redis.Prefix = prefix
		p := mustOpen(t, Options{Config: cfg, Redis: redisView{client: client, configured: true, wantDB: client.Options().DB, t: t}})
		t.Cleanup(func() {
			if err := p.Close(context.WithoutCancel(t.Context())); err != nil {
				t.Errorf("stop cache: %v", err)
			}
		})
		engine := gin.New()
		calls := 0
		engine.GET("/public", p.Middleware(3*time.Second), func(c *gin.Context) { calls++; c.String(http.StatusOK, suffix) })
		for range 2 {
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/public", nil))
			if response.Code != 200 || response.Body.String() != suffix {
				t.Fatalf("cross-prefix response: %d %q", response.Code, response.Body.String())
			}
		}
		if calls != 1 {
			t.Fatalf("handler calls=%d", calls)
		}
		var keys []string
		// Scan the owned family to completion, without inspecting other namespaces.
		var cursor uint64
		for {
			batch, next, scanErr := client.Scan(t.Context(), cursor, prefix+":httpcache:*", 100).Result()
			if scanErr != nil {
				t.Fatal(scanErr)
			}
			keys = append(keys, batch...)
			cursor = next
			if cursor == 0 {
				break
			}
		}
		if len(keys) != 1 || !strings.HasPrefix(keys[0], prefix+":httpcache:") {
			t.Fatalf("keys=%v", keys)
		}
		ttl, err := client.TTL(t.Context(), keys[0]).Result()
		if err != nil || ttl <= 0 || ttl > 3*time.Second {
			t.Fatalf("TTL=%v err=%v", ttl, err)
		}
		t.Logf("owned namespace=%s key count=%d ttl=%s", prefix, len(keys), ttl)
	}
}
