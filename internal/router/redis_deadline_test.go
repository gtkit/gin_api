package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/alicebob/miniredis/v2/server"
	"github.com/gtkit/gin-api/internal/platform/config"
	redisplatform "github.com/gtkit/gin-api/internal/platform/redis"
	"github.com/gtkit/gin-api/internal/testutil"
)

type socketReadinessChecker struct{ ping func(context.Context) error }

func (c socketReadinessChecker) PingDB(ctx context.Context) error    { return ctx.Err() }
func (c socketReadinessChecker) PingRedis(ctx context.Context) error { return c.ping(ctx) }
func (socketReadinessChecker) RedisConfigured() bool                 { return true }

func TestReadinessBudgetBoundsRealRedisSocketRead(t *testing.T) {
	testutil.RequireLoopback(t)
	srv := miniredis.RunT(t)
	client, err := redisplatform.OpenRedis(t.Context(), config.RedisConfig{Addr: srv.Addr(), DBs: []int{0}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	})
	engine := newTestEngineWith(t, Options{HealthChecker: socketReadinessChecker{ping: client.HealthCheck}, RedisRequired: true})
	// Initialization and the Redis protocol handshake have already succeeded.
	srv.Server().SetPreHook(func(_ *server.Peer, command string, _ ...string) bool {
		return strings.EqualFold(command, "ping")
	})
	recorder := httptest.NewRecorder()
	started := time.Now()
	engine.ServeHTTP(recorder, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/ready", nil))
	elapsed := time.Since(started)
	if recorder.Code != http.StatusServiceUnavailable || elapsed > 2500*time.Millisecond {
		t.Fatalf("readiness exceeded its socket budget: status=%d elapsed=%s body=%s", recorder.Code, elapsed, recorder.Body.String())
	}
	t.Logf("readiness deadline=%s actual socket wait=%s", readyGlobalDeadline, elapsed)
}
