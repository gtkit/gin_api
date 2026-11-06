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
	"github.com/gtkit/gin-api/internal/pkg/jwtauth"
)

type sessionVerifier struct {
	version int64
	err     error
	calls   int
}

func (v *sessionVerifier) ValidateSession(_ context.Context, uid, version int64) (bool, error) {
	v.calls++
	return uid == 42 && version == v.version, v.err
}

func TestAuthRechecksSessionWithCachedJWT(t *testing.T) {
	cfg := testJWTConfig(t)
	cfg.JWT.CacheMaxEntries = 8
	jwt, err := jwtauth.Assemble(cfg)
	if err != nil {
		t.Fatal(err)
	}
	token, err := jwt.Issuer.IssueForSession(t.Context(), 42, 0)
	if err != nil {
		t.Fatal(err)
	}
	sessions := &sessionVerifier{}
	engine := gin.New()
	// The ordinary verifier denies everything: session verification must replace it.
	engine.Use(Auth(AuthOptions{Verifier: fakeUserVerifier{}, Parser: jwt.Parser, Sessions: sessions}))
	engine.GET("/protected", func(c *gin.Context) {
		identity, ok := ginc.TokenIdentityFromContext(c)
		if !ok || identity.SessionVersion != sessions.version {
			t.Error("verified session identity was not propagated")
		}
		c.Status(http.StatusNoContent)
	})
	request := func(want int) {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/protected", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		engine.ServeHTTP(rec, req)
		if rec.Code != want || strings.Contains(rec.Body.String(), "database detail") {
			t.Fatalf("status=%d body=%s", rec.Code, rec.Body)
		}
	}
	request(http.StatusNoContent)
	sessions.version++
	request(http.StatusUnauthorized)
	sessions.err = errors.New("database detail")
	request(http.StatusServiceUnavailable)
	if sessions.calls != 3 {
		t.Fatalf("session lookups=%d, want one per request", sessions.calls)
	}
}
