package http

import (
	"net/http"
	"net/http/httptest"
	"strings"
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
		RateLimit: func(_ string, ids ...string) []gin.HandlerFunc {
			var mws []gin.HandlerFunc
			for _, id := range ids {
				requested[id] = true
				mws = append(mws, passthrough())
			}
			return mws
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

// TestLogoutBodyGovernanceRunsBeforeAuth 补上 order-body-governance-before-auth
// 自陈缺失的那条判据。
//
// `/logout` 的 body 治理与认证都在路由级，改动只是把两个中间件换序——而换序
// **没有任何测试兜住**：换回去不会有任何失败。交叉验证把它列为"无判据"，这里补上。
//
// 断言执行序而非注册序：注册顺序对了但被别的中间件插队同样会坏。
func TestLogoutBodyGovernanceRunsBeforeAuth(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	var authRan bool
	engine := gin.New()
	Register(engine.Group("/api/v1"), Deps{
		Auth: func(c *gin.Context) { authRan = true; c.Next() },
	})

	// 只接受 JSON 的路由，故意送 text/plain。
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", strings.NewReader("plain"))
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415（Content-Type 不符应由 body 治理拒绝）", rec.Code)
	}
	if authRan {
		t.Error("认证在 body 治理之前执行：一个内容类型不符的请求先付掉了" +
			"令牌解析与撤销查询，而 body 治理只读 header")
	}
}
