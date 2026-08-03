package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	jwtv5 "github.com/golang-jwt/jwt/v5"

	"github.com/gtkit/gin-api/internal/pkg/ginc"
	"github.com/gtkit/gin-api/internal/pkg/jwtauth"
)

const legacyLaravelTestSecret = "legacy-laravel-test-secret"

func signLaravelTestToken(t *testing.T, userID int64) string {
	t.Helper()

	token, err := jwtv5.NewWithClaims(jwtv5.SigningMethodHS256, jwtv5.MapClaims{
		"sub": userID,
		"exp": time.Now().Add(time.Hour).Unix(),
		"iat": time.Now().Unix(),
	}).SignedString([]byte(legacyLaravelTestSecret))
	if err != nil {
		t.Fatalf("sign laravel test token: %v", err)
	}

	return token
}

// TestAuthAcceptsLegacyLaravelToken 走完整中间件链路：
// Ed25519 主验证失败 → 回退 Laravel HS256 验证 → 角色校验 → 用户校验 → SetUserID。
func TestAuthAcceptsLegacyLaravelToken(t *testing.T) {
	issuer := newTestIssuer(t)

	laravelVerifier, err := jwtauth.NewLaravelVerifier(legacyLaravelTestSecret, "hs256", "user")
	if err != nil {
		t.Fatalf("NewLaravelVerifier returned error: %v", err)
	}
	parser, err := jwtauth.NewFallbackParser(issuer, laravelVerifier)
	if err != nil {
		t.Fatalf("NewFallbackParser returned error: %v", err)
	}

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(mustAuthenticate(t, fakeUserVerifier{valid: true}, parser, false))
	engine.GET("/protected", func(c *gin.Context) {
		if got := ginc.UserIDFromContext(c); got != 42 {
			t.Fatalf("user id = %d, want 42", got)
		}
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("App-Token", signLaravelTestToken(t, 42))

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
}

// TestAuthRejectsLegacyLaravelTokenWhenFallbackDisabled 确认未启用 legacy 回退时，
// 同一个 Laravel token 仍被 401 拒绝（组合 parser 不存在 legacy 分支）。
func TestAuthRejectsLegacyLaravelTokenWhenFallbackDisabled(t *testing.T) {
	issuer := newTestIssuer(t)

	parser, err := jwtauth.NewFallbackParser(issuer, nil)
	if err != nil {
		t.Fatalf("NewFallbackParser returned error: %v", err)
	}

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(mustAuthenticate(t, fakeUserVerifier{valid: true}, parser, false))
	engine.GET("/protected", func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodGet, "/protected", nil)
	req.Header.Set("App-Token", signLaravelTestToken(t, 42))

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnauthorized)
	}
}
