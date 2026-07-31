package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/pkg/cache"

	"github.com/gin-gonic/gin"
)

func init() {
	gin.SetMode(gin.TestMode)
}

func newTestFallbackCache(t *testing.T) *cache.FallbackCache {
	t.Helper()
	fc, err := cache.NewFallbackCache("test", 5*time.Minute, 1)
	if err != nil {
		t.Fatalf("create fallback cache: %v", err)
	}
	return fc
}

// --- 基本功能 ---

func TestFallback_SuccessResponseIsCached(t *testing.T) {
	fc := newTestFallbackCache(t)

	engine := gin.New()
	engine.Use(FallbackMiddleware(fc))
	engine.GET("/api", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{"data": "fresh"})
	})

	req := httptest.NewRequest(http.MethodGet, "/api", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "fresh") {
		t.Fatalf("body = %q, want contains 'fresh'", rec.Body.String())
	}
	if _, ok := fc.Get("/api"); !ok {
		t.Fatal("expected data to be cached after successful response")
	}
}

func TestFallback_5xxServedFromCache(t *testing.T) {
	fc := newTestFallbackCache(t)

	callCount := 0
	engine := gin.New()
	engine.Use(FallbackMiddleware(fc))
	engine.GET("/api", func(c *gin.Context) {
		callCount++
		if callCount == 1 {
			c.JSON(http.StatusOK, gin.H{"data": "cached-value"})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db down"})
	})

	// 第一次：成功，写入缓存。
	req1 := httptest.NewRequest(http.MethodGet, "/api", nil)
	rec1 := httptest.NewRecorder()
	engine.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("first request: status = %d, want 200", rec1.Code)
	}

	// 第二次：Handler 返回 500，中间件返回缓存数据。
	req2 := httptest.NewRequest(http.MethodGet, "/api", nil)
	rec2 := httptest.NewRecorder()
	engine.ServeHTTP(rec2, req2)

	if rec2.Code != http.StatusOK {
		t.Fatalf("degraded request: status = %d, want 200", rec2.Code)
	}
	if rec2.Header().Get("X-Degraded") != "true" {
		t.Fatal("expected X-Degraded header")
	}
	if !strings.Contains(rec2.Body.String(), "cached-value") {
		t.Fatalf("body = %q, want contains 'cached-value'", rec2.Body.String())
	}
}

func TestFallback_5xxWithoutCacheReturnsEmptyDegradedResponse(t *testing.T) {
	fc := newTestFallbackCache(t)

	engine := gin.New()
	engine.Use(FallbackMiddleware(fc))
	engine.GET("/api", func(c *gin.Context) {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db down"})
	})

	// 没有缓存：返回友好的空数据响应，而不是透传 500。
	req := httptest.NewRequest(http.MethodGet, "/api", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (empty degraded)", rec.Code)
	}
	if rec.Header().Get("X-Degraded") != "true" {
		t.Fatal("expected X-Degraded header")
	}
	if !strings.Contains(rec.Body.String(), `"data":[]`) {
		t.Fatalf("body = %q, want empty data array", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "db down") {
		t.Fatal("should not expose internal error message")
	}
}

func TestFallback_4xxNotCached(t *testing.T) {
	fc := newTestFallbackCache(t)

	engine := gin.New()
	engine.Use(FallbackMiddleware(fc))
	engine.GET("/api", func(c *gin.Context) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "bad params"})
	})

	req := httptest.NewRequest(http.MethodGet, "/api", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if _, ok := fc.Get("/api"); ok {
		t.Fatal("4xx response should not be cached")
	}
}

// --- Query Params 隔离 ---

