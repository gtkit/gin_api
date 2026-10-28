package bootstrap

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	authapp "github.com/gtkit/gin-api/internal/module/auth/application"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/platform/data"
)

type admissionProbeReader struct {
	reader  io.Reader
	entered chan struct{}
	once    sync.Once
}

func (r *admissionProbeReader) Read(buffer []byte) (int, error) {
	r.once.Do(func() { close(r.entered) })
	return r.reader.Read(buffer)
}

func TestBuildEngineCredentialBudgetUsesExistingLimits(t *testing.T) {
	for _, tc := range []struct {
		name           string
		http, db, want int
	}{
		{"single HTTP slot", 1, 25, 1}, {"DB constrained", 4, 1, 1}, {"reserve HTTP slot", 4, 25, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := jwtTestConfig(t)
			cfg.Env, cfg.App.Name = "test", "credential-budget"
			cfg.App.MaxInflightRequests, cfg.Database.MaxOpenConn = tc.http, tc.db
			cfg.RateLimit = config.RateLimitConfig{KeyHMACSecret: strings.Repeat("x", 32), Policies: []config.RateLimitPolicyConfig{
				{ID: "auth.credential", Scope: "ip", Store: "local", Rate: 100, Burst: 100, MaxKeys: 100, FailureMode: "fail_closed"},
				{ID: "auth.token", Scope: "ip", Store: "local", Rate: 100, Burst: 100, MaxKeys: 100, FailureMode: "fail_closed"},
			}}
			limits, err := buildRateLimits(t.Context(), cfg, &data.Resources{})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := closeRateLimits(limits); err != nil {
					t.Error(err)
				}
			})
			inf := infra{cfg: cfg, resources: &data.Resources{}, rateLimits: limits}
			installMetricsFixture(t, &inf)
			svc := services{}
			svc.auth.Service = &authapp.Service{}
			engine, err := buildEngine(inf, svc, middlewares{auth: func(c *gin.Context) { c.AbortWithStatus(204) }}, nil)
			if err != nil {
				t.Fatal(err)
			}
			var workers sync.WaitGroup
			t.Cleanup(workers.Wait)
			for range tc.want {
				reader, writer := io.Pipe()
				probe := &admissionProbeReader{reader: reader, entered: make(chan struct{})}
				t.Cleanup(func() {
					if err := writer.Close(); err != nil {
						t.Error(err)
					}
					if err := reader.Close(); err != nil {
						t.Error(err)
					}
				})
				workers.Go(func() {
					req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", probe)
					req.Header.Set("Content-Type", "application/json")
					engine.ServeHTTP(httptest.NewRecorder(), req)
				})
				select {
				case <-probe.entered:
				case <-time.After(3 * time.Second):
					t.Fatal("credential slot did not enter body read")
				}
			}
			request := func(path string) int {
				req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{}`))
				req.Header.Set("Content-Type", "application/json")
				response := httptest.NewRecorder()
				engine.ServeHTTP(response, req)
				return response.Code
			}
			if got := request("/api/v1/auth/login"); got != 503 {
				t.Fatalf("excess credential request=%d", got)
			}
			wantToken := 204
			if tc.http == 1 {
				wantToken = 503
			}
			if got := request("/api/v1/auth/logout"); got != wantToken {
				t.Fatalf("token request=%d want=%d", got, wantToken)
			}
		})
	}
}
