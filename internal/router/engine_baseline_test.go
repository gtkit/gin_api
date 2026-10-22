package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/gtkit/gin-api/internal/middleware"
	resp "github.com/gtkit/gin-api/internal/pkg/response"
	"github.com/gtkit/gin-api/internal/shared/env"
)

// TestGinModeFromEnv 锁住环境名 → gin 运行模式的映射。
//
// 保存并恢复 gin.Mode()：它是进程级全局状态，本测试若把它留在 debug/release，
// 同包后续测试就会在一个非预期的模式下运行。
func TestGinModeFromEnv(t *testing.T) {
	original := gin.Mode()
	t.Cleanup(func() { gin.SetMode(original) })

	tests := []struct {
		name        string
		environment env.Env
		want        string
	}{
		{name: "prod 用发布模式", environment: env.Prod, want: gin.ReleaseMode},
		{name: "test 用测试模式", environment: env.Test, want: gin.TestMode},
		{name: "dev 用调试模式", environment: env.Dev, want: gin.DebugMode},
		{name: "未知环境值回落调试模式", environment: env.Env("staging"), want: gin.DebugMode},
		{name: "零值回落调试模式", environment: env.Env(""), want: gin.DebugMode},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ginModeFromEnv(tt.environment); got != tt.want {
				t.Fatalf("ginModeFromEnv(%q) = %q, want %q", tt.environment, got, tt.want)
			}
		})
	}
}

// TestEngineBaselineFields 断言 engine 每一个可配置字段的定性结果。
//
// 连"保持 gin 默认"的项也一并断言：那些项是有记录的决定，不是遗漏。
// 把它们钉在测试里，使日后任何人顺手改动某个默认值时必须先改这里，
// 从而把"隐式沿用框架默认"变成一次显式选择——这正是本变更要防的那类缺陷。
func TestEngineBaselineFields(t *testing.T) {
	original := gin.Mode()
	t.Cleanup(func() { gin.SetMode(original) })

	engine, err := newRouterForTest("test", Options{})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	boolChecks := []struct {
		name string
		got  bool
		want bool
	}{
		// 显式改掉 gin 默认的三项。
		{name: "RedirectTrailingSlash", got: engine.RedirectTrailingSlash, want: false},
		{name: "HandleMethodNotAllowed", got: engine.HandleMethodNotAllowed, want: true},
		{name: "ContextWithFallback", got: engine.ContextWithFallback, want: true},
		// 有意保持 gin 默认的项。
		{name: "RedirectFixedPath", got: engine.RedirectFixedPath, want: false},
		{name: "RemoveExtraSlash", got: engine.RemoveExtraSlash, want: false},
		{name: "UseRawPath", got: engine.UseRawPath, want: false},
		{name: "UseEscapedPath", got: engine.UseEscapedPath, want: false},
		{name: "UnescapePathValues", got: engine.UnescapePathValues, want: true},
		{name: "UseH2C", got: engine.UseH2C, want: false},
		{name: "ForwardedByClientIP", got: engine.ForwardedByClientIP, want: true},
	}
	for _, c := range boolChecks {
		if c.got != c.want {
			t.Errorf("engine.%s = %v, want %v", c.name, c.got, c.want)
		}
	}

	if engine.TrustedPlatform != "" {
		t.Errorf("engine.TrustedPlatform = %q, want empty: 设置它等于无条件信任某云厂商的 IP 头",
			engine.TrustedPlatform)
	}

	// 保持 gin 默认的 32MB。仓库无 multipart 路由，全局 body 上限已封顶该场景；
	// 为尚不存在的上传路由预设一个值没有依据。
	const wantMultipartMemory = 32 << 20
	if engine.MaxMultipartMemory != wantMultipartMemory {
		t.Errorf("engine.MaxMultipartMemory = %d, want %d", engine.MaxMultipartMemory, wantMultipartMemory)
	}
}

// newBaselineEngine 造一个注册了单条 GET 路由的 engine，供 404/405/尾斜杠用例共用。
func newBaselineEngine(t *testing.T) *gin.Engine {
	t.Helper()

	engine, err := newRouterForTest("test", Options{
		RegisterModules: func(v1 *gin.RouterGroup) {
			v1.GET("/ping", func(c *gin.Context) { c.Status(http.StatusOK) })
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return engine
}

func do(t *testing.T, engine *gin.Engine, method, path string) *httptest.ResponseRecorder {
	t.Helper()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, nil)
	engine.ServeHTTP(rec, req)
	return rec
}

// decodeEnvelope 解出统一响应信封，解不出就是脱离了信封。
func decodeEnvelope(t *testing.T, rec *httptest.ResponseRecorder) resp.ResResult {
	t.Helper()

	if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "application/json") {
		t.Fatalf("Content-Type = %q, want JSON; body = %q", ct, rec.Body.String())
	}

	var got resp.ResResult
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("响应体不是统一信封: %v; body = %q", err, rec.Body.String())
	}
	if got.RequestID == "" || got.RequestID != rec.Header().Get(middleware.RequestIDHeader) {
		t.Fatalf("request ID mismatch: body=%q header=%q", got.RequestID, rec.Header().Get(middleware.RequestIDHeader))
	}
	return got
}