func TestFallback_DifferentQueryParamsAreCachedSeparately(t *testing.T) {
	fc := newTestFallbackCache(t)

	callCount := 0
	engine := gin.New()
	engine.Use(FallbackMiddleware(fc))
	engine.GET("/api", func(c *gin.Context) {
		callCount++
		page := c.Query("page")
		if callCount <= 2 {
			c.JSON(http.StatusOK, gin.H{"page": page})
			return
		}
		c.JSON(http.StatusInternalServerError, gin.H{"error": "db down"})
	})

	// 缓存 page=1
	req1 := httptest.NewRequest(http.MethodGet, "/api?page=1", nil)
	rec1 := httptest.NewRecorder()
	engine.ServeHTTP(rec1, req1)
	if rec1.Code != http.StatusOK {
		t.Fatalf("page1: status = %d", rec1.Code)
	}

	// 缓存 page=2
	req2 := httptest.NewRequest(http.MethodGet, "/api?page=2", nil)
	rec2 := httptest.NewRecorder()
	engine.ServeHTTP(rec2, req2)
	if rec2.Code != http.StatusOK {
		t.Fatalf("page2: status = %d", rec2.Code)
	}

	// Handler 挂了，page=1 返回 page=1 的缓存。
	req3 := httptest.NewRequest(http.MethodGet, "/api?page=1", nil)
	rec3 := httptest.NewRecorder()
	engine.ServeHTTP(rec3, req3)

	if rec3.Code != http.StatusOK {
		t.Fatalf("degraded page1: status = %d", rec3.Code)
	}
	if !strings.Contains(rec3.Body.String(), `"page":"1"`) {
		t.Fatalf("expected page 1 data, got %q", rec3.Body.String())
	}

	// page=2 也返回 page=2 的缓存。
	req4 := httptest.NewRequest(http.MethodGet, "/api?page=2", nil)
	rec4 := httptest.NewRecorder()
	engine.ServeHTTP(rec4, req4)

	if !strings.Contains(rec4.Body.String(), `"page":"2"`) {
		t.Fatalf("expected page 2 data, got %q", rec4.Body.String())
	}
}

// --- 恢复切换 ---

func TestFallback_RecoveryAfterDegradation(t *testing.T) {
	fc := newTestFallbackCache(t)

	callCount := 0
	engine := gin.New()
	engine.Use(FallbackMiddleware(fc))
	engine.GET("/api", func(c *gin.Context) {
		callCount++
		switch callCount {
		case 1:
			c.JSON(http.StatusOK, gin.H{"data": "v1"})
		case 2:
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db down"})
		case 3:
			c.JSON(http.StatusOK, gin.H{"data": "v2"})
		}
	})

	// 1. 正常返回 v1，缓存。
	req1 := httptest.NewRequest(http.MethodGet, "/api", nil)
	rec1 := httptest.NewRecorder()
	engine.ServeHTTP(rec1, req1)
	if !strings.Contains(rec1.Body.String(), "v1") {
		t.Fatalf("expected v1, got %q", rec1.Body.String())
	}

	// 2. 500 → 返回缓存 v1。
	req2 := httptest.NewRequest(http.MethodGet, "/api", nil)
	rec2 := httptest.NewRecorder()
	engine.ServeHTTP(rec2, req2)
	if rec2.Header().Get("X-Degraded") != "true" {
		t.Fatal("expected degraded response")
	}
	if !strings.Contains(rec2.Body.String(), "v1") {
		t.Fatalf("expected cached v1, got %q", rec2.Body.String())
	}

	// 3. 恢复正常 → 返回 v2，更新缓存。
	req3 := httptest.NewRequest(http.MethodGet, "/api", nil)
	rec3 := httptest.NewRecorder()
	engine.ServeHTTP(rec3, req3)
	if rec3.Code != http.StatusOK {
		t.Fatalf("recovery: status = %d", rec3.Code)
	}
	if rec3.Header().Get("X-Degraded") == "true" {
		t.Fatal("should not have X-Degraded after recovery")
	}
	if !strings.Contains(rec3.Body.String(), "v2") {
		t.Fatalf("expected v2, got %q", rec3.Body.String())
	}

	// 验证缓存已更新为 v2。
	cached, ok := fc.Get("/api")
	if !ok {
		t.Fatal("cache should be updated after recovery")
	}
	if !strings.Contains(string(cached), "v2") {
		t.Fatalf("cache should contain v2, got %q", string(cached))
	}
}

// --- 空响应体 ---

