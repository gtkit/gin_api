package http

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/gin-api/internal/middleware"
	"github.com/gtkit/gin-api/internal/module/auth/application"
)

type waitingCredentials struct {
	stubCreds
	entered chan struct{}
	calls   atomic.Int64
}

func (s *waitingCredentials) FindByPhone(ctx context.Context, _ string) (application.Credential, bool, error) {
	s.calls.Add(1)
	select {
	case s.entered <- struct{}{}:
	default:
	}
	<-ctx.Done()
	return application.Credential{}, false, ctx.Err()
}

func TestCredentialAdmissionPreservesTokenCapacityBeforeDatabase(t *testing.T) {
	credentials := &waitingCredentials{entered: make(chan struct{}, 1)}
	service, err := application.NewService(application.Options{
		Credentials: credentials, Hasher: stubHasher{}, AccessIssuer: stubIssuer{},
		RefreshStore: stubRefresh{}, RevocationList: stubRevocation{}, AccessTTL: time.Hour,
	})
	if err != nil {
		t.Fatal(err)
	}
	engine := gin.New()
	group := engine.Group("/api/v1", middleware.GinRecovery(false), middleware.Admission(3, nil))
	Register(group, Deps{Service: service, Auth: passthrough(), CredentialAdmission: middleware.Admission(1, nil)})
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		req := httptest.NewRequestWithContext(ctx, http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"phone":"13800138000","password":"ok"}`))
		req.Header.Set("Content-Type", "application/json")
		engine.ServeHTTP(httptest.NewRecorder(), req)
	}()
	<-credentials.entered
	t.Cleanup(func() { cancel(); <-done })
	request := func(path, payload string) int {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/"+path, strings.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, req)
		return response.Code
	}
	if status := request("login", `{"phone":"13800138000","password":"ok"}`); status != 503 {
		t.Fatalf("second login = %d", status)
	}
	if status := request("register", `{"phone":"13800138001","password":"strong-password"}`); status != 503 {
		t.Fatalf("register = %d", status)
	}
	if credentials.calls.Load() != 1 {
		t.Fatal("rejected credential request reached database")
	}
	if status := request("refresh", `{"refresh_token":"valid"}`); status != 200 {
		t.Fatalf("refresh = %d", status)
	}
	if status := request("logout", `{}`); status == 503 {
		t.Fatal("logout occupied a credential slot")
	}
	cancel()
	<-done
	if status := request("register", `{"phone":"13800138001","password":"strong-password"}`); status != 200 {
		t.Fatalf("credential slot not released after cancellation: %d", status)
	}
}

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
