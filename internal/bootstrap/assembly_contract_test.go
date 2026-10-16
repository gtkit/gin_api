package bootstrap

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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
	cfg.Modules.AI = config.AIModuleConfig{
		DefaultProvider: "deepseek",
		Providers:       map[string]config.AIProviderConfig{"deepseek": {APIKey: "test"}},
	}
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
	if svc.ai == nil || svc.auth.Service != nil || svc.accounts == nil || RedisRequired(cfg) {
		t.Fatalf("unexpected assembly: ai=%v auth=%v accounts=%v", svc.ai, svc.auth.Service, svc.accounts)
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
		path string
		want int
	}{
		{"/api/v1/ai/providers", http.StatusUnauthorized},
		{"/api/v1/auth/login", http.StatusNotFound},
	} {
		w := httptest.NewRecorder()
		engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, tc.path, nil))
		if w.Code != tc.want {
			t.Fatalf("%s = %d, want %d", tc.path, w.Code, tc.want)
		}
	}
	// 同一个模块的缺失限流引用必须仍使装配失败，不能因 Redis 可选而放宽。
	inf.rateLimits = rateLimitAssembly{}
	if _, err := buildEngine(inf, svc, mw, nil); err == nil {
		t.Fatal("missing route policies accepted")
	}
}

func TestServiceAssemblyErrorPaths(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name     string
		provider string
		local    bool
	}{
		{"later auth failure", "deepseek", true},
		{"invalid ai provider", "unknown", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{Auth: config.AuthConfig{Mode: config.AuthModeExternal}}
			cfg.Modules.AI = config.AIModuleConfig{DefaultProvider: tc.provider, Providers: map[string]config.AIProviderConfig{tc.provider: {APIKey: "test"}}}
			if tc.local {
				cfg.Auth.Mode = config.AuthModeLocal
			}
			inf := infra{cfg: cfg, resources: &data.Resources{}, mysqlDB: &gorm.DB{}}
			if _, err := buildServices(t.Context(), inf, &Runtime{config: cfg}); err == nil {
				t.Fatal("broken assembly accepted")
			}
		})
	}
}

func TestUnconfiguredHealthCheckerDoesNotConnect(t *testing.T) {
	t.Parallel()
	for _, resources := range []*data.Resources{nil, {}} {
		checker := newHealthChecker(resources)
		if checker.(*runtimeHealthChecker).RedisConfigured() {
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
