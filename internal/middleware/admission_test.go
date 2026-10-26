package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestAdmissionReleasesAfterPanic(t *testing.T) {
	engine := gin.New()
	engine.Use(func(c *gin.Context) {
		defer func() {
			if recover() != nil {
				c.AbortWithStatus(500)
			}
		}()
		c.Next()
	})
	group := engine.Group("/api", Admission(1, nil))
	group.GET("/panic", func(*gin.Context) { panic("test") })
	group.GET("/ok", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	for _, tc := range []struct {
		path   string
		status int
	}{{"/api/panic", 500}, {"/api/ok", 204}} {
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if w.Code != tc.status {
			t.Fatalf("%s: %d", tc.path, w.Code)
		}
	}
}
