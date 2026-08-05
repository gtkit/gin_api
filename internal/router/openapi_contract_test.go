package router_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/gtkit/gin-api/pkg/ratelimit"

	"github.com/gtkit/gin-api/internal/models"
	authhttp "github.com/gtkit/gin-api/internal/module/auth/transport/http"
	userapp "github.com/gtkit/gin-api/internal/module/user/application"
	userhttp "github.com/gtkit/gin-api/internal/module/user/transport/http"
	"github.com/gtkit/gin-api/internal/router"

	"github.com/gin-gonic/gin"
)

var openAPIMethods = map[string]struct{}{
	"delete":  {},
	"get":     {},
	"head":    {},
	"options": {},
	"patch":   {},
	"post":    {},
	"put":     {},
	"trace":   {},
}

type openAPIDocument struct {
	OpenAPI string                                `json:"openapi"`
	Info    openAPIInfo                           `json:"info"`
	Paths   map[string]map[string]json.RawMessage `json:"paths"`
}

type openAPIInfo struct {
	Title   string `json:"title"`
	Version string `json:"version"`
}

type openAPIOperation struct {
	OperationID string                     `json:"operationId"`
	Responses   map[string]json.RawMessage `json:"responses"`
}

func TestOpenAPIContract(t *testing.T) {
	t.Parallel()

	document := loadOpenAPIDocument(t)
	documentedOperations, operationIDs := documentedOpenAPIOperations(t, document)
	registeredOperations := registeredRouteOperations(t)

	tests := []struct {
		name  string
		check func() error
	}{
		{
			name: "document metadata",
			check: func() error {
				if document.OpenAPI != "3.1.0" {
					return fmt.Errorf("openapi = %q, want 3.1.0", document.OpenAPI)
				}
				if strings.TrimSpace(document.Info.Title) == "" || strings.TrimSpace(document.Info.Version) == "" {
					return fmt.Errorf("info title and version must be non-empty")
				}
				return nil
			},
		},
		{
			name: "unique operation IDs",
			check: func() error {
				if len(operationIDs) != len(documentedOperations) {
					return fmt.Errorf("operation IDs = %d, operations = %d", len(operationIDs), len(documentedOperations))
				}
				return nil
			},
		},
		{
			name: "registered route parity",
			check: func() error {
				differences := compareOperations(registeredOperations, documentedOperations)
				if len(differences) > 0 {
					return fmt.Errorf("route contract mismatch:\n%s", strings.Join(differences, "\n"))
				}
				return nil
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if err := tt.check(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCompareOperations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		registered map[string]struct{}
		documented map[string]struct{}
		want       []string
	}{
		{
			name:       "matching operations",
			registered: operationSet("GET /health"),
			documented: operationSet("GET /health"),
		},
		{
			name:       "missing documentation",
			registered: operationSet("GET /health", "GET /ready"),
			documented: operationSet("GET /health"),
			want:       []string{"missing from OpenAPI: GET /ready"},
		},
		{
			name:       "stale documentation",
			registered: operationSet("GET /health"),
			documented: operationSet("GET /health", "POST /laravel"),
			want:       []string{"not registered by Gin: POST /laravel"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := compareOperations(tt.registered, tt.documented); !slices.Equal(got, tt.want) {
				t.Fatalf("compareOperations() = %v, want %v", got, tt.want)
			}
		})
	}
}

func loadOpenAPIDocument(t *testing.T) openAPIDocument {
	t.Helper()

	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve OpenAPI contract test path")
	}
	contractPath := filepath.Join(filepath.Dir(filename), "..", "..", "api", "openapi.json")
	content, err := os.ReadFile(contractPath)
	if err != nil {
		t.Fatalf("read OpenAPI contract: %v", err)
	}

	var document openAPIDocument
	if err := json.Unmarshal(content, &document); err != nil {
		t.Fatalf("decode OpenAPI contract: %v", err)
	}
	if len(document.Paths) == 0 {
		t.Fatal("OpenAPI contract has no paths")
	}

	return document
}

func documentedOpenAPIOperations(t *testing.T, document openAPIDocument) (map[string]struct{}, map[string]struct{}) {
	t.Helper()

	operations := make(map[string]struct{})
	operationIDs := make(map[string]struct{})
	for path, pathItem := range document.Paths {
		for method, rawOperation := range pathItem {
			method = strings.ToLower(method)
			if _, ok := openAPIMethods[method]; !ok {
				continue
			}

			var operation openAPIOperation
			if err := json.Unmarshal(rawOperation, &operation); err != nil {
				t.Fatalf("decode OpenAPI operation %s %s: %v", strings.ToUpper(method), path, err)
			}
			if strings.TrimSpace(operation.OperationID) == "" {
				t.Fatalf("OpenAPI operation %s %s has no operationId", strings.ToUpper(method), path)
			}
			if len(operation.Responses) == 0 {
				t.Fatalf("OpenAPI operation %s %s has no responses", strings.ToUpper(method), path)
			}
			if _, exists := operationIDs[operation.OperationID]; exists {
				t.Fatalf("duplicate OpenAPI operationId %q", operation.OperationID)
			}

			operations[strings.ToUpper(method)+" "+path] = struct{}{}
			operationIDs[operation.OperationID] = struct{}{}
		}
	}

	return operations, operationIDs
}

// registeredRouteOperations 构建**真实**的 engine 并读回它实际注册的路由。
//
// 系统探针现在也是 RouteSpec 的一部分，因此不再需要额外的旁路注册调用——
// 这正是"唯一 registrar"要达到的效果：想拿到全部路由，只有一条路径。
func registeredRouteOperations(tb testing.TB) map[string]struct{} {
	tb.Helper()

	userModule := userhttp.NewModule(newContractUserService(tb))
	// 认证模块与生成器保持一致：契约里有这四条路由，真实 engine 就必须能注册
	// 出同样的四条。service 传 nil——本测试只走描述符，不执行 handler。
	authModule := authhttp.NewModule(nil)
	engine, err := router.InitRouterWithVersionedModules("test", router.VersionedModules{
		V1: []router.Module{userModule, authModule},
	}, router.RouterOptions{
		// 声明了 AccessAuthenticated 的路由必须有鉴权实现，否则 registrar 拒绝启动。
		AuthMiddleware: func(c *gin.Context) { c.Next() },
		// 业务路由声明了默认限流策略，因此必须提供注册表。
		// registrar 在缺注册表时拒绝启动——那正是要的：否则那些路由会静默地
		// 完全没有限流，而 RouteSpec 里明明写着策略 ID。
		RateLimits:    contractRateLimitRegistry(tb),
		RateLimitKeys: contractKeyBuilder(tb),
	})
	if err != nil {
		tb.Fatalf("build engine: %v", err)
	}

	operations := make(map[string]struct{})
	for _, route := range engine.Routes() {
		operations[route.Method+" "+route.Path] = struct{}{}
	}
	return operations
}

func compareOperations(registered, documented map[string]struct{}) []string {
	differences := make([]string, 0)
	for operation := range registered {
		if _, ok := documented[operation]; !ok {
			differences = append(differences, "missing from OpenAPI: "+operation)
		}
	}
	for operation := range documented {
		if _, ok := registered[operation]; !ok {
			differences = append(differences, "not registered by Gin: "+operation)
		}
	}
	slices.Sort(differences)
	return differences
}

func operationSet(operations ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(operations))
	for _, operation := range operations {
		result[operation] = struct{}{}
	}
	return result
}

// contractUserRepo 是契约测试用的空壳仓储：本测试只关心「路由表 ↔ OpenAPI 契约」，
// 不触发任何 handler 逻辑，因此各方法返回零值即可。
//
// 它存在的唯一原因是 application.NewService 现在拒绝不完整依赖——
// 这正是期望的行为：连测试都不能再随手传 nil 装出一个半残的 service。
type contractUserRepo struct{}

func (contractUserRepo) ExistsByID(context.Context, int64) (bool, error) { return false, nil }
func (contractUserRepo) Get(context.Context, string) (models.User, bool, error) {
	return models.User{}, false, nil
}
func (contractUserRepo) Update(context.Context, int64, map[string]any) error { return nil }
func (contractUserRepo) LockByID(context.Context, int64) (*models.User, bool, error) {
	return nil, false, nil
}
func (contractUserRepo) AnonymizeForDeactivation(context.Context, int64, string, string) error {
	return nil
}
func (contractUserRepo) SoftDeleteProfile(context.Context, int64) error { return nil }

type contractDeactRecorder struct{}

func (contractDeactRecorder) Create(context.Context, *models.UserDeactivation) error { return nil }

type contractTxManager struct{}

func (contractTxManager) Do(ctx context.Context, fn func(context.Context) error) error {
	return fn(ctx)
}

// newContractUserService 装出一个依赖齐全但全为空实现的 user service。
func newContractUserService(tb testing.TB) *userapp.Service {
	tb.Helper()

	svc, err := userapp.NewService(userapp.Deps{
		Repo:     contractUserRepo{},
		Recorder: contractDeactRecorder{},
		Tx:       contractTxManager{},
	})
	if err != nil {
		tb.Fatalf("build contract user service: %v", err)
	}
	return svc
}

// contractRateLimitRegistry 登记业务路由引用的默认策略。
//
// 参数与生产模板一致即可——本测试只关心路由能否装配，不验证限流行为本身。
func contractRateLimitRegistry(tb testing.TB) *ratelimit.Registry {
	tb.Helper()

	registry := ratelimit.NewRegistry()
	for _, p := range []ratelimit.Policy{
		{
			ID: "default.ip", Rate: 5, Burst: 20,
			Scope: ratelimit.ScopeIP, Store: ratelimit.StoreLocal,
			FailureMode: ratelimit.FailureClosed, MaxKeys: 100000,
		},
		{
			ID: "default.user", Rate: 10, Burst: 50,
			Scope: ratelimit.ScopeUser, Store: ratelimit.StoreLocal,
			FailureMode: ratelimit.FailureClosed, MaxKeys: 100000,
		},
		{
			ID: "auth.credential", Rate: 0.2, Burst: 10,
			Scope: ratelimit.ScopeIP, Store: ratelimit.StoreLocal,
			FailureMode: ratelimit.FailureClosed, MaxKeys: 100000,
		},
		{
			ID: "auth.token", Rate: 1, Burst: 20,
			Scope: ratelimit.ScopeIP, Store: ratelimit.StoreLocal,
			FailureMode: ratelimit.FailureClosed, MaxKeys: 100000,
		},
	} {
		limiter, err := ratelimit.NewLocalBackend(p)
		if err != nil {
			tb.Fatalf("NewLocalBackend(%s): %v", p.ID, err)
		}
		if err := registry.Register(limiter); err != nil {
			tb.Fatalf("Register(%s): %v", p.ID, err)
		}
	}
	return registry
}

func contractKeyBuilder(tb testing.TB) *ratelimit.KeyBuilder {
	tb.Helper()

	builder, err := ratelimit.NewKeyBuilder("contract-test-secret-not-a-real-credential", "contract")
	if err != nil {
		tb.Fatalf("NewKeyBuilder: %v", err)
	}
	return builder
}
