package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	jwtv5 "github.com/golang-jwt/jwt/v5"

	"github.com/gtkit/gin-api/internal/pkg/ginc"
	"github.com/gtkit/gin-api/internal/pkg/jwtauth"
)

// 32 字节：满足 hs256 的密钥下限。
const laravelTestSecret = "laravel-test-secret-0123456789ab"

type exactLaravelUser struct{ seen []int64 }

func (v *exactLaravelUser) ValidateUser(_ context.Context, id int64) (bool, error) {
	v.seen = append(v.seen, id)
	return true, nil
}

func TestLaravelLargeIntegerReachesUserVerifierExactly(t *testing.T) {
	verifier, err := jwtauth.NewLaravelVerifier(laravelTestSecret, "hs256", "user", 0)
	if err != nil {
		t.Fatal(err)
	}
	parser, err := jwtauth.NewFallbackParser(newTestParser(t), verifier, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	users := &exactLaravelUser{}
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(mustAuthenticate(t, users, parser, false))
	const want = int64(9007199254740993)
	engine.GET("/protected", func(c *gin.Context) {
		if got := ginc.UserIDFromContext(c); got != want {
			t.Errorf("request identity=%d want=%d", got, want)
		}
		c.Status(http.StatusNoContent)
	})
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/protected", nil)
	req.Header.Set("App-Token", signLaravelTestToken(t, want))
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, req)
	if response.Code != http.StatusNoContent || len(users.seen) != 1 || users.seen[0] != want {
		t.Fatalf("status=%d verifier IDs=%v", response.Code, users.seen)
	}
}

func signLaravelTestToken(t *testing.T, userID int64) string {
	t.Helper()

	token, err := jwtv5.NewWithClaims(jwtv5.SigningMethodHS256, jwtv5.MapClaims{
		"sub": userID,
		"exp": time.Now().Add(time.Hour).Unix(),
		"iat": time.Now().Unix(),
	}).SignedString([]byte(laravelTestSecret))
	if err != nil {
		t.Fatalf("sign laravel test token: %v", err)
	}

	return token
}

// TestAuthAcceptsLaravelToken 走完整中间件链路：
// Ed25519 主验证失败 → 回退 Laravel HS256 验证 → 角色校验 → 用户校验 → SetUserID。
func TestAuthAcceptsLaravelToken(t *testing.T) {
	primary := newTestParser(t)

	laravelVerifier, err := jwtauth.NewLaravelVerifier(laravelTestSecret, "hs256", "user", 0)
	if err != nil {
		t.Fatalf("NewLaravelVerifier returned error: %v", err)
	}
	parser, err := jwtauth.NewFallbackParser(primary, laravelVerifier, time.Now().Add(time.Hour))
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

// TestAuthRejectsLaravelTokenWhenFallbackDisabled 确认未启用 laravel 回退时，
// 同一个 Laravel token 仍被 401 拒绝（组合 parser 不存在 laravel 分支）。
func TestAuthRejectsLaravelTokenWhenFallbackDisabled(t *testing.T) {
	primary := newTestParser(t)

	parser, err := jwtauth.NewFallbackParser(primary, nil, time.Time{})
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
