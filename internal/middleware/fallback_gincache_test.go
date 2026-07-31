package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/gincache"
	"github.com/gtkit/gincache/persist"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// newTestRedis 启动一个内存 Redis 用于测试 gincache。
func newTestRedis(t *testing.T) *redis.Client {
	t.Helper()
	s := miniredis.RunT(t)
	return redis.NewClient(&redis.Options{Addr: s.Addr()})
}

func newTestGincache(t *testing.T, rdb *redis.Client) *gincache.Middleware {
	t.Helper()
	store := persist.NewRedisStore(rdb,
		persist.WithKeyPrefix("test:cache"),
		persist.WithReadTimeout(time.Second),
		persist.WithWriteTimeout(time.Second),
	)
	return gincache.New(store, 30*time.Second)
}

// TestFallbackWithGincache_NormalFlow 验证正常流程：
// gincache miss → Handler 200 → fallback 缓存 + gincache 缓存 → 下次 gincache 命中。
func TestFallbackWithGincache_NormalFlow(t *testing.T) {
	fc := newTestFallbackCache(t)
	rdb := newTestRedis(t)
	gc := newTestGincache(t, rdb)

	handlerCalls := 0
	engine := gin.New()
	// 正确顺序：fallback → gincache → handler
	engine.GET("/api",
		FallbackMiddleware(fc),
		gc.CacheByPath(),
		func(c *gin.Context) {
			handlerCalls++
			c.JSON(http.StatusOK, gin.H{"data": "fresh"})
		},
	)

	// 第一次请求：gincache miss → Handler 执行 → 两层都缓存。
	req1 := httptest.NewRequest(http.MethodGet, "/api", nil)
	rec1 := httptest.NewRecorder()
	engine.ServeHTTP(rec1, req1)

	if rec1.Code != http.StatusOK {
		t.Fatalf("req1: status = %d, want 200", rec1.Code)
	}
	if !strings.Contains(rec1.Body.String(), "fresh") {
		t.Fatalf("req1: body = %q", rec1.Body.String())
	}
	if handlerCalls != 1 {
		t.Fatalf("handler should be called once, got %d", handlerCalls)
	}

	// 验证 fallback 本地缓存已写入。
	if _, ok := fc.Get("/api"); !ok {
		t.Fatal("fallback cache should have data after first request")
	}

	// 第二次请求：gincache 命中 → Handler 不执行 → fallback 也看到 200。
	req2 := httptest.NewRequest(http.MethodGet, "/api", nil)
	rec2 := httptest.NewRecorder()
	engine.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("req2: status = %d, want 200", rec2.Code)
	}
	if handlerCalls != 1 {
		t.Fatalf("handler should not be called again (gincache hit), got %d", handlerCalls)
	}
}

// TestFallbackWithGincache_RedisDownDBDown 验证核心降级场景：
// Redis 挂 + DB 挂 → gincache miss → Handler 500 → fallback 返回本地缓存。
func TestFallbackWithGincache_RedisDownDBDown(t *testing.T) {
	fc := newTestFallbackCache(t)
	rdb := newTestRedis(t)
	gc := newTestGincache(t, rdb)

	callCount := 0
	engine := gin.New()
	engine.GET("/api",
		FallbackMiddleware(fc),
		gc.CacheByPath(),
		func(c *gin.Context) {
			callCount++
			if callCount == 1 {
				c.JSON(http.StatusOK, gin.H{"data": "original"})
				return
			}
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db down"})
		},
	)

	// 第一次：正常，两层缓存都写入。
	req1 := httptest.NewRequest(http.MethodGet, "/api", nil)
	rec1 := httptest.NewRecorder()
	engine.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("req1: status = %d", rec1.Code)
	}

	// 模拟 Redis 挂掉：清空 Redis 让 gincache miss。
	rdb.FlushAll(context.Background())

	// 第二次：gincache miss → Handler 500 → fallback 返回本地缓存。
	req2 := httptest.NewRequest(http.MethodGet, "/api", nil)
	rec2 := httptest.NewRecorder()
	engine.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("req2: status = %d, want 200 (degraded)", rec2.Code)
	}
	if rec2.Header().Get("X-Degraded") != "true" {
		t.Fatal("req2: expected X-Degraded header")
	}
	if !strings.Contains(rec2.Body.String(), "original") {
		t.Fatalf("req2: body = %q, want contains 'original'", rec2.Body.String())
	}
}

// TestFallbackWithGincache_GincacheHitBypassesFallback 验证：
// gincache 命中时，fallback 只是看到 200 响应并更新本地缓存，不会干扰。
func TestFallbackWithGincache_GincacheHitBypassesFallback(t *testing.T) {
	fc := newTestFallbackCache(t)
	rdb := newTestRedis(t)
	gc := newTestGincache(t, rdb)

	engine := gin.New()
	engine.GET("/api",
		FallbackMiddleware(fc),
		gc.CacheByPath(),
		func(c *gin.Context) {
			c.JSON(http.StatusOK, gin.H{"data": "value"})
		},
	)

	// 预热 gincache。
	req1 := httptest.NewRequest(http.MethodGet, "/api", nil)
	rec1 := httptest.NewRecorder()
	engine.ServeHTTP(rec1, req1)

	// gincache 命中后，fallback 不应加 X-Degraded。
	req2 := httptest.NewRequest(http.MethodGet, "/api", nil)
	rec2 := httptest.NewRecorder()
	engine.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec2.Code)
	}
	if rec2.Header().Get("X-Degraded") == "true" {
		t.Fatal("gincache hit should not have X-Degraded header")
	}
	// gincache 会加 X-Cache: HIT。
	if rec2.Header().Get("X-Cache") != "HIT" {
		t.Fatal("expected X-Cache: HIT from gincache")
	}
}
