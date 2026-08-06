package http

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func passthrough() gin.HandlerFunc {
	return func(c *gin.Context) { c.Next() }
}

// TestRegisterMountsEveryRoute 钉住四条认证路由都注册上了。
func TestRegisterMountsEveryRoute(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	Register(engine.Group("/api/v1"), Deps{Auth: passthrough()})

	got := map[string]struct{}{}
	for _, r := range engine.Routes() {
		got[r.Method+" "+r.Path] = struct{}{}
	}

	for _, want := range []string{
		"POST /api/v1/auth/register",
		"POST /api/v1/auth/login",
		"POST /api/v1/auth/refresh",
		"POST /api/v1/auth/logout",
	} {
		if _, ok := got[want]; !ok {
			t.Errorf("route %q was not registered", want)
		}
	}
}

// TestOnlyLogoutRequiresAuth 钉住认证端点的公开/鉴权划分。
//
// 这条划分很容易写反，而两个方向都坏：
//   - 登录/注册/刷新若要求鉴权，没有令牌的客户端永远拿不到令牌——死锁；
//   - 登出若不要求鉴权，任何人都能调用它，而它要撤销的正是**本次请求所用的
//     令牌**，拿不到令牌标识就只能报错或什么都不做。
//
// 描述符机制移除后，这两件事都由代码位置决定，没有任何编译期信号。
func TestOnlyLogoutRequiresAuth(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	var authed []string
	engine := gin.New()
	Register(engine.Group("/api/v1"), Deps{
		Auth: func(c *gin.Context) {
			authed = append(authed, c.FullPath())
			c.AbortWithStatus(http.StatusTeapot)
		},
	})

	public := []string{
		"/api/v1/auth/register",
		"/api/v1/auth/login",
		"/api/v1/auth/refresh",
	}
	for _, path := range public {
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, nil))
		if rec.Code == http.StatusTeapot {
			t.Errorf("%s requires authentication: a client with no token could never "+
				"obtain one", path)
		}
	}

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil))
	if rec.Code != http.StatusTeapot {
		t.Error("logout does not require authentication: it must revoke the token used " +
			"by this very request, and without authentication that identity is unavailable")
	}
	if len(authed) != 1 {
		t.Errorf("auth ran on %d routes, want exactly 1 (logout): %v", len(authed), authed)
	}
}

// TestCredentialAndTokenEndpointsUseDifferentLimits 钉住限流分档。
//
// 凭据端点是全站唯一接受口令猜测的入口，与令牌端点共用一个额度等于
// 给爆破留出与正常刷新同样宽的通道。
func TestCredentialAndTokenEndpointsUseDifferentLimits(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	requested := map[string]bool{}
	engine := gin.New()
	Register(engine.Group("/api/v1"), Deps{
		Auth: passthrough(),
		RateLimit: func(ids ...string) (preAuth, postAuth []gin.HandlerFunc) {
			for _, id := range ids {
				requested[id] = true
				preAuth = append(preAuth, passthrough())
			}
			return preAuth, nil
		},
	})

	for _, id := range []string{"auth.credential", "auth.token"} {
		if !requested[id] {
			t.Errorf("rate limit policy %q was never requested", id)
		}
	}
	if requested["default.ip"] || requested["default.user"] {
		t.Error("auth endpoints fell back to the business default limits: those are far " +
			"too loose for the only endpoints that accept password guesses")
	}
}
