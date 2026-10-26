package http

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/gin-api/internal/module/auth/application"
	"github.com/gtkit/gin-api/internal/pkg/metrics"
	"github.com/gtkit/gin-api/internal/router"
	"github.com/gtkit/logger/v2"
)

func TestAuthenticationBodyBounds(t *testing.T) {
	rec, err := metrics.New(nil)
	if err != nil {
		t.Fatal(err)
	}
	log := logger.MustNew(logger.WithConsole(true), logger.WithFile(false), logger.WithReplaceGlobals(false))
	t.Cleanup(log.Sync)
	h := newTestHandler(t, stubCreds{createID: 7, found: true, cred: application.Credential{UserID: 7, PasswordHash: "hashed", Active: true}}, stubRefresh{})
	engine, err := router.New("test", router.Options{Metrics: rec, AccessLogger: log, RegisterModules: func(v1 *gin.RouterGroup) {
		Register(v1, Deps{Service: h.service, Auth: func(c *gin.Context) { c.Next() }})
	}})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"register", "login", "refresh", "logout"} {
		for _, streamed := range []bool{false, true} {
			body := strings.Repeat(" ", authBodyLimit) + `{}`
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/"+path, strings.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			if streamed {
				req.ContentLength = -1
			}
			w := httptest.NewRecorder()
			engine.ServeHTTP(w, req)
			if w.Code != 413 {
				t.Fatalf("%s streamed=%v: %d %s", path, streamed, w.Code, w.Body.String())
			}
		}
	}
	body := `{"phone":"13800138000","password":"ok"}`
	body = strings.Repeat(" ", authBodyLimit-len(body)) + body
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	engine.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("valid boundary rejected: %d %s", w.Code, w.Body.String())
	}
}
