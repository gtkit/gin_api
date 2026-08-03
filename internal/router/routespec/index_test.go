package routespec

import (
	"net/http"
	"strings"
	"testing"
	"time"
)

// idxSpec 构造一条测试路由。operationId 由 method+path 派生，
// 保证同一批 spec 内部唯一（compile 强制全局唯一）。
func idxSpec(method, path string) RouteSpec {
	return RouteSpec{
		Method: method, Path: path, Handler: noopHandler,
		OperationID:   idxOperationID(method, path),
		AccessPolicy:  AccessPolicy{Mode: AccessPublic},
		TimeoutPolicy: TimeoutPolicy{Mode: TimeoutCooperative, Deadline: time.Second, WriteGrace: time.Second},
	}
}

func buildTestIndex(t *testing.T, specs ...RouteSpec) *RouteIndex {
	t.Helper()
	compiled, err := Compile([]Contribution{{Source: "test", Prefix: "/api/v1", Routes: specs}})
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return BuildIndex(compiled)
}

// TestRouteIndexStaticBeatsPattern 锁住 Gin 的匹配优先级：
// 静态段优先于参数段，否则 /user/me 会被 /user/:id 抢走，
// 405 的 Allow 头就会报出错误路由的方法集合。
func TestRouteIndexStaticBeatsPattern(t *testing.T) {
	t.Parallel()

	idx := buildTestIndex(t,
		idxSpec(http.MethodGet, "/user/:id"),
		idxSpec(http.MethodPost, "/user/me"),
	)

	if got := idx.AllowedMethods("/api/v1/user/me"); len(got) != 1 || got[0] != http.MethodPost {
		t.Fatalf("static path must win over the parameter pattern, got %v", got)
	}
	if got := idx.AllowedMethods("/api/v1/user/42"); len(got) != 1 || got[0] != http.MethodGet {
		t.Fatalf("parameter pattern should match a non-static segment, got %v", got)
	}
}

func TestRouteIndexAllowedMethods(t *testing.T) {
	t.Parallel()

	idx := buildTestIndex(t,
		idxSpec(http.MethodGet, "/thing"),
		idxSpec(http.MethodPost, "/thing"),
		idxSpec(http.MethodGet, "/thing/:id"),
		idxSpec(http.MethodDelete, "/tree/*rest"),
	)

	tests := []struct {
		name string
		path string
		want []string
	}{
		{name: "static multi method", path: "/api/v1/thing", want: []string{"GET", "POST"}},
		{name: "param", path: "/api/v1/thing/7", want: []string{"GET"}},
		{name: "catch all one segment", path: "/api/v1/tree/a", want: []string{"DELETE"}},
		{name: "catch all deep", path: "/api/v1/tree/a/b/c", want: []string{"DELETE"}},
		// 未知路径必须返回空——调用方据此区分 404 与 405。
		{name: "unknown path", path: "/api/v1/nope", want: nil},
		{name: "unknown prefix", path: "/other/thing", want: nil},
		// 参数段不匹配空段。
		{name: "param requires a segment", path: "/api/v1/thing/", want: nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := idx.AllowedMethods(tt.path)
			if len(got) != len(tt.want) {
				t.Fatalf("AllowedMethods(%q) = %v, want %v", tt.path, got, tt.want)
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Fatalf("AllowedMethods(%q) = %v, want %v", tt.path, got, tt.want)
				}
			}
		})
	}
}

// TestAllowHeaderValueIsStableAndComplete 锁住 Allow 头的两条性质：
// 顺序固定（它是对外契约，抖动会破坏缓存与快照比对），
// 且包含由 GET 派生的 HEAD 与由 CORS 派生的 OPTIONS（它们是真实可用的方法）。
func TestAllowHeaderValueIsStableAndComplete(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		specs []RouteSpec
		path  string
		want  string
	}{
		{
			name:  "get only gains head and options",
			specs: []RouteSpec{idxSpec(http.MethodGet, "/a")},
			path:  "/api/v1/a", want: "GET, HEAD, OPTIONS",
		},
		{
			name: "declaration order does not affect output order",
			specs: []RouteSpec{
				idxSpec(http.MethodDelete, "/b"),
				idxSpec(http.MethodPost, "/b"),
				idxSpec(http.MethodGet, "/b"),
			},
			path: "/api/v1/b", want: "GET, HEAD, POST, DELETE, OPTIONS",
		},
		{
			name:  "post only does not gain head",
			specs: []RouteSpec{idxSpec(http.MethodPost, "/c")},
			path:  "/api/v1/c", want: "POST, OPTIONS",
		},
		{
			name:  "unknown path yields no header",
			specs: []RouteSpec{idxSpec(http.MethodGet, "/d")},
			path:  "/api/v1/unknown", want: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			idx := buildTestIndex(t, tt.specs...)
			if got := idx.AllowHeaderValue(tt.path); got != tt.want {
				t.Fatalf("AllowHeaderValue(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}

// TestAllowHeaderValueIsDeterministic 直接验证"多次构建结果一致"，
// 因为索引内部用了 map，遍历顺序天然随机。
func TestAllowHeaderValueIsDeterministic(t *testing.T) {
	t.Parallel()

	specs := []RouteSpec{
		idxSpec(http.MethodDelete, "/x"),
		idxSpec(http.MethodGet, "/x"),
		idxSpec(http.MethodPatch, "/x"),
		idxSpec(http.MethodPost, "/x"),
	}
	first := buildTestIndex(t, specs...).AllowHeaderValue("/api/v1/x")
	for i := range 50 {
		if got := buildTestIndex(t, specs...).AllowHeaderValue("/api/v1/x"); got != first {
			t.Fatalf("iteration %d produced %q, want the stable %q", i, got, first)
		}
	}
}

func TestNilRouteIndexIsSafe(t *testing.T) {
	t.Parallel()

	var idx *RouteIndex
	if got := idx.AllowedMethods("/anything"); got != nil {
		t.Fatalf("nil index must report no methods, got %v", got)
	}
	if got := idx.AllowHeaderValue("/anything"); got != "" {
		t.Fatalf("nil index must produce no Allow header, got %q", got)
	}
}

// idxOperationID 从 method 与 path 派生一个合法的 operationId。
func idxOperationID(method, path string) string {
	cleaned := strings.Map(func(r rune) rune {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') {
			return r
		}
		return '_'
	}, path)
	return strings.ToLower(method) + "_" + cleaned
}