// TestNotFoundUsesEnvelope 钉住未匹配路由走统一信封。
//
// 此前未注册 NoRoute，gin 回的是纯文本 404 page not found——客户端因此要
// 为这一条路径准备第二种解析方式，而它恰好是接错地址时最先撞上的响应。
func TestNotFoundUsesEnvelope(t *testing.T) {
	original := gin.Mode()
	t.Cleanup(func() { gin.SetMode(original) })

	rec := do(t, newBaselineEngine(t), http.MethodGet, "/api/v1/does-not-exist")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusNotFound)
	}
	if body := rec.Body.String(); strings.Contains(body, "404 page not found") {
		t.Fatalf("响应体仍是 gin 默认纯文本: %q", body)
	}

	envelope := decodeEnvelope(t, rec)
	if envelope.Code == 0 {
		t.Errorf("envelope.Code = 0, want 非零业务错误码; body = %q", rec.Body.String())
	}
	if envelope.Message == "" {
		t.Error("envelope.Message 为空")
	}
}

// TestMethodNotAllowed 钉住 405 及其 Allow 头。
//
// Allow 由 gin 自身在进入 NoMethod 之前按 RFC 7231 §6.5.5 设置，
// 处理器不重复计算——本用例同时验证那份头确实被发出。
func TestMethodNotAllowed(t *testing.T) {
	original := gin.Mode()
	t.Cleanup(func() { gin.SetMode(original) })

	rec := do(t, newBaselineEngine(t), http.MethodPost, "/api/v1/ping")

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want %d（HandleMethodNotAllowed 未生效时这里会是 404）",
			rec.Code, http.StatusMethodNotAllowed)
	}
	if allow := rec.Header().Get("Allow"); !strings.Contains(allow, http.MethodGet) {
		t.Errorf("Allow = %q, want 含 GET", allow)
	}

	envelope := decodeEnvelope(t, rec)
	if int32(envelope.Code) != resp.MethodNotAllowedCode {
		t.Errorf("envelope.Code = %d, want %d", envelope.Code, resp.MethodNotAllowedCode)
	}
}

// TestTrailingSlashIsNotRedirected 钉住不做隐式尾斜杠重定向。
//
// gin 默认对非 GET 方法发 307，客户端会带着原请求体重发——一次写操作
// 因此可能落到并非作者本意的路径上。
func TestTrailingSlashIsNotRedirected(t *testing.T) {
	original := gin.Mode()
	t.Cleanup(func() { gin.SetMode(original) })

	engine := newBaselineEngine(t)

	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			rec := do(t, engine, method, "/api/v1/ping/")

			if rec.Code >= 300 && rec.Code < 400 {
				t.Fatalf("status = %d，仍在做隐式重定向（Location = %q）",
					rec.Code, rec.Header().Get("Location"))
			}
		})
	}
}

// TestUnmatchedRequestPassesGlobalMiddleware 钉住未匹配请求仍走全部全局中间件。
//
// gin 的 allNoRoute/allNoMethod 由 combineHandlers 合成，本就包含全局中间件；
// 本用例把这一点变成断言，因为访问日志与指标是否覆盖 404/405 是可观测性的前提——
// 而 404/405 恰好是最需要留痕的两类。
func TestUnmatchedRequestPassesGlobalMiddleware(t *testing.T) {
	original := gin.Mode()
	t.Cleanup(func() { gin.SetMode(original) })

	engine := newBaselineEngine(t)

	cases := []struct {
		name   string
		method string
		path   string
	}{
		{name: "未匹配路由", method: http.MethodGet, path: "/api/v1/does-not-exist"},
		{name: "未匹配方法", method: http.MethodPost, path: "/api/v1/ping"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := do(t, engine, tc.method, tc.path)

			if rec.Header().Get(middleware.RequestIDHeader) == "" {
				t.Errorf("缺少 %s：RequestID 中间件未覆盖该路径", middleware.RequestIDHeader)
			}
			if rec.Header().Get("X-Content-Type-Options") == "" {
				t.Error("缺少 X-Content-Type-Options：SecurityHeaders 未覆盖该路径")
			}
		})
	}
}

// TestContextWithFallbackPropagatesDeadline 钉住 *gin.Context 当 context.Context 用时
// 不丢弃请求的 deadline。
//
// 这是母版责任：派生项目照 gin 社区惯例写 service.Do(c, …) 能通过编译，
// 而 ContextWithFallback 为 false 时路由级超时对这种写法完全失效，
// 且没有任何编译期或运行期信号。
func TestContextWithFallbackPropagatesDeadline(t *testing.T) {
	original := gin.Mode()
	t.Cleanup(func() { gin.SetMode(original) })

	var sawDeadline bool

	engine, err := newRouterForTest("test", Options{
		RegisterModules: func(v1 *gin.RouterGroup) {
			v1.GET("/deadline",
				middleware.Timeout(time.Second, 100*time.Millisecond),
				func(c *gin.Context) {
					// 关键：查询 c 本身，而不是 c.Request.Context()。
					_, sawDeadline = c.Deadline()
					c.Status(http.StatusOK)
				})
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	if rec := do(t, engine, http.MethodGet, "/api/v1/deadline"); rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}
	if !sawDeadline {
		t.Fatal("c.Deadline() 未报告路由超时：ContextWithFallback 未生效，" +
			"把 *gin.Context 当 context.Context 传给下游会静默丢弃超时")
	}
}
