package bootstrap

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/gin-api/internal/middleware"
	"github.com/gtkit/gin-api/internal/pkg/ginc"
	sharedauth "github.com/gtkit/gin-api/internal/shared/auth"
)

type contractRoles struct {
	allowed bool
	err     error
}

func (r *contractRoles) HasRole(context.Context, int64, string) (bool, error) {
	return r.allowed, r.err
}

func TestAuthorizationUsesCurrentAuthority(t *testing.T) {
	roles := &contractRoles{}
	engine := gin.New()
	engine.Use(middleware.ErrorHandler())
	called := 0
	engine.GET("/records/:id", func(c *gin.Context) { ginc.SetPrincipal(c, sharedauth.Principal{UserID: 7, Roles: []string{"admin"}}) }, middleware.Authorize(newRoleAuthorizer(roles, "admin"), "records.read"), func(c *gin.Context) { called++; c.Status(204) })
	for _, tc := range []struct {
		name    string
		allowed bool
		err     error
		status  int
	}{
		{"token role cannot grant permission", false, nil, 403},
		{"current authority grants", true, nil, 204},
		{"revoked role takes effect", false, nil, 403},
		{"authority unavailable", false, errors.New("private authority detail"), 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			roles.allowed, roles.err = tc.allowed, tc.err
			response := httptest.NewRecorder()
			engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/records/1", nil))
			if response.Code != tc.status || strings.Contains(response.Body.String(), "private authority detail") {
				t.Fatalf("status=%d body=%s", response.Code, response.Body)
			}
		})
	}
	if called != 1 {
		t.Fatalf("denied requests reached handler: %d calls", called)
	}
}
