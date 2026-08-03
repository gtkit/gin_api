package router

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/router/routespec"
)

type testModule struct {
	name   string
	called bool
}

func (m *testModule) Name() string {
	if m.name == "" {
		return "module.test"
	}
	return m.name
}

func (m *testModule) Routes() []routespec.RouteSpec {
	m.called = true
	return []routespec.RouteSpec{{
		Method: http.MethodGet,
		// operationId 由模块名派生：同一个 testModule 类型会被实例化成
		// v1/v2 两个模块，写死会撞上 compile 的全局唯一约束。
		OperationID: "probe_" + strings.ReplaceAll(m.Name(), ".", "_"),
		Path:        "/probe",
		Handler:     func(c *gin.Context) { c.Status(http.StatusNoContent) },
		// 测试模块同样必须显式声明策略——零值会被 compile 拒绝，
		// 这正是描述符要保证的性质。
		AccessPolicy: routespec.AccessPolicy{Mode: routespec.AccessPublic},
		TimeoutPolicy: routespec.TimeoutPolicy{
			Mode:       routespec.TimeoutCooperative,
			Deadline:   time.Second,
			WriteGrace: time.Second,
		},
	}}
}

func TestGinModeFromEnv(t *testing.T) {
	// case 用 config.Env 常量驱动，锁定 gin mode 与 Env.IsProd() 共用同一套环境定义；
	// 大小写/空白变体不再归一化——它们在 config.Validate 已被拒绝，这里兜底落 Debug。
	cases := []struct {
		env  string
		want string
	}{
		{env: string(config.EnvProd), want: gin.ReleaseMode},
		{env: string(config.EnvTest), want: gin.TestMode},
		{env: string(config.EnvDev), want: gin.DebugMode},
		{env: "", want: gin.DebugMode},
		{env: "Prod", want: gin.DebugMode},
		{env: " prod ", want: gin.DebugMode},
		{env: "production", want: gin.DebugMode},
	}

	for _, tc := range cases {
		if got := ginModeFromEnv(tc.env); got != tc.want {
			t.Fatalf("ginModeFromEnv(%q) = %q, want %q", tc.env, got, tc.want)
		}
	}
}

func TestInitRouterUsesExplicitEnv(t *testing.T) {
	oldMode := gin.Mode()
	defer gin.SetMode(oldMode)

	module := &testModule{}
	engine, err := InitRouterWithModules("prod", []Module{module})
	if err != nil {
		t.Fatalf("InitRouterWithModules returned error: %v", err)
	}
	if engine == nil {
		t.Fatal("expected engine")
	}
	if !module.called {
		t.Fatal("expected module to be called")
	}
	if gin.Mode() != gin.ReleaseMode {
		t.Fatalf("gin mode = %q, want %q", gin.Mode(), gin.ReleaseMode)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/probe", nil)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
}

func TestInitRouterWithVersionedModulesRegistersV1AndV2(t *testing.T) {
	oldMode := gin.Mode()
	defer gin.SetMode(oldMode)

	v1Module := &testModule{name: "module.v1"}
	v2Module := &testModule{name: "module.v2"}
	engine, err := InitRouterWithVersionedModules("prod", VersionedModules{
		V1: []Module{v1Module},
		V2: []Module{v2Module},
	})
	if err != nil {
		t.Fatalf("InitRouterWithVersionedModules returned error: %v", err)
	}
	if engine == nil {
		t.Fatal("expected engine")
	}
	if !v1Module.called || !v2Module.called {
		t.Fatalf("expected both modules to be called, v1=%v v2=%v", v1Module.called, v2Module.called)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/probe", nil)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("v1 status = %d, want %d", recorder.Code, http.StatusNoContent)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v2/probe", nil)
	recorder = httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)
	if recorder.Code != http.StatusNoContent {
		t.Fatalf("v2 status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
}
