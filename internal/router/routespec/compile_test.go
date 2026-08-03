package routespec

import (
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func noopHandler(*gin.Context) {}

func okAccess() AccessPolicy { return AccessPolicy{Mode: AccessPublic} }
func okTimeout() TimeoutPolicy {
	return TimeoutPolicy{Mode: TimeoutCooperative, Deadline: time.Second, WriteGrace: time.Second}
}

func specOK() RouteSpec {
	return RouteSpec{
		Method: http.MethodGet, Path: "/thing", Handler: noopHandler,
		AccessPolicy: okAccess(), TimeoutPolicy: okTimeout(),
	}
}

func compileOne(spec RouteSpec) error {
	_, err := Compile([]Contribution{{Source: "test", Prefix: "/api/v1", Routes: []RouteSpec{spec}}})
	return err
}

// TestCompileRejectsZeroValuePolicies 是本包存在的核心理由：
// 作者漏写策略是最可能的失误路径，零值必须指向拒绝而不是"默认放行"。
func TestCompileRejectsZeroValuePolicies(t *testing.T) {
	t.Parallel()

	t.Run("access policy unspecified", func(t *testing.T) {
		t.Parallel()
		s := specOK()
		s.AccessPolicy = AccessPolicy{}
		err := compileOne(s)
		if err == nil || !strings.Contains(err.Error(), "access policy is unspecified") {
			t.Fatalf("zero-value access policy must be rejected, got %v", err)
		}
	})

	t.Run("timeout policy unspecified", func(t *testing.T) {
		t.Parallel()
		s := specOK()
		s.TimeoutPolicy = TimeoutPolicy{}
		err := compileOne(s)
		if err == nil || !strings.Contains(err.Error(), "timeout policy is unspecified") {
			t.Fatalf("zero-value timeout policy must be rejected, got %v", err)
		}
	})
}

func TestCompileValidatesMethod(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		method  string
		wantErr bool
	}{
		{name: "get", method: http.MethodGet},
		{name: "post", method: http.MethodPost},
		{name: "put", method: http.MethodPut},
		{name: "patch", method: http.MethodPatch},
		{name: "delete", method: http.MethodDelete},
		{name: "head", method: http.MethodHead},
		// OPTIONS 只能由 CORS 派生，模块自己声明会产生两套预检来源。
		{name: "options rejected", method: http.MethodOptions, wantErr: true},
		// TRACE 会回显请求内容；CONNECT 是代理语义。
		{name: "trace rejected", method: http.MethodTrace, wantErr: true},
		{name: "connect rejected", method: http.MethodConnect, wantErr: true},
		{name: "lowercase rejected", method: "get", wantErr: true},
		{name: "empty rejected", method: "", wantErr: true},
		{name: "custom rejected", method: "PURGE", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := specOK()
			s.Method = tt.method
			if err := compileOne(s); (err != nil) != tt.wantErr {
				t.Fatalf("method %q err = %v, wantErr = %v", tt.method, err, tt.wantErr)
			}
		})
	}
}

func TestCompileValidatesPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		path    string
		wantErr bool
	}{
		{name: "simple", path: "/thing"},
		{name: "nested", path: "/thing/sub"},
		{name: "param", path: "/thing/:id"},
		{name: "root means prefix itself", path: "/"},
		{name: "empty rejected", path: "", wantErr: true},
		{name: "missing leading slash rejected", path: "thing", wantErr: true},
		{name: "scheme rejected", path: "/http://evil", wantErr: true},
		{name: "query rejected", path: "/thing?a=1", wantErr: true},
		{name: "fragment rejected", path: "/thing#x", wantErr: true},
		{name: "double slash rejected", path: "/thing//sub", wantErr: true},
		{name: "dot dot rejected", path: "/thing/../x", wantErr: true},
		{name: "single dot rejected", path: "/thing/./x", wantErr: true},
		// 尾斜杠 + RedirectTrailingSlash=false 会组合成"注册了但访问不到"。
		{name: "trailing slash rejected", path: "/thing/", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := specOK()
			s.Path = tt.path
			if err := compileOne(s); (err != nil) != tt.wantErr {
				t.Fatalf("path %q err = %v, wantErr = %v", tt.path, err, tt.wantErr)
			}
		})
	}
}

