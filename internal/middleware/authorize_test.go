package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/gtkit/gin-api/internal/pkg/ginc"
	sharedauth "github.com/gtkit/gin-api/internal/shared/auth"
)

type stubAuthorizer struct {
	allowed bool
	err     error
	seen    sharedauth.AuthorizationRequest
	subject int64
}

func (s *stubAuthorizer) Authorize(
	_ context.Context, p sharedauth.Principal, r sharedauth.AuthorizationRequest,
) (bool, error) {
	s.seen = r
	s.subject = p.UserID
	return s.allowed, s.err
}

// TestAuthenticateRejectsMalformedToken 覆盖 token 的输入边界。
//
// 这些检查必须在**解析与缓存之前**：没有它们，超长或含空白的输入会迫使
// 解析器与缓存做无谓的工作，空白还会让 " abc" 与 "abc" 在缓存键、审计与
// 限流键上产生歧义。
func TestAuthenticateRejectsMalformedToken(t *testing.T) {
	issuer := newTestIssuer(t)

	tests := []struct {
		name  string
		token string
	}{
		{name: "leading space", token: " abc"},
		{name: "trailing space", token: "abc "},
		{name: "inner space", token: "ab c"},
		{name: "tab", token: "ab\tc"},
		{name: "too long", token: strings.Repeat("a", (8<<10)+1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			engine := gin.New()
			engine.Use(mustAuthenticate(t, fakeUserVerifier{valid: true}, issuer, false))
			engine.GET("/x", func(c *gin.Context) { c.Status(http.StatusNoContent) })

			req := httptest.NewRequest(http.MethodGet, "/x", nil)
			req.Header.Set("App-Token", tt.token)
			w := httptest.NewRecorder()
			engine.ServeHTTP(w, req)

			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d", w.Code, http.StatusUnauthorized)
			}
		})
	}
}

// TestOptionalAuthRejectsInvalidToken 是可选认证最关键的一条：
// 缺 token 可以匿名放行，但**带了非法 token 必须拒绝**——
// 否则攻击者只要发一个坏 token 就能绕过校验以匿名身份继续。
func TestOptionalAuthRejectsInvalidToken(t *testing.T) {
	issuer := newTestIssuer(t)

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(mustAuthenticate(t, fakeUserVerifier{valid: true}, issuer, true))
	engine.GET("/x", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	t.Run("missing token passes through", func(t *testing.T) {
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
		if w.Code != http.StatusNoContent {
			t.Fatalf("status = %d, want %d", w.Code, http.StatusNoContent)
		}
	})

	t.Run("invalid token is rejected", func(t *testing.T) {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.Header.Set("App-Token", "not-a-real-token")
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want %d (a supplied-but-invalid token must not fall back to anonymous)",
				w.Code, http.StatusUnauthorized)
		}
	})
}

func TestAuthorizeRequiresAuthenticatedPrincipal(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	// 刻意不装配认证：授权拿不到身份时必须拒绝，绝不能解释成"允许"。
	authorize, err := Authorize(&stubAuthorizer{allowed: true}, "test.authorizer")
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	engine.Use(authorize)
	engine.GET("/x", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/x", nil))
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusUnauthorized)
	}
}

