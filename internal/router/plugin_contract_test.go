package router

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/gin-api/internal/core"
	"github.com/gtkit/gin-api/internal/plugin"
)

type alternateMiddleware struct {
	deps  plugin.MiddlewareDeps
	err   error
	chain []gin.HandlerFunc
}

func (p *alternateMiddleware) Build(deps plugin.MiddlewareDeps) ([]gin.HandlerFunc, error) {
	p.deps = deps
	return p.chain, p.err
}
func TestRouterUsesSelectedMiddlewareAndReturnsBuildFailures(t *testing.T) {
	root := errors.New("selected middleware failed")
	if _, err := New("test", Options{}); err == nil {
		t.Fatal("router silently selected a default")
	}
	broken := &alternateMiddleware{err: root}
	if _, err := New("test", Options{Middleware: broken}); !errors.Is(err, root) {
		t.Fatalf("lost Build root: %v", err)
	}
	selected := &alternateMiddleware{chain: []gin.HandlerFunc{func(c *gin.Context) { c.Header("Selected-Middleware", "alternative"); c.Next() }}}
	engine, err := New("test", Options{Middleware: selected, ServiceName: "selected", TracingEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if selected.deps.ServiceName != "selected" || !selected.deps.TracingEnabled {
		t.Fatal("named dependencies did not reach selected implementation")
	}
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/health", nil))
	if response.Header().Get("Selected-Middleware") != "alternative" {
		t.Fatal("selected middleware not installed")
	}
}
func TestGlobalMiddlewareInstallIsAtomicAndOnce(t *testing.T) {
	handler := func(c *gin.Context) { c.Next() }
	for _, tc := range []struct {
		name  string
		chain []gin.HandlerFunc
	}{
		{"empty", nil},
		{"over Gin capacity", make([]gin.HandlerFunc, 62)}, {"nil entry", []gin.HandlerFunc{handler, nil}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			engine := gin.New()
			if err := installGlobalMiddleware(engine, tc.chain); !errors.Is(err, core.ErrPluginContractViolation) {
				t.Fatal(err)
			}
			if len(engine.Handlers) != 0 {
				t.Fatal("invalid chain partially installed")
			}
		})
	}
	engine := gin.New()
	if err := installGlobalMiddleware(engine, []gin.HandlerFunc{handler}); err != nil {
		t.Fatal(err)
	}
	if err := installGlobalMiddleware(engine, []gin.HandlerFunc{handler}); !errors.Is(err, core.ErrPluginContractViolation) {
		t.Fatal(err)
	}
	if len(engine.Handlers) != 1 {
		t.Fatal("duplicate chain installed")
	}
}
