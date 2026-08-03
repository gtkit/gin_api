package openapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/gtkit/gin-api/internal/router/routespec"
)

func route(method, path string, mutate func(*routespec.RouteSpec)) routespec.CompiledRoute {
	spec := routespec.RouteSpec{
		Method:      method,
		Path:        path,
		OperationID: "op" + strings.ReplaceAll(strings.ToLower(method)+path, "/", "_"),
		AccessPolicy: routespec.AccessPolicy{
			Mode: routespec.AccessPublic,
		},
		ResponseShape: routespec.ResponseShapeEmpty,
	}
	if mutate != nil {
		mutate(&spec)
	}
	return routespec.CompiledRoute{FullPath: "/api/v1" + path, Spec: spec}
}

// TestGenerateIsDeterministic 钉住输出确定性。
//
// 生成物一致性门禁比对的是字节。任何随生成时刻或 map 遍历顺序变化的内容
// 都会让门禁在无关改动上失败，而一条"经常无故失败"的门禁最终会被关掉。
func TestGenerateIsDeterministic(t *testing.T) {
	t.Parallel()

	routes := []routespec.CompiledRoute{
		route(http.MethodGet, "/b", func(s *routespec.RouteSpec) { s.Tags = []string{"Z", "A"} }),
		route(http.MethodPost, "/a", func(s *routespec.RouteSpec) { s.Tags = []string{"M"} }),
		route(http.MethodGet, "/a/:id", nil),
	}

	first := mustRender(t, routes)
	for range 20 {
		if got := mustRender(t, routes); got != first {
			t.Fatal("Generate produced a different byte sequence on a repeated run")
		}
	}

	// 生成物绝不能带时间戳。
	for _, banned := range []string{"generatedAt", "timestamp", "20"} {
		if banned == "20" {
			continue // 版本号里合法出现，跳过这个过宽的探针
		}
		if strings.Contains(first, banned) {
			t.Fatalf("output contains %q, which would change on every run", banned)
		}
	}
}

// TestSecurityIsDerivedFromAccessPolicy 钉住 security 由访问策略派生。
//
// 维护第二个字段的话，改了访问策略却忘了改文档，客户端会照着一份过期的
// 安全声明去实现——而那个错误只在真实调用时才暴露。
func TestSecurityIsDerivedFromAccessPolicy(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name            string
		policy          routespec.AccessPolicy
		wantSecurity    int
		wantAnonymous   bool
		wantAuthorizer  string
		wantStatusCodes []string
	}{
		{
			name:            "public has no security",
			policy:          routespec.AccessPolicy{Mode: routespec.AccessPublic},
			wantSecurity:    0,
			wantStatusCodes: []string{"200"},
		},
		{
			name:            "optional allows anonymous",
			policy:          routespec.AccessPolicy{Mode: routespec.AccessOptionalAuthenticated},
			wantSecurity:    2,
			wantAnonymous:   true,
			wantStatusCodes: []string{"200", "401"},
		},
		{
			name:            "authenticated requires the token",
			policy:          routespec.AccessPolicy{Mode: routespec.AccessAuthenticated},
			wantSecurity:    1,
			wantStatusCodes: []string{"200", "401"},
		},
		{
			name: "authorized adds 403 and 503",
			policy: routespec.AccessPolicy{
				Mode: routespec.AccessAuthorized, AuthorizerID: "entitlement.vip",
			},
			wantSecurity:    1,
			wantAuthorizer:  "entitlement.vip",
			wantStatusCodes: []string{"200", "401", "403", "503"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			doc := mustGenerate(t, []routespec.CompiledRoute{
				route(http.MethodGet, "/x", func(s *routespec.RouteSpec) { s.AccessPolicy = tc.policy }),
			}, Options{Title: "t", Version: "1"})

			op := doc.Paths["/api/v1/x"]["get"]
			if len(op.Security) != tc.wantSecurity {
				t.Fatalf("security entries = %d, want %d", len(op.Security), tc.wantSecurity)
			}
			if tc.wantAnonymous {
				var hasEmpty bool
				for _, entry := range op.Security {
					if len(entry) == 0 {
						hasEmpty = true
					}
				}
				if !hasEmpty {
					t.Fatal("optional auth must declare an empty security entry (anonymous is allowed)")
				}
			}
			// 授权实现走受控扩展字段，不伪装成 OpenAPI 能执行的权限。
			if op.XAuthorizer != tc.wantAuthorizer {
				t.Fatalf("x-authorizer = %q, want %q", op.XAuthorizer, tc.wantAuthorizer)
			}
			for _, status := range tc.wantStatusCodes {
				if _, ok := op.Responses[status]; !ok {
					t.Fatalf("response %s is missing (have %v)", status, keysOf(op.Responses))
				}
			}
		})
	}
}

