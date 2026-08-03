package http

import (
	"net/http"
	"testing"

	"github.com/gtkit/gin-api/internal/router/routespec"
)

func TestUserModuleRoutes(t *testing.T) {
	t.Parallel()

	routes := NewModule(nil).Routes()

	tests := []struct {
		name      string
		method    string
		path      string
		wantFound bool
	}{
		{name: "get current profile", method: http.MethodGet, path: "/user", wantFound: true},
		{name: "update current profile", method: http.MethodPut, path: "/user", wantFound: true},
		{name: "deactivate current profile", method: http.MethodPost, path: "/user/deactivate", wantFound: true},
		// 该路由曾经带一个从不使用的路径参数，删除后不得复活。
		{name: "legacy unused id route removed", method: http.MethodPut, path: "/user/:id", wantFound: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			found := false
			for _, r := range routes {
				if r.Method == tt.method && r.Path == tt.path {
					found = true
					break
				}
			}
			if found != tt.wantFound {
				t.Fatalf("route %s %s found=%t, want %t", tt.method, tt.path, found, tt.wantFound)
			}
		})
	}
}

// TestUserModuleRoutesAllRequireAuthentication 是本模块最重要的一条断言。
//
// 此前鉴权靠 group.Use(authMiddleware) 表达：新增路由时忘了挂进同一个 group，
// 它就会静默变成公开接口，而代码看上去毫无异常。改成描述符后，
// 这条测试能逐条锁死每个路由的访问模式。
func TestUserModuleRoutesAllRequireAuthentication(t *testing.T) {
	t.Parallel()

	for _, r := range NewModule(nil).Routes() {
		if r.AccessPolicy.Mode != routespec.AccessAuthenticated {
			t.Fatalf("route %s %s has access mode %s, want authenticated",
				r.Method, r.Path, r.AccessPolicy.Mode)
		}
	}
}

// TestUserModuleRoutesDeclarePolicies 守住零值 fail-closed：
// 描述符的价值在于"漏写就启动失败"，因此模块自己也不能留下未声明的策略。
func TestUserModuleRoutesDeclarePolicies(t *testing.T) {
	t.Parallel()

	routes := NewModule(nil).Routes()
	if len(routes) == 0 {
		t.Fatal("module must contribute at least one route")
	}
	for _, r := range routes {
		if r.AccessPolicy.Mode == routespec.AccessUnspecified {
			t.Fatalf("route %s %s left the access policy unspecified", r.Method, r.Path)
		}
		if r.TimeoutPolicy.Mode == routespec.TimeoutUnspecified {
			t.Fatalf("route %s %s left the timeout policy unspecified", r.Method, r.Path)
		}
		if r.Handler == nil {
			t.Fatalf("route %s %s has a nil handler", r.Method, r.Path)
		}
	}
}
