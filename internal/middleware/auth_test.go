package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	legacyclaims "github.com/gtkit/gojwt/claims"

	"github.com/gtkit/gin-api/internal/pkg/ginc"
)

type fakeUserVerifier struct {
	valid bool
	err   error
}

func (f fakeUserVerifier) ValidateUser(context.Context, int64) (bool, error) {
	return f.valid, f.err
}

func TestAuthAllowsValidToken(t *testing.T) {
	issuer := newTestIssuer(t)

	token, err := issuer.GenerateToken(42, legacyclaims.WithRole("user"))
	if err != nil {
		t.Fatalf("GenerateToken returned error: %v", err)
	}

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(Auth(fakeUserVerifier{valid: true}, issuer))
	engine.GET("/protected", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("App-Token", token)

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
}

func TestAuthRejectsUnknownUser(t *testing.T) {
	issuer := newTestIssuer(t)

	token, err := issuer.GenerateToken(42, legacyclaims.WithRole("user"))
	if err != nil {
		t.Fatalf("GenerateToken returned error: %v", err)
	}

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(Auth(fakeUserVerifier{valid: false}, issuer))
	engine.GET("/protected", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("App-Token", token)

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code == http.StatusNoContent {
		t.Fatal("expected request to be rejected before reaching handler")
	}
}

func TestAuthReturns503WhenVerifierErrors(t *testing.T) {
	issuer := newTestIssuer(t)

	token, err := issuer.GenerateToken(42, legacyclaims.WithRole("user"))
	if err != nil {
		t.Fatalf("GenerateToken returned error: %v", err)
	}

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	// verifier 因基础设施故障返回 error：必须是 503，不能是 401。
	engine.Use(Auth(fakeUserVerifier{err: errors.New("db down")}, issuer))
	engine.GET("/protected", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("App-Token", token)

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d (infra error must not be reported as auth failure)", recorder.Code, http.StatusServiceUnavailable)
	}
}

// TestAuthRejectsQueryTokenForSSERequest 守卫「query token 仅限 WebSocket 升级」：
// 声明 Accept: text/event-stream 的普通请求不得凭 query token 通过鉴权。
func TestAuthRejectsQueryTokenForSSERequest(t *testing.T) {
	issuer := newTestIssuer(t)

	token, err := issuer.GenerateToken(42, legacyclaims.WithRole("user"))
	if err != nil {
		t.Fatalf("GenerateToken returned error: %v", err)
	}

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(Auth(fakeUserVerifier{valid: true}, issuer))
	engine.GET("/protected", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/protected?App-Token="+token, nil)
	req.Header.Set("Accept", "text/event-stream")

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code == http.StatusNoContent {
		t.Fatal("expected SSE-style request with query-only token to be rejected")
	}
}

func TestAuthAllowsWebSocketQueryToken(t *testing.T) {
	issuer := newTestIssuer(t)

	token, err := issuer.GenerateToken(42, legacyclaims.WithRole("user"))
	if err != nil {
		t.Fatalf("GenerateToken returned error: %v", err)
	}

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(Auth(fakeUserVerifier{valid: true}, issuer))
	engine.GET("/ws", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/ws?App-Token="+token, nil)
	req.Header.Set("Upgrade", "websocket")

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
}

func TestAuthRejectsQueryTokenForPlainRequest(t *testing.T) {
	issuer := newTestIssuer(t)

	token, err := issuer.GenerateToken(42, legacyclaims.WithRole("user"))
	if err != nil {
		t.Fatalf("GenerateToken returned error: %v", err)
	}

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(Auth(fakeUserVerifier{valid: true}, issuer))
	engine.GET("/protected", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	// 普通 GET 即使带 App-Token query 也应被拒绝，避免 token 落进 CDN/nginx 日志。
	req := httptest.NewRequest(http.MethodGet, "/protected?App-Token="+token, nil)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code == http.StatusNoContent {
		t.Fatal("expected plain GET with query-only token to be rejected")
	}
}

func TestOptionalAuthAllowsMissingToken(t *testing.T) {
	issuer := newTestIssuer(t)

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(OptionalAuth(fakeUserVerifier{valid: true}, issuer))
	engine.GET("/public", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/public", nil)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
}

func TestOptionalAuthSetsUserIDWhenTokenIsPresent(t *testing.T) {
	issuer := newTestIssuer(t)

	token, err := issuer.GenerateToken(42, legacyclaims.WithRole("user"))
	if err != nil {
		t.Fatalf("GenerateToken returned error: %v", err)
	}

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(OptionalAuth(fakeUserVerifier{valid: true}, issuer))
	engine.GET("/public", func(c *gin.Context) {
		if got := ginc.UserIDFromContext(c); got != 42 {
			t.Fatalf("user id = %d, want 42", got)
		}
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/public", nil)
	req.Header.Set("App-Token", token)

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
}