// TestAppTokenSchemeShape 钉住安全方案的形状。
//
// 不能声明成 HTTP bearer：那会让客户端发送 `Authorization: Bearer`，
// 而运行时只接受自定义的 `App-Token` header——生成的客户端会 100% 认证失败。
func TestAppTokenSchemeShape(t *testing.T) {
	t.Parallel()

	doc := mustGenerate(t, nil, Options{Title: "t", Version: "1"})
	scheme, ok := doc.Components.SecuritySchemes[AppTokenScheme]
	if !ok {
		t.Fatalf("security scheme %q is missing", AppTokenScheme)
	}
	if scheme.Type != "apiKey" || scheme.In != "header" || scheme.Name != "App-Token" {
		t.Fatalf("scheme = %+v, want apiKey/header/App-Token", scheme)
	}
}

// TestPathParametersAreExtracted 验证 Gin 的 `:id` 被翻译成 OpenAPI 的 `{id}`。
//
// 不翻译的话，生成的客户端会把 `:id` 当成路径字面量发出去。
func TestPathParametersAreExtracted(t *testing.T) {
	t.Parallel()

	doc := mustGenerate(t, []routespec.CompiledRoute{
		route(http.MethodGet, "/thing/:id", nil),
	}, Options{Title: "t", Version: "1"})

	if _, ok := doc.Paths["/api/v1/thing/{id}"]; !ok {
		t.Fatalf("path was not converted to OpenAPI syntax: %v", keysOfPaths(doc.Paths))
	}
	op := doc.Paths["/api/v1/thing/{id}"]["get"]
	if len(op.Parameters) != 1 {
		t.Fatalf("parameters = %+v, want exactly one", op.Parameters)
	}
	if op.Parameters[0].Name != "id" || !op.Parameters[0].Required {
		t.Fatalf("parameter = %+v, want a required 'id'", op.Parameters[0])
	}
}

// TestDescriptorResponsesOverrideDerived 验证描述符声明的响应并入结果。
//
// 响应契约归描述符所有而不是由生成器猜：生成器只能从方法名猜，
// 而"这个接口会不会返回 422"是业务事实，猜错的方向是漏声明。
func TestDescriptorResponsesOverrideDerived(t *testing.T) {
	t.Parallel()

	doc := mustGenerate(t, []routespec.CompiledRoute{
		route(http.MethodPost, "/x", func(s *routespec.RouteSpec) {
			s.AccessPolicy = routespec.AccessPolicy{Mode: routespec.AccessAuthenticated}
			s.Responses = map[string]string{"422": "validation failed", "500": "internal"}
		}),
	}, Options{Title: "t", Version: "1"})

	op := doc.Paths["/api/v1/x"]["post"]
	for _, status := range []string{"200", "401", "422", "500"} {
		if _, ok := op.Responses[status]; !ok {
			t.Fatalf("response %s is missing (have %v)", status, keysOf(op.Responses))
		}
	}
	if got := op.Responses["422"].Description; got != "validation failed" {
		t.Fatalf("422 description = %q, want the descriptor's text", got)
	}
}

func mustRender(t *testing.T, routes []routespec.CompiledRoute) string {
	t.Helper()

	doc := mustGenerate(t, routes, Options{Title: "svc", Version: "1.0.0"})
	var sb strings.Builder
	if err := Write(&sb, doc); err != nil {
		t.Fatalf("Write: %v", err)
	}
	// 顺带确认输出是合法 JSON。
	var probe map[string]any
	if err := json.Unmarshal([]byte(sb.String()), &probe); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	return sb.String()
}

func keysOf(m map[string]Response) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func keysOfPaths(m map[string]PathItem) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
