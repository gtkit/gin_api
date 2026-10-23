package bootstrap

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	authapp "github.com/gtkit/gin-api/internal/module/auth/application"
	"github.com/gtkit/gin-api/internal/pkg/metrics"
	"github.com/gtkit/logger/v2"
	"go.opentelemetry.io/otel"

	"gorm.io/gorm"

	"github.com/gtkit/gin-api/internal/pkg/httpcache"
	"github.com/gtkit/gin-api/internal/pkg/jwtauth"
	"github.com/gtkit/gin-api/internal/platform/config"
	data "github.com/gtkit/gin-api/internal/platform/data"
)

func TestAssembleExternalIdentityWithoutRedis(t *testing.T) {
	cfg := jwtTestConfig(t)
	cfg.Env = "test"
	cfg.App.Name = "assembly-contract"
	cfg.Auth.Mode = config.AuthModeExternal
	cfg.RateLimit = config.RateLimitConfig{KeyHMACSecret: strings.Repeat("x", 32), Policies: []config.RateLimitPolicyConfig{
		{ID: "default.ip", Scope: "ip", Store: "local", Rate: 10, Burst: 10, MaxKeys: 100, FailureMode: "fail_open"},
		{ID: "default.user", Scope: "user", Store: "local", Rate: 10, Burst: 10, MaxKeys: 100, FailureMode: "fail_open"},
	}}
	jwt, err := jwtauth.Assemble(cfg)
	if err != nil {
		t.Fatal(err)
	}
	resources := &data.Resources{}
	limits, err := buildRateLimits(t.Context(), cfg, resources)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := closeRateLimits(limits); err != nil {
			t.Error(err)
		}
	})
	cache, err := httpcache.New(config.CacheConfig{}, nil, cfg.App.Name)
	if err != nil {
		t.Fatal(err)
	}
	inf := infra{cfg: cfg, resources: resources, mysqlDB: &gorm.DB{}, rateLimits: limits, cache: cache}
	installMetricsFixture(t, &inf)
	runtime := &Runtime{config: cfg, resources: resources, jwt: jwt}
	svc, err := buildServices(t.Context(), inf, runtime)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := svc.cleanup(); err != nil {
			t.Error(err)
		}
	})
	if svc.auth.Service != nil || svc.accounts == nil || RedisRequired(cfg) {
		t.Fatalf("unexpected assembly: auth=%v accounts=%v", svc.auth.Service, svc.accounts)
	}
	mw, err := buildMiddlewares(svc, runtime)
	if err != nil {
		t.Fatal(err)
	}
	workers, err := buildWorkers(&inf)
	if err != nil {
		t.Fatal(err)
	}
	if err := workers.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	engine, err := buildEngine(inf, svc, mw, newProcessStatus(NewShutdownCoordinator(), workers))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		method string
		path   string
		want   int
	}{
		{http.MethodPost, "/api/v1/ai/chat", http.StatusNotFound},
		{http.MethodPost, "/api/v1/ai/chat/stream", http.StatusNotFound},
		{http.MethodGet, "/api/v1/ai/providers", http.StatusNotFound},
		{http.MethodGet, "/api/v1/ai/conversations", http.StatusNotFound},
		{http.MethodGet, "/api/v1/ai/conversations/1", http.StatusNotFound},
		{http.MethodPatch, "/api/v1/ai/conversations/1", http.StatusNotFound},
		{http.MethodDelete, "/api/v1/ai/conversations/1", http.StatusNotFound},
		{http.MethodPost, "/api/v1/auth/login", http.StatusNotFound},
		{http.MethodGet, "/health", http.StatusOK},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			w := httptest.NewRecorder()
			engine.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
			if w.Code != tc.want {
				t.Fatalf("%s %s = %d, want %d", tc.method, tc.path, w.Code, tc.want)
			}
		})
	}
	// 本地认证的缺失限流引用仍须使装配失败，不能因 Redis 可选而放宽。
	svc.auth.Service = &authapp.Service{}
	inf.rateLimits = rateLimitAssembly{}
	installMetricsFixture(t, &inf)
	if _, err := buildEngine(inf, svc, mw, nil); err == nil || !strings.Contains(err.Error(), "rate limit policy") {
		t.Fatal("missing route policies accepted")
	}
}

func TestServiceAssemblyRejectsBrokenLocalAuth(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{Auth: config.AuthConfig{Mode: config.AuthModeLocal}}
	inf := infra{cfg: cfg, resources: &data.Resources{}, mysqlDB: &gorm.DB{}}
	if _, err := buildServices(t.Context(), inf, &Runtime{config: cfg}); err == nil {
		t.Fatal("broken assembly accepted")
	}
}

func TestUnconfiguredHealthCheckerDoesNotConnect(t *testing.T) {
	t.Parallel()
	for _, resources := range []*data.Resources{nil, {}} {
		checker := newHealthChecker(resources)
		runtimeChecker, ok := checker.(*runtimeHealthChecker)
		if !ok {
			t.Fatalf("health checker type %T", checker)
		}
		if runtimeChecker.RedisConfigured() {
			t.Fatal("zero resources reported Redis configured")
		}
		if err := checker.PingDB(t.Context()); err == nil {
			t.Fatal("missing database reported healthy")
		}
		if err := checker.PingRedis(t.Context()); err == nil {
			t.Fatal("missing Redis reported healthy")
		}
	}
}

func installMetricsFixture(t *testing.T, inf *infra) {
	t.Helper()
	inf.accessLogger = logger.MustNew(logger.WithConsole(true), logger.WithFile(false), logger.WithReplaceGlobals(false))
	t.Cleanup(inf.accessLogger.Sync)
	recorder, err := metrics.New(otel.Meter("assembly-contract"))
	if err != nil {
		t.Fatal(err)
	}
	inf.recorder = recorder
}
