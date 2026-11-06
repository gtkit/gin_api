package router

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	authhttp "github.com/gtkit/gin-api/internal/module/auth/transport/http"
)

type budgetRecorder struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
}

func (w *budgetRecorder) SetWriteDeadline(deadline time.Time) error {
	w.deadlines = append(w.deadlines, deadline)
	return nil
}

func TestDefaultAPITimeoutBudgets(t *testing.T) {
	var observed time.Time
	capture := func(c *gin.Context) {
		var ok bool
		observed, ok = c.Request.Context().Deadline()
		if !ok {
			t.Error("API request has no deadline")
		}
		c.AbortWithStatus(http.StatusNoContent)
	}
	engine := newTestEngineWith(t, Options{
		HealthChecker: &stubChecker{},
		RegisterModules: func(v1 *gin.RouterGroup) {
			v1.GET("/budget", capture)
			authhttp.Register(v1, authhttp.Deps{
				Auth:      capture,
				RateLimit: func(string, ...string) []gin.HandlerFunc { return []gin.HandlerFunc{capture} },
			})
		},
	})
	for _, tc := range []struct {
		name   string
		method string
		path   string
		budget time.Duration
		parent bool
	}{
		{name: "ordinary API", method: http.MethodGet, path: "/api/v1/budget", budget: 30 * time.Second},
		{name: "auth retains shorter budget", method: http.MethodPost, path: "/api/v1/auth/login", budget: 10 * time.Second},
		{name: "parent stays earlier", method: http.MethodGet, path: "/api/v1/budget", budget: time.Second, parent: true},
		{name: "health outside API", method: http.MethodGet, path: "/health"},
		{name: "ready outside API", method: http.MethodGet, path: "/ready"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := &budgetRecorder{ResponseRecorder: httptest.NewRecorder()}
			req := httptest.NewRequest(tc.method, tc.path, nil)
			start := time.Now()
			if tc.parent {
				ctx, cancel := context.WithTimeout(req.Context(), tc.budget)
				defer cancel()
				req = req.WithContext(ctx)
			}
			engine.ServeHTTP(w, req)
			if tc.budget == 0 {
				if len(w.deadlines) != 0 || w.Code != http.StatusOK {
					t.Fatalf("system probe inherited API deadline: %v, status=%d", w.deadlines, w.Code)
				}
				return
			}
			if remaining := observed.Sub(start); remaining < tc.budget-time.Second || remaining > tc.budget+time.Second {
				t.Fatalf("business budget = %s, want %s", remaining, tc.budget)
			}
			if len(w.deadlines) == 0 || w.deadlines[len(w.deadlines)-1].Sub(observed) != 2*time.Second {
				t.Fatalf("write grace did not follow actual business deadline: %v, business=%v", w.deadlines, observed)
			}
		})
	}
}
