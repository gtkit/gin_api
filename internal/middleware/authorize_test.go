package middleware

import (
	"context"
	"errors"
	"fmt"
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
	jwt := newTestJWT(t)

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
			engine.Use(mustAuthenticate(t, fakeUserVerifier{valid: true}, jwt.Parser, false))
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
	jwt := newTestJWT(t)

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(mustAuthenticate(t, fakeUserVerifier{valid: true}, jwt.Parser, true))
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
	authorize := Authorize(&stubAuthorizer{allowed: true}, "test.authorizer")
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
			authorize := Authorize(tt.authorizer, "test.authorizer")
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

func TestAuthorizePreservesContextErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)
	tests := []struct {
		name       string
		err        error
		wantStatus int
		wantWrite  bool
	}{
		{name: "canceled", err: context.Canceled},
		{name: "wrapped cancellation", err: fmt.Errorf("role lookup: %w", context.Canceled)},
		{name: "cancellation with deadline", err: errors.Join(context.Canceled, context.DeadlineExceeded)},
		{name: "deadline", err: context.DeadlineExceeded, wantStatus: http.StatusGatewayTimeout, wantWrite: true},
		{name: "wrapped deadline", err: fmt.Errorf("role lookup: %w", context.DeadlineExceeded), wantStatus: http.StatusGatewayTimeout, wantWrite: true},
		{name: "cancellation with dependency failure", err: errors.Join(context.Canceled, errors.New("role source down")), wantStatus: http.StatusServiceUnavailable, wantWrite: true},
		{name: "deadline with dependency failure", err: errors.Join(context.DeadlineExceeded, errors.New("role source down")), wantStatus: http.StatusServiceUnavailable, wantWrite: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var wrote, reachedHandler bool
			engine := gin.New()
			engine.Use(func(c *gin.Context) {
				c.Next()
				wrote = c.Writer.Written()
			}, ErrorHandler(), func(c *gin.Context) {
				ginc.SetPrincipal(c, sharedauth.Principal{UserID: 42})
				c.Next()
			}, Authorize(&stubAuthorizer{err: tt.err}, "role.admin"))
			engine.GET("/protected", func(c *gin.Context) {
				reachedHandler = true
				c.Status(http.StatusNoContent)
			})
			w := httptest.NewRecorder()
			engine.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/protected", nil))
			if reachedHandler {
				t.Fatal("authorization failure reached the protected handler")
			}
			if wrote != tt.wantWrite {
				t.Fatalf("response written = %t, want %t", wrote, tt.wantWrite)
			}
			if tt.wantWrite && w.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", w.Code, tt.wantStatus)
			}
			if !tt.wantWrite && w.Body.Len() != 0 {
				t.Fatal("client cancellation wrote a response body")
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
	authorize := Authorize(auth, "test.authorizer")
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

// TestAuthAndAuthorizeRejectMissingDependencies 钉住依赖缺失在装配期失败。
//
// panic 而不是返回 error：这与 gin 生态的惯例一致（gin-contrib 的中间件同样
// 在配置非法时 panic），而且它发生在**启动期**——一个没有 parser 的认证
// 中间件若被静默接受，每个请求都会在生产上崩，那才是真正危险的形态。
func TestAuthAndAuthorizeRejectMissingDependencies(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		build func()
	}{
		{name: "no parser", build: func() {
			Auth(AuthOptions{Parser: nil, Verifier: fakeUserVerifier{}})
		}},
		{name: "no verifier", build: func() {
			Auth(AuthOptions{Parser: newTestParser(t), Verifier: nil})
		}},
		{name: "no authorizer", build: func() { Authorize(nil, "x") }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer func() {
				if recover() == nil {
					t.Fatal("a missing dependency was accepted at assembly time: " +
						"every request would then fail in production")
				}
			}()
			tt.build()
		})
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
