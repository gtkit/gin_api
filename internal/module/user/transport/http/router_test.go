package http

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestUserModuleRoutes(t *testing.T) {
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	module := NewModule(nil, func(c *gin.Context) { c.Next() })
	module.RegisterRoutes(engine.Group("/api/v1"))

	routes := engine.Routes()
	tests := []struct {
		name      string
		method    string
		path      string
		wantFound bool
	}{
		{name: "get current profile", method: http.MethodGet, path: "/api/v1/user", wantFound: true},
		{name: "update current profile", method: http.MethodPut, path: "/api/v1/user", wantFound: true},
		{name: "legacy unused id route removed", method: http.MethodPut, path: "/api/v1/user/:id", wantFound: false},
		{name: "deactivate current profile", method: http.MethodPost, path: "/api/v1/user/deactivate", wantFound: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			found := false
			for _, route := range routes {
				if route.Method == tt.method && route.Path == tt.path {
					found = true
					break
				}
			}
			if found != tt.wantFound {
				t.Fatalf("route %s %s found=%t, want %t", tt.method, tt.path, found, tt.wantFound)
			}
		})
	}
}
