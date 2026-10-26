package bootstrap

import (
	"encoding/json"
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/gtkit/gin-api/internal/pkg/jwtauth"
	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/platform/data"
	redisowner "github.com/gtkit/gin-api/internal/platform/redis"
	"gorm.io/gorm"
)

func TestOpenAPIMatchesProductionRoutes(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "docs", "openapi.json"))
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		OpenAPI string `json:"openapi"`
		Paths   map[string]map[string]struct {
			Mode string `json:"x-auth-mode"`
		} `json:"paths"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	if spec.OpenAPI != "3.1.0" {
		t.Fatal("unexpected OpenAPI dialect")
	}
	for _, mode := range []config.AuthMode{config.AuthModeLocal, config.AuthModeExternal} {
		t.Run(string(mode), func(t *testing.T) {
			engine := openAPIEngine(t, mode)
			actual := make(map[string]bool)
			for _, route := range engine.Routes() {
				actual[route.Method+" "+openAPIPath(route.Path)] = true
			}
			expected := make(map[string]bool)
			for path, methods := range spec.Paths {
				for method, operation := range methods {
					if operation.Mode != "" && operation.Mode != string(mode) {
						continue
					}
					expected[strings.ToUpper(method)+" "+path] = true
				}
			}
			if !maps.Equal(actual, expected) {
				t.Fatalf("production routes=%v; OpenAPI=%v", actual, expected)
			}
		})
	}
}

// Construct services through the actual runtime assembly as well as buildEngine;
// the route set is not duplicated in this test.
func openAPIEngine(t *testing.T, mode config.AuthMode) *gin.Engine {
	t.Helper()
	cfg := jwtTestConfig(t)
	cfg.Env = "test"
	cfg.App.Name = "openapi-contract"
	cfg.Auth = config.AuthConfig{Mode: mode, BcryptCost: 10, RefreshReplaySecret: strings.Repeat("r", 32)}
	resources := &data.Resources{}
	if mode == config.AuthModeLocal {
		server := miniredis.RunT(t)
		cfg.Redis = config.RedisConfig{Addr: server.Addr(), DBs: []int{0}}
		client, err := redisowner.OpenRedis(t.Context(), cfg.Redis)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := client.Close(); err != nil {
				t.Error(err)
			}
		})
		resources, err = data.ResourcesFromClients(&lifecycleComponent{db: &gorm.DB{}}, client, true)
		if err != nil {
			t.Fatal(err)
		}
	}
	cfg.RateLimit = config.RateLimitConfig{KeyHMACSecret: strings.Repeat("k", 32), Policies: []config.RateLimitPolicyConfig{
		{ID: "auth.credential", Scope: "ip", Store: "local", Rate: 10, Burst: 10, MaxKeys: 100, FailureMode: "fail_open"},
		{ID: "auth.token", Scope: "ip", Store: "local", Rate: 10, Burst: 10, MaxKeys: 100, FailureMode: "fail_open"},
	}}
	limits, err := buildRateLimits(t.Context(), cfg, resources)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := closeRateLimits(limits); err != nil {
			t.Error(err)
		}
	})
	jwt, err := jwtauth.Assemble(cfg)
	if err != nil {
		t.Fatal(err)
	}
	inf := infra{cfg: cfg, resources: resources, mysqlDB: &gorm.DB{}, rateLimits: limits}
	installMetricsFixture(t, &inf)
	runtime := &Runtime{config: cfg, resources: resources, jwt: jwt}
	svc, err := buildServices(t.Context(), inf, runtime)
	if err != nil {
		t.Fatal(err)
	}
	mw, err := buildMiddlewares(svc, runtime)
	if err != nil {
		t.Fatal(err)
	}
	engine, err := buildEngine(inf, svc, mw, nil)
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func openAPIPath(path string) string {
	parts := strings.Split(path, "/")
	for i, part := range parts {
		if strings.HasPrefix(part, ":") || strings.HasPrefix(part, "*") {
			parts[i] = "{" + part[1:] + "}"
		}
	}
	return strings.Join(parts, "/")
}
