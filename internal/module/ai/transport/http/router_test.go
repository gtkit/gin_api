package http

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/gtkit/gin-api/internal/module/ai/application"
)

// wantRoutes 是本模块应当注册的全部路由。
var wantRoutes = []struct{ method, path string }{
	{http.MethodPost, "/api/v1/ai/chat"},
	{http.MethodPost, "/api/v1/ai/chat/stream"},
	{http.MethodGet, "/api/v1/ai/providers"},
	{http.MethodGet, "/api/v1/ai/conversations"},
	{http.MethodGet, "/api/v1/ai/conversations/:id"},
	{http.MethodPatch, "/api/v1/ai/conversations/:id"},
	{http.MethodDelete, "/api/v1/ai/conversations/:id"},
}

// TestRegisterMountsEveryRoute 钉住路由清单不缺项。
func TestRegisterMountsEveryRoute(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	Register(engine.Group("/api/v1"), Deps{Auth: func(c *gin.Context) { c.Next() }})

	got := map[string]struct{}{}
	for _, r := range engine.Routes() {
		got[r.Method+" "+r.Path] = struct{}{}
	}
	for _, want := range wantRoutes {
		if _, ok := got[want.method+" "+want.path]; !ok {
			t.Errorf("route %s %s was not registered", want.method, want.path)
		}
	}
}

// TestEveryRouteGoesThroughAuth 是本模块访问控制的**唯一**自动化保障。
//
// 访问控制由代码位置表达（docs/architecture.md）：新增路由时写在认证中间件
// 覆盖不到的位置，它就是公开接口，而编译、go vet 与其余全部测试都不会有任何
// 异常。对 AI 端点这尤其昂贵——一条漏鉴权的对话路由等于把公司的模型账单
// 开放给互联网。
//
// 逐条路由发真实请求，而不是检查中间件链的长度：后者会被"挂了一个不是认证的
// 中间件"骗过去。
func TestEveryRouteGoesThroughAuth(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	for _, route := range wantRoutes {
		var reached []string
		engine := gin.New()
		Register(engine.Group("/api/v1"), Deps{
			Auth: func(c *gin.Context) {
				reached = append(reached, c.FullPath())
				c.AbortWithStatus(http.StatusTeapot)
			},
		})

		path := strings.Replace(route.path, ":id", "1", 1)
		var body *strings.Reader
		var req *http.Request
		if route.method == http.MethodGet || route.method == http.MethodDelete {
			req = httptest.NewRequest(route.method, path, nil)
		} else {
			body = strings.NewReader(`{"message":"x","title":"x"}`)
			req = httptest.NewRequest(route.method, path, body)
			req.Header.Set("Content-Type", "application/json")
		}

		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)

		if rec.Code != http.StatusTeapot {
			t.Errorf("%s %s did not go through the auth middleware (status %d): "+
				"it is a public endpoint, and nothing else would tell you",
				route.method, route.path, rec.Code)
		}
		if len(reached) == 0 {
			t.Errorf("%s %s never reached the auth middleware", route.method, route.path)
		}
	}
}

// TestWriteRoutesRejectANonJSONBody 钉住请求体治理排在认证之前。
//
// 顺序要紧：认证先跑的话，一个 Content-Type 不符的请求要先付掉令牌解析、
// 用户查库与撤销查 Redis 才被 415 拒。
func TestWriteRoutesRejectANonJSONBody(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	var authCalls int
	engine := gin.New()
	Register(engine.Group("/api/v1"), Deps{
		Auth: func(c *gin.Context) { authCalls++; c.Next() },
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/ai/chat",
		strings.NewReader("message=x"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415 (body %s)", rec.Code, rec.Body.String())
	}
	if authCalls != 0 {
		t.Error("authentication ran before the content-type was rejected: " +
			"a malformed request would still cost a token parse and a database round trip")
	}
}

// TestRateLimitStagerIsConsulted 钉住模块确实声明了限流策略。
//
// 声明缺失不会有任何编译或运行期信号——那条路由只是不限流，而每个放行的
// AI 请求都是一笔真实的上游账单。
func TestRateLimitStagerIsConsulted(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	var route string
	var ids []string
	engine := gin.New()
	Register(engine.Group("/api/v1"), Deps{
		Auth: func(c *gin.Context) { c.Next() },
		RateLimit: func(r string, policyIDs ...string) ([]gin.HandlerFunc, []gin.HandlerFunc) {
			route, ids = r, policyIDs
			return nil, nil
		},
	})
	_ = engine

	if route != "/ai" {
		t.Errorf("rate limit was requested for route %q, want %q", route, "/ai")
	}
	// 两层都要：只限身份时一个来源可以用大量账号并发打满；
	// 只限来源时就失去了按账号的精确限额。
	want := map[string]bool{"default.ip": false, "default.user": false}
	for _, id := range ids {
		if _, ok := want[id]; ok {
			want[id] = true
		}
	}
	for id, seen := range want {
		if !seen {
			t.Errorf("policy %q is not referenced by the ai routes", id)
		}
	}
}

// TestStreamRouteIsNotBoundByTheChatDeadline 钉住流式路由没被挂上超时中间件。
//
// 挂上的后果是正常生成中的长流被协作式 deadline 掐断，客户端只看到回答
// 戛然而止——没有错误、没有日志线索。这条很容易在"给所有路由统一加超时"
// 的重构里被无意引入（例如把 Timeout 从 chat 子组上移到 write 组）。
//
// 判据是**行为**：给一个远小于生成耗时的非流式预算，
//   - /chat        必须被它掐断（证明这个预算确实小到能起作用）；
//   - /chat/stream 必须照常跑完（证明它不受那个预算约束）。
//
// 只断言后者是不够的：一个被写死成"永不超时"的实现也能让它通过。
func TestStreamRouteIsNotBoundByTheChatDeadline(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	newEngineWithBudget := func(llm *stubLLM) *gin.Engine {
		t.Helper()
		return newEngineWith(t, &stubStore{}, llm, Timeouts{
			Request:     30 * time.Millisecond,
			StreamTotal: 5 * time.Second,
			Heartbeat:   time.Second,
		})
	}

	slow := func() *stubLLM {
		return &stubLLM{
			deltas: []string{"慢", "但是完整"},
			delay:  120 * time.Millisecond,
			reply:  application.Reply{Provider: "deepseek", Model: "deepseek-chat", FinishReason: "stop"},
		}
	}

	rec := postJSON(t, newEngineWithBudget(slow()), "/api/v1/ai/chat", `{"message":"问题"}`)
	if rec.Code == http.StatusOK {
		t.Fatalf("the 30ms budget did not cut off a 120ms non-streaming call (status %d): "+
			"this test cannot tell whether the stream route is exempt", rec.Code)
	}

	rec = postJSON(t, newEngineWithBudget(slow()), "/api/v1/ai/chat/stream", `{"message":"问题"}`)
	body := rec.Body.String()
	if !strings.Contains(body, "event: done") {
		t.Errorf("the stream was cut off by the non-streaming deadline:\n%s", body)
	}
}
