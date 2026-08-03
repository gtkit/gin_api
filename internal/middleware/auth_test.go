package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

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
	jwt := newTestJWT(t)
	issuer := jwt.Issuer

	token, err := issuer.Issue(context.Background(), 42, "user")
	if err != nil {
		t.Fatalf("Issue returned error: %v", err)
	}

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(mustAuthenticate(t, fakeUserVerifier{valid: true}, jwt.Parser, false))
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
	jwt := newTestJWT(t)
	issuer := jwt.Issuer

	token, err := issuer.Issue(context.Background(), 42, "user")
	if err != nil {
		t.Fatalf("Issue returned error: %v", err)
	}

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(mustAuthenticate(t, fakeUserVerifier{valid: false}, jwt.Parser, false))
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
	jwt := newTestJWT(t)
	issuer := jwt.Issuer

	token, err := issuer.Issue(context.Background(), 42, "user")
	if err != nil {
		t.Fatalf("Issue returned error: %v", err)
	}

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	// verifier 因基础设施故障返回 error：必须是 503，不能是 401。
	engine.Use(mustAuthenticate(t, fakeUserVerifier{err: errors.New("db down")}, jwt.Parser, false))
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
	jwt := newTestJWT(t)
	issuer := jwt.Issuer

	token, err := issuer.Issue(context.Background(), 42, "user")
	if err != nil {
		t.Fatalf("Issue returned error: %v", err)
	}

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(mustAuthenticate(t, fakeUserVerifier{valid: true}, jwt.Parser, false))
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

// TestAuthRejectsWebSocketQueryToken 锁住被删掉的 query-token 旁路。
//
// 此前 WebSocket 升级请求可以从 query 读 token。那是一个**零消费者的安全敞口**：
// 仓库里没有任何 WebSocket 端点，而 query 会进访问日志、Referer 与浏览器历史。
// 将来引入 WS 时须单独设计（如一次性票据换取连接），不复活 query token。
func TestAuthRejectsWebSocketQueryToken(t *testing.T) {
	jwt := newTestJWT(t)
	issuer := jwt.Issuer

	token, err := issuer.Issue(context.Background(), 42, "user")
	if err != nil {
		t.Fatalf("Issue returned error: %v", err)
	}

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(mustAuthenticate(t, fakeUserVerifier{valid: true}, jwt.Parser, false))
	engine.GET("/ws", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/ws?App-Token="+token, nil)
	req.Header.Set("Upgrade", "websocket")

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d (query token must not be accepted, not even for upgrades)",
			recorder.Code, http.StatusUnauthorized)
	}
}

func TestAuthRejectsQueryTokenForPlainRequest(t *testing.T) {
	jwt := newTestJWT(t)
	issuer := jwt.Issuer

	token, err := issuer.Issue(context.Background(), 42, "user")
	if err != nil {
		t.Fatalf("Issue returned error: %v", err)
	}

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(mustAuthenticate(t, fakeUserVerifier{valid: true}, jwt.Parser, false))
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
	jwt := newTestJWT(t)

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(mustAuthenticate(t, fakeUserVerifier{valid: true}, jwt.Parser, true))
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
	jwt := newTestJWT(t)
	issuer := jwt.Issuer

	token, err := issuer.Issue(context.Background(), 42, "user")
	if err != nil {
		t.Fatalf("Issue returned error: %v", err)
	}

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(mustAuthenticate(t, fakeUserVerifier{valid: true}, jwt.Parser, true))
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
