package httpcache

import (
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/platform/config"

	"github.com/gin-gonic/gin"
)

func init() { gin.SetMode(gin.TestMode) }

// newTestStager 建一个有界本地缓存的装配器。
//
// 不绑任何端口：gin 的 ServeHTTP 直接被调用，因此受限环境同样能跑完整判据。
func newTestStager(t *testing.T) *Stager {
	t.Helper()

	stager, err := New(config.CacheConfig{
		Enabled:  true,
		Store:    config.CacheStoreMemory,
		TTL:      time.Minute,
		MaxBytes: 1 << 20,
	}, nil, "test")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = stager.Close() })
	return stager
}

// serve 用给定装配器跑两次同一请求，返回 handler 实际被调用的次数。
func serve(t *testing.T, stager *Stager, mutate func(*http.Request), status int) int32 {
	t.Helper()

	var calls atomic.Int32
	engine := gin.New()
	engine.Use(stager.Middleware(0))
	handler := func(c *gin.Context) {
		calls.Add(1)
		c.JSON(status, gin.H{"n": calls.Load()})
	}
	engine.GET("/items", handler)
	engine.POST("/items", handler)

	for range 2 {
		method := http.MethodGet
		req := httptest.NewRequest(method, "/items", nil)
		if mutate != nil {
			mutate(req)
		}
		if req.Method != http.MethodGet {
			req = httptest.NewRequest(req.Method, "/items", nil)
			if mutate != nil {
				mutate(req)
			}
		}
		engine.ServeHTTP(httptest.NewRecorder(), req)
	}
	return calls.Load()
}

// TestDisabledStagerIsTransparent 钉住关闭态：中间件透传，不构造任何 store。
//
// 关闭时返回透传而不是 nil，调用方才能无条件挂上；否则每个路由注册点都要写
// 一次 if，漏写一处就是行为不一致。
func TestDisabledStagerIsTransparent(t *testing.T) {
	t.Parallel()

	stager, err := New(config.CacheConfig{Enabled: false}, nil, "test")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if stager.Enabled() {
		t.Fatal("关闭态报告为启用")
	}
	if got := serve(t, stager, nil, http.StatusOK); got != 2 {
		t.Fatalf("handler 被调用 %d 次，want 2（关闭态不得缓存）", got)
	}
	if err := stager.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestEnabledStagerCachesAnonymousGet 钉住开启态确实生效。
func TestEnabledStagerCachesAnonymousGet(t *testing.T) {
	t.Parallel()

	stager := newTestStager(t)
	if !stager.Enabled() {
		t.Fatal("开启态报告为未启用")
	}
	if got := serve(t, stager, nil, http.StatusOK); got != 1 {
		t.Fatalf("handler 被调用 %d 次，want 1（第二次应命中缓存）", got)
	}
}

// TestIdentifiedRequestsBypassCache 是本包最重要的一条：带身份的请求绝不进缓存。
//
// 响应缓存的键里没有身份。让带身份的响应进缓存，就是把第一个用户的数据原样
// 发给后面的所有人——不会报错、不会有异常日志，只会在某天被用户发现。
func TestIdentifiedRequestsBypassCache(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*http.Request)
	}{
		{
			name:   "Authorization 头",
			mutate: func(r *http.Request) { r.Header.Set("Authorization", "Bearer token") },
		},
		{
			name:   "Cookie（会话 ID 常常是隐式带上的）",
			mutate: func(r *http.Request) { r.Header.Set("Cookie", "session=abc") },
		},
		{
			name:   "Range（响应是片段，缓存后会被当成完整响应）",
			mutate: func(r *http.Request) { r.Header.Set("Range", "bytes=0-99") },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stager := newTestStager(t)
			if got := serve(t, stager, tt.mutate, http.StatusOK); got != 2 {
				t.Fatalf("handler 被调用 %d 次，want 2：带身份的请求被缓存了", got)
			}
		})
	}
}

// TestNonGetAndErrorResponsesAreNotCached 钉住两类不该缓存的响应。
//
// 非 GET 有副作用，缓存它等于把一次写变成没发生；缓存 4xx/5xx 会把一次瞬时
// 故障固定住整个 TTL，故障恢复后用户仍在看错误页。
func TestNonGetAndErrorResponsesAreNotCached(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*http.Request)
		status int
	}{
		{name: "POST", mutate: func(r *http.Request) { r.Method = http.MethodPost }, status: http.StatusOK},
		{name: "500 响应", status: http.StatusInternalServerError},
		{name: "404 响应", status: http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			stager := newTestStager(t)
			if got := serve(t, stager, tt.mutate, tt.status); got != 2 {
				t.Fatalf("handler 被调用 %d 次，want 2", got)
			}
		})
	}
}

// TestNewRejectsUnusableConfig 钉住构造期拒绝：无界的本地缓存与缺客户端的 redis 后端。
func TestNewRejectsUnusableConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  config.CacheConfig
	}{
		{
			name: "memory 无容量上限",
			cfg:  config.CacheConfig{Enabled: true, Store: config.CacheStoreMemory},
		},
		{
			name: "redis 后端缺客户端",
			cfg:  config.CacheConfig{Enabled: true, Store: config.CacheStoreRedis},
		},
		{
			name: "未知后端",
			cfg:  config.CacheConfig{Enabled: true, Store: "memcached"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, err := New(tt.cfg, nil, "test"); err == nil {
				t.Fatal("构造接受了不可用的配置")
			}
		})
	}
}

// TestNilStagerIsSafe 钉住 nil 装配器可用：装配失败的路径上调用方可能拿到 nil。
func TestNilStagerIsSafe(t *testing.T) {
	t.Parallel()

	var stager *Stager
	if stager.Enabled() {
		t.Fatal("nil Stager 报告为启用")
	}
	if err := stager.Close(); err != nil {
		t.Fatalf("nil Close: %v", err)
	}
	if stager.Middleware(0) == nil {
		t.Fatal("nil Stager 的 Middleware 返回了 nil，调用方会 panic")
	}
}