// TestAuthorizeMapsOutcomes 锁住三种结果的映射。
//
// 最重要的是第三条：依赖故障 MUST 映射 503 而非 403。
// 把"角色数据源挂了"报成 403，会让一次基础设施故障表现为全体用户突然失去权限，
// 而客户端看到 403 只会以为自己真的没权限。
func TestAuthorizeMapsOutcomes(t *testing.T) {
	tests := []struct {
		name       string
		authorizer *stubAuthorizer
		wantStatus int
	}{
		{name: "allowed", authorizer: &stubAuthorizer{allowed: true}, wantStatus: http.StatusNoContent},
		{name: "denied", authorizer: &stubAuthorizer{allowed: false}, wantStatus: http.StatusForbidden},
		{
			name:       "dependency failure is 503 not 403",
			authorizer: &stubAuthorizer{err: errors.New("role source down")},
			wantStatus: http.StatusServiceUnavailable,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			engine := gin.New()
			engine.Use(func(c *gin.Context) {
				ginc.SetPrincipal(c, sharedauth.Principal{UserID: 42, Roles: []string{"user"}})
				c.Next()
			})
			authorize, err := Authorize(tt.authorizer, "test.authorizer")
			if err != nil {
				t.Fatalf("Authorize: %v", err)
			}
			engine.Use(authorize)
			engine.GET("/thing/:id", func(c *gin.Context) { c.Status(http.StatusNoContent) })

			w := httptest.NewRecorder()
			engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/thing/7", nil))
			if w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", w.Code, tt.wantStatus)
			}
		})
	}
}

// TestAuthorizeRequestIsNeutralAndLowCardinality 锁住授权输入的形状：
// 用路由模式而非实际路径，才能作为低基数 metrics label；
// 不传 HTTP 上下文对象，避免授权实现读 body 或写响应。
func TestAuthorizeRequestIsNeutralAndLowCardinality(t *testing.T) {
	auth := &stubAuthorizer{allowed: true}

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		ginc.SetPrincipal(c, sharedauth.Principal{UserID: 99})
		c.Next()
	})
	authorize, err := Authorize(auth, "test.authorizer")
	if err != nil {
		t.Fatalf("Authorize: %v", err)
	}
	engine.Use(authorize)
	engine.GET("/thing/:id", func(c *gin.Context) { c.Status(http.StatusNoContent) })

	engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/thing/12345", nil))

	if auth.seen.RoutePattern != "/thing/:id" {
		t.Fatalf("RoutePattern = %q, want the registered pattern (not the concrete path)", auth.seen.RoutePattern)
	}
	if auth.seen.Method != http.MethodGet {
		t.Fatalf("Method = %q, want GET", auth.seen.Method)
	}
	if auth.seen.PathParams["id"] != "12345" {
		t.Fatalf("PathParams = %v, want id=12345", auth.seen.PathParams)
	}
	if auth.subject != 99 {
		t.Fatalf("principal subject = %d, want 99", auth.subject)
	}
}

func TestAuthenticateAndAuthorizeRejectMissingDependencies(t *testing.T) {
	t.Parallel()

	if _, err := Authenticate(AuthOptions{Parser: nil, Verifier: fakeUserVerifier{}}); err == nil {
		t.Fatal("missing parser must be an assembly error, not a panic")
	}
	if _, err := Authenticate(AuthOptions{Parser: newTestIssuer(t), Verifier: nil}); err == nil {
		t.Fatal("missing verifier must be an assembly error, not a panic")
	}
	if _, err := Authorize(nil, "x"); err == nil {
		t.Fatal("missing authorizer must be an assembly error, not a panic")
	}
}

// TestDenyAllAuthorizerAlwaysDenies 锁住占位实现的方向。
//
// "先放行、以后再补"的占位会在补上之前一直是一个敞开的授权缺口，
// 而且它不会以任何方式提醒你它还没做完。
func TestDenyAllAuthorizerAlwaysDenies(t *testing.T) {
	t.Parallel()

	d := sharedauth.DenyAll("no authoritative source yet")
	allowed, err := d.Authorize(context.Background(),
		sharedauth.Principal{UserID: 1, Roles: []string{"admin"}},
		sharedauth.AuthorizationRequest{Method: http.MethodGet, RoutePattern: "/x"})
	if err != nil {
		t.Fatalf("DenyAll must not report a dependency failure: %v", err)
	}
	if allowed {
		t.Fatal("DenyAll must deny even a principal carrying an admin role claim")
	}
	if d.Reason == "" {
		t.Fatal("DenyAll must carry a reason so that a persistent 403 can be located")
	}
}