func TestFallback_EmptyBodyNotCached(t *testing.T) {
	fc := newTestFallbackCache(t)

	engine := gin.New()
	engine.Use(FallbackMiddleware(fc))
	engine.GET("/api", func(c *gin.Context) {
		c.Status(http.StatusOK)
		// 不写 body。
	})

	req := httptest.NewRequest(http.MethodGet, "/api", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if _, ok := fc.Get("/api"); ok {
		t.Fatal("empty body should not be cached")
	}
}

// --- 并发安全 ---

func TestFallback_ConcurrentSafety(t *testing.T) {
	fc := newTestFallbackCache(t)

	callCount := 0
	var mu sync.Mutex

	engine := gin.New()
	engine.Use(FallbackMiddleware(fc))
	engine.GET("/api", func(c *gin.Context) {
		mu.Lock()
		callCount++
		n := callCount
		mu.Unlock()

		if n <= 5 {
			c.JSON(http.StatusOK, gin.H{"data": "fresh"})
		} else {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "db down"})
		}
	})

	// 前 5 个请求预热缓存。
	for range 5 {
		req := httptest.NewRequest(http.MethodGet, "/api", nil)
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("warm-up: status = %d", rec.Code)
		}
	}

	// 并发 50 个请求，全部 500，应该都从缓存返回。
	var wg sync.WaitGroup
	var degradedCount atomic.Int64
	var errorCount atomic.Int64

	for range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			req := httptest.NewRequest(http.MethodGet, "/api", nil)
			rec := httptest.NewRecorder()
			engine.ServeHTTP(rec, req)

			if rec.Code == http.StatusOK && rec.Header().Get("X-Degraded") == "true" {
				degradedCount.Add(1)
			} else if rec.Code >= 500 {
				errorCount.Add(1)
			}
		}()
	}

	wg.Wait()

	if degradedCount.Load() != 50 {
		t.Fatalf("degraded = %d, want 50 (errors = %d)", degradedCount.Load(), errorCount.Load())
	}
}

// --- 多种 5xx 状态码 ---

func TestFallback_502And503AlsoTriggerDegradation(t *testing.T) {
	for _, code := range []int{502, 503, 504} {
		t.Run(http.StatusText(code), func(t *testing.T) {
			fc := newTestFallbackCache(t)
			callCount := 0

			engine := gin.New()
			engine.Use(FallbackMiddleware(fc))
			engine.GET("/api", func(c *gin.Context) {
				callCount++
				if callCount == 1 {
					c.JSON(http.StatusOK, gin.H{"data": "cached"})
					return
				}
				c.JSON(code, gin.H{"error": "fail"})
			})

			// 预热。
			req1 := httptest.NewRequest(http.MethodGet, "/api", nil)
			rec1 := httptest.NewRecorder()
			engine.ServeHTTP(rec1, req1)

			// 触发降级。
			req2 := httptest.NewRequest(http.MethodGet, "/api", nil)
			rec2 := httptest.NewRecorder()
			engine.ServeHTTP(rec2, req2)

			if rec2.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (degraded from %d)", rec2.Code, code)
			}
			if rec2.Header().Get("X-Degraded") != "true" {
				t.Fatal("expected X-Degraded header")
			}
		})
	}
}

// --- bufferedWriter 通过 Gin engine 间接测试 ---

func TestBufferedWriter_WriteWithoutExplicitStatus(t *testing.T) {
	fc := newTestFallbackCache(t)

	engine := gin.New()
	engine.Use(FallbackMiddleware(fc))
	engine.GET("/api", func(c *gin.Context) {
		// 直接写 body，不显式设 status → 应默认 200。
		_, _ = c.Writer.Write([]byte(`{"data":"raw"}`))
	})

	req := httptest.NewRequest(http.MethodGet, "/api", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "raw") {
		t.Fatalf("body = %q, want contains 'raw'", rec.Body.String())
	}
}

func TestBufferedWriter_WriteStringContent(t *testing.T) {
	fc := newTestFallbackCache(t)

	engine := gin.New()
	engine.Use(FallbackMiddleware(fc))
	engine.GET("/api", func(c *gin.Context) {
		_, _ = c.Writer.WriteString(`{"data":"string-write"}`)
	})

	req := httptest.NewRequest(http.MethodGet, "/api", nil)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "string-write") {
		t.Fatalf("body = %q, want contains 'string-write'", rec.Body.String())
	}
}