func TestCompileRejectsNilHandler(t *testing.T) {
	t.Parallel()

	s := specOK()
	s.Handler = nil
	if err := compileOne(s); err == nil || !strings.Contains(err.Error(), "handler must not be nil") {
		t.Fatalf("nil handler must be rejected, got %v", err)
	}
}

func TestCompileValidatesAccessPolicyCombinations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		policy  AccessPolicy
		wantErr bool
	}{
		{name: "public", policy: AccessPolicy{Mode: AccessPublic}},
		{name: "optional", policy: AccessPolicy{Mode: AccessOptionalAuthenticated}},
		{name: "authenticated", policy: AccessPolicy{Mode: AccessAuthenticated}},
		{name: "authorized with id", policy: AccessPolicy{Mode: AccessAuthorized, AuthorizerID: "role.admin"}},
		// 非 Authorized 模式带 authorizer id 说明作者搞混了模式。
		{name: "public with authorizer id", policy: AccessPolicy{Mode: AccessPublic, AuthorizerID: "role.admin"}, wantErr: true},
		{name: "authenticated with authorizer id", policy: AccessPolicy{Mode: AccessAuthenticated, AuthorizerID: "x"}, wantErr: true},
		{name: "authorized without id", policy: AccessPolicy{Mode: AccessAuthorized}, wantErr: true},
		{name: "authorized with uppercase id", policy: AccessPolicy{Mode: AccessAuthorized, AuthorizerID: "Role.Admin"}, wantErr: true},
		{name: "authorized with leading digit", policy: AccessPolicy{Mode: AccessAuthorized, AuthorizerID: "1role"}, wantErr: true},
		{name: "unknown mode", policy: AccessPolicy{Mode: AccessMode(99)}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := specOK()
			s.AccessPolicy = tt.policy
			if err := compileOne(s); (err != nil) != tt.wantErr {
				t.Fatalf("access policy %+v err = %v, wantErr = %v", tt.policy, err, tt.wantErr)
			}
		})
	}
}

func TestCompileValidatesTimeoutPolicyCombinations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		policy  TimeoutPolicy
		wantErr bool
	}{
		{
			name:   "cooperative ok",
			policy: TimeoutPolicy{Mode: TimeoutCooperative, Deadline: time.Second, WriteGrace: time.Second},
		},
		{
			name:   "client visible ok",
			policy: TimeoutPolicy{Mode: TimeoutClientVisible, Deadline: time.Second, WriteGrace: time.Second},
		},
		{
			name: "streaming ok",
			policy: TimeoutPolicy{
				Mode: TimeoutStreaming, IdleWrite: 30 * time.Second, HeartbeatInterval: 10 * time.Second,
			},
		},
		{name: "cooperative without deadline", policy: TimeoutPolicy{Mode: TimeoutCooperative, WriteGrace: time.Second}, wantErr: true},
		{name: "cooperative without write grace", policy: TimeoutPolicy{Mode: TimeoutCooperative, Deadline: time.Second}, wantErr: true},
		{
			// 流式字段出现在非流式策略上，说明作者搞混了模式。
			name: "cooperative with streaming fields",
			policy: TimeoutPolicy{
				Mode: TimeoutCooperative, Deadline: time.Second, WriteGrace: time.Second, IdleWrite: time.Second,
			},
			wantErr: true,
		},
		{
			// 心跳不早于空闲超时就永远续不上——write deadline 过期后无法延长。
			name: "heartbeat equal to idle write",
			policy: TimeoutPolicy{
				Mode: TimeoutStreaming, IdleWrite: 10 * time.Second, HeartbeatInterval: 10 * time.Second,
			},
			wantErr: true,
		},
		{
			name: "heartbeat longer than idle write",
			policy: TimeoutPolicy{
				Mode: TimeoutStreaming, IdleWrite: 10 * time.Second, HeartbeatInterval: 11 * time.Second,
			},
			wantErr: true,
		},
		{
			name: "streaming with deadline",
			policy: TimeoutPolicy{
				Mode: TimeoutStreaming, IdleWrite: 30 * time.Second,
				HeartbeatInterval: 10 * time.Second, Deadline: time.Second,
			},
			wantErr: true,
		},
		{name: "unknown mode", policy: TimeoutPolicy{Mode: TimeoutMode(99)}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := specOK()
			s.TimeoutPolicy = tt.policy
			if err := compileOne(s); (err != nil) != tt.wantErr {
				t.Fatalf("timeout policy %+v err = %v, wantErr = %v", tt.policy, err, tt.wantErr)
			}
		})
	}
}

