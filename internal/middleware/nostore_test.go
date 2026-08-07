package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestNoStoreSetsCacheControl 钉住敏感响应不缓存（local-authentication spec）：
// 令牌一旦进浏览器缓存/共享代理/CDN，寿命就不再由服务端控制。
func TestNoStoreSetsCacheControl(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	engine.Use(NoStore())
	engine.GET("/token", func(c *gin.Context) { c.String(http.StatusOK, "secret") })

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/token", nil))

	if got := recorder.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("Cache-Control = %q, want no-store", got)
	}
}
