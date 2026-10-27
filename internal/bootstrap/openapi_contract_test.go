package bootstrap

import (
	"encoding/json"
	"fmt"
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
	for _, mode := range []config.AuthMode{config.AuthModeLocal, config.AuthModeExternal} {
		t.Run(string(mode), func(t *testing.T) {
			engine := openAPIEngine(t, mode)
			actual := make(map[string]bool)
			for _, route := range engine.Routes() {
				actual[route.Method+" "+openAPIPath(route.Path)] = true
			}
			expected, err := openAPIExpectedRoutes(raw, mode)
			if err != nil {
				t.Fatal(err)
			}
			if !maps.Equal(actual, expected) {
				t.Fatalf("production routes=%v; OpenAPI=%v", actual, expected)
			}
		})
	}
}

func openAPIExpectedRoutes(raw []byte, mode config.AuthMode) (map[string]bool, error) {
	var spec struct {
		OpenAPI string                     `json:"openapi"`
		Paths   map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(raw, &spec); err != nil {
		return nil, err
	}
	if spec.OpenAPI != "3.1.0" {
		return nil, fmt.Errorf("unexpected OpenAPI dialect %q", spec.OpenAPI)
	}
	expected := make(map[string]bool)
	for path, rawPath := range spec.Paths {
		if strings.HasPrefix(path, "x-") {
			continue
		}
		var methods map[string]json.RawMessage
		if err := json.Unmarshal(rawPath, &methods); err != nil {
			return nil, fmt.Errorf("decode path %q: %w", path, err)
		}
		if methods == nil {
			return nil, fmt.Errorf("path %q must be an object, not null", path)
		}
		if _, exists := methods["$ref"]; exists {
			return nil, fmt.Errorf("path %q: path-level $ref is not supported", path)
		}
		for method, rawOperation := range methods {
			switch method {
			case "get", "put", "post", "delete", "options", "head", "patch", "trace":
			default:
				continue
			}
			var operation *struct {
				Mode string `json:"x-auth-mode"`
			}
			if err := json.Unmarshal(rawOperation, &operation); err != nil {
				return nil, fmt.Errorf("decode %s %s: %w", method, path, err)
			}
			if operation == nil {
				return nil, fmt.Errorf("operation %s %s must be an object", method, path)
			}
			if operation.Mode != "" && operation.Mode != string(mode) {
				continue
			}
			expected[strings.ToUpper(method)+" "+path] = true
		}
	}
	return expected, nil
}

func TestOpenAPIPathMetadataAndOperations(t *testing.T) {
	for _, mode := range []config.AuthMode{config.AuthModeLocal, config.AuthModeExternal} {
		t.Run(string(mode), func(t *testing.T) {
			raw := []byte(`{"openapi":"3.1.0","paths":{"/items":{
				"summary":"Items", "description":"Path-wide description",
				"parameters":[{"name":"id","in":"query"}], "servers":[{"url":"/"}],
				"x-metadata":{"note":"not an operation"},
				"get":{}, "put":{}, "post":{}, "delete":{}, "options":{}, "head":{}, "patch":{}, "trace":{}
			},"/local":{"post":{"x-auth-mode":"local"}},"/external":{"post":{"x-auth-mode":"external"}}}}`)
			got, err := openAPIExpectedRoutes(raw, mode)
			if err != nil {
				t.Fatal(err)
			}
			want := map[string]bool{"POST /" + string(mode): true}
			for _, method := range []string{"GET", "PUT", "POST", "DELETE", "OPTIONS", "HEAD", "PATCH", "TRACE"} {
				want[method+" /items"] = true
			}
			if !maps.Equal(got, want) {
				t.Fatalf("operations=%v, want %v", got, want)
			}
		})
	}
}

func TestOpenAPIRejectsInvalidOperations(t *testing.T) {
	for _, raw := range []string{
		`{`, `{"openapi":"3.0.0","paths":{}}`,
		`{"openapi":"3.1.0","paths":{"/items":{"get":"invalid"}}}`,
		`{"openapi":"3.1.0","paths":{"/items":{"get":[]}}}`,
		`{"openapi":"3.1.0","paths":{"/items":{"get":null}}}`,
	} {
		if _, err := openAPIExpectedRoutes([]byte(raw), config.AuthModeLocal); err == nil {
			t.Errorf("accepted invalid contract %s", raw)
		}
	}
}

func TestOpenAPIRejectsUnsupportedPathValues(t *testing.T) {
	for _, path := range []string{"/items", "/x-extra"} {
		for _, value := range []string{`null`, `true`, `42`, `[]`, `"invalid"`, `{"$ref":"#/components/pathItems/Items"}`, `{"$ref":""}`, `{"$ref":null}`} {
			raw := fmt.Sprintf(`{"openapi":"3.1.0","paths":{%q:%s}}`, path, value)
			if _, err := openAPIExpectedRoutes([]byte(raw), config.AuthModeLocal); err == nil {
				t.Errorf("silently accepted unsupported path %s value %s", path, value)
			}
		}
	}
}

func TestOpenAPIPathsExtensions(t *testing.T) {
	for _, tc := range []struct{ name, value string }{
		{"null", `null`},
		{"string", `"metadata"`},
		{"number", `42`},
		{"boolean", `true`},
		{"array", `[null,42,{"get":null}]`},
		{"empty object", `{}`},
		{"reference object", `{"$ref":null}`},
		{"operation-like object", `{"get":null,"post":{"x-auth-mode":"local"}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, mode := range []config.AuthMode{config.AuthModeLocal, config.AuthModeExternal} {
				t.Run(string(mode), func(t *testing.T) {
					raw := fmt.Sprintf(`{"openapi":"3.1.0","paths":{"x-extra":%s,"/x-extra":{"get":{}},"/local":{"post":{"x-auth-mode":"local"}},"/external":{"post":{"x-auth-mode":"external"}}}}`, tc.value)
					got, err := openAPIExpectedRoutes([]byte(raw), mode)
					if err != nil {
						t.Fatal(err)
					}
					want := map[string]bool{"GET /x-extra": true, "POST /" + string(mode): true}
					if !maps.Equal(got, want) {
						t.Fatalf("extension changed routes: got %v, want %v", got, want)
					}
				})
			}
		})
	}
}

func TestOpenAPIAllowsEmptyPathsAndSchemaReferences(t *testing.T) {
	raw := []byte(`{"openapi":"3.1.0","paths":{"/empty":{},"/items":{"get":{"responses":{"200":{"description":"ok","content":{"application/json":{"schema":{"$ref":"#/components/schemas/Item"}}}}}}}},"components":{"schemas":{"Item":{"type":"object"}}}}`)
	got, err := openAPIExpectedRoutes(raw, config.AuthModeLocal)
	if err != nil {
		t.Fatal(err)
	}
	if !maps.Equal(got, map[string]bool{"GET /items": true}) {
		t.Fatalf("wrong inline routes: %v", got)
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