// TestCompileRejectsDuplicateRoutes 覆盖重复注册。
// 错误信息必须点名两个来源——只说"重复"会让人在几十个模块里逐个找。
func TestCompileRejectsDuplicateRoutes(t *testing.T) {
	t.Parallel()

	a := Contribution{Source: "module.a", Prefix: "/api/v1", Routes: []RouteSpec{specOK()}}
	b := Contribution{Source: "module.b", Prefix: "/api/v1", Routes: []RouteSpec{specOK()}}

	_, err := Compile([]Contribution{a, b})
	if err == nil {
		t.Fatal("duplicate route must be rejected")
	}
	for _, want := range []string{"module.a", "module.b", "duplicate"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error must mention %q for locating the conflict, got %v", want, err)
		}
	}
}

// TestCompileDetectsPathTreeConflict 覆盖只有 Gin 路由树知道的冲突：
// 两条路径字符串不同，前面的重复检查抓不到，而 Gin 注册时会 panic。
func TestCompileDetectsPathTreeConflict(t *testing.T) {
	t.Parallel()

	byID := specOK()
	byID.Path = "/thing/:id"
	byName := specOK()
	byName.Path = "/thing/:name"

	_, err := Compile([]Contribution{
		{Source: "module.a", Prefix: "/api/v1", Routes: []RouteSpec{byID, byName}},
	})
	if err == nil || !strings.Contains(err.Error(), "path tree conflict") {
		t.Fatalf("conflicting path parameters must be reported as a compile error, got %v", err)
	}
}

// TestCompileReportsAllErrorsAtOnce 守住"一次报全"：
// 逐条修复再重跑是很慢的反馈循环，尤其在批量新增路由时。
func TestCompileReportsAllErrorsAtOnce(t *testing.T) {
	t.Parallel()

	bad1 := specOK()
	bad1.Method = "PURGE"
	bad2 := specOK()
	bad2.Path = "no-slash"

	_, err := Compile([]Contribution{
		{Source: "module.a", Prefix: "/api/v1", Routes: []RouteSpec{bad1, bad2}},
	})
	if err == nil {
		t.Fatal("expected errors")
	}
	if !strings.Contains(err.Error(), "PURGE") || !strings.Contains(err.Error(), "no-slash") {
		t.Fatalf("all failures should be reported together, got %v", err)
	}
}

func TestCompileJoinsPrefix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		prefix string
		path   string
		want   string
	}{
		{name: "normal", prefix: "/api/v1", path: "/user", want: "/api/v1/user"},
		{name: "nested", prefix: "/api/v1", path: "/user/deactivate", want: "/api/v1/user/deactivate"},
		// 模块路径 "/" 表示"就是前缀本身"。
		{name: "root path", prefix: "/api/v1", path: "/", want: "/api/v1"},
		{name: "no prefix", prefix: "", path: "/health", want: "/health"},
		{name: "prefix with trailing slash", prefix: "/api/v1/", path: "/user", want: "/api/v1/user"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := specOK()
			s.Path = tt.path
			compiled, err := Compile([]Contribution{{Source: "t", Prefix: tt.prefix, Routes: []RouteSpec{s}}})
			if err != nil {
				t.Fatalf("Compile: %v", err)
			}
			if compiled[0].FullPath != tt.want {
				t.Fatalf("full path = %q, want %q", compiled[0].FullPath, tt.want)
			}
		})
	}
}

func TestCompileValidatesRateLimitPolicyID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		id      string
		wantErr bool
	}{
		{name: "empty means no limit"},
		{name: "valid", id: "ip.default"},
		{name: "valid with dash", id: "user-scope.login"},
		{name: "uppercase rejected", id: "IP.Default", wantErr: true},
		{name: "leading digit rejected", id: "1ip", wantErr: true},
		{name: "space rejected", id: "ip default", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := specOK()
			s.RateLimitPolicyID = tt.id
			if err := compileOne(s); (err != nil) != tt.wantErr {
				t.Fatalf("rate limit policy id %q err = %v, wantErr = %v", tt.id, err, tt.wantErr)
			}
		})
	}
}
