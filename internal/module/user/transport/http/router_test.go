package http

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestRegisterMountsEveryRoute 钉住三条路由都注册上了。
//
// 断言的是 engine 实际注册出来的路由表，不是 Register 里写了几行——
// 一条写在错误分组里、或者路径拼错的路由，只有从路由表才看得出来。
func TestRegisterMountsEveryRoute(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	Register(engine.Group("/api/v1"), Deps{Auth: passthrough()})

	got := map[string]struct{}{}
	for _, r := range engine.Routes() {
		got[r.Method+" "+r.Path] = struct{}{}
	}

	for _, want := range []string{
		"GET /api/v1/user",
		"PUT /api/v1/user",
		"POST /api/v1/user/deactivate",
	} {
		if _, ok := got[want]; !ok {
			t.Errorf("route %q was not registered (got %v)", want, keys(got))
		}
	}
}

// TestEveryRouteGoesThroughAuth 是访问控制的**唯一**自动化保障。
//
// 描述符机制被移除后，"这条路由要不要鉴权"由代码位置决定：写在挂了
// deps.Auth 的组外面就是公开接口，而编译、vet 与其余测试都不会有任何异常。
//
// 这条测试用一个会记账的假认证中间件替换真的那个，然后逐条路由发请求，
// 确认它们都经过了它。新增路由若漏挂鉴权，这里会失败——这是本次改动之后
// 唯一还能自动发现该问题的地方，删掉它等于彻底放弃这条保障。
func TestEveryRouteGoesThroughAuth(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	var seen []string
	engine := gin.New()
	Register(engine.Group("/api/v1"), Deps{
		Auth: func(c *gin.Context) {
			seen = append(seen, c.Request.Method+" "+c.FullPath())
			// 直接中止：本测试只关心"认证中间件跑到了"，
			// 不需要让请求真的进 handler（那需要一个 service）。
			c.AbortWithStatus(http.StatusTeapot)
		},
	})

	routes := engine.Routes()
	if len(routes) == 0 {
		t.Fatal("no route was registered")
	}

	for _, r := range routes {
		req := httptest.NewRequest(r.Method, r.Path, nil)
		rec := httptest.NewRecorder()
		engine.ServeHTTP(rec, req)

		if rec.Code != http.StatusTeapot {
			t.Errorf("%s %s did not go through the auth middleware (status %d): "+
				"it is a public endpoint, and nothing else in the build would tell you",
				r.Method, r.Path, rec.Code)
		}
	}

	if len(seen) != len(routes) {
		t.Errorf("auth ran for %d of %d routes", len(seen), len(routes))
	}
}

// TestRateLimitIsAppliedWhenConfigured 钉住限流中间件确实被挂上。
func TestRateLimitIsAppliedWhenConfigured(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	var requested []string
	engine := gin.New()
	Register(engine.Group("/api/v1"), Deps{
		Auth: passthrough(),
		RateLimit: func(_ string, ids ...string) (preAuth, postAuth []gin.HandlerFunc) {
			requested = append(requested, ids...)
			return []gin.HandlerFunc{passthrough()}, []gin.HandlerFunc{passthrough()}
		},
	})

	// 两层限流：来源 + 身份。只挂一层的话，另一维度完全不设限。
	want := map[string]bool{"default.ip": false, "default.user": false}
	for _, id := range requested {
		if _, ok := want[id]; ok {
			want[id] = true
		}
	}
	for id, applied := range want {
		if !applied {
			t.Errorf("rate limit policy %q was never requested", id)
		}
	}
}

// TestIdentityScopedRateLimitRunsAfterAuth 钉住身份作用域限流排在认证之后。
//
// 缺陷：两条策略都挂在 deps.Auth 之前，于是身份限流执行时 context 里还没有身份，
// rateLimitSubject 取到 userID==0 返回错误，而中间件把"取不到主体"判为装配错误
// 并拒绝——配了 default.user 策略的路由，**每个请求都是 429**。
//
// 断言执行序而不是注册序：注册顺序正确但被别的中间件插队同样会坏。
func TestIdentityScopedRateLimitRunsAfterAuth(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	var order []string
	mark := func(name string) gin.HandlerFunc {
		return func(c *gin.Context) {
			order = append(order, name)
			c.Next()
		}
	}

	engine := gin.New()
	Register(engine.Group("/api/v1"), Deps{
		Service: nil,
		Auth:    mark("auth"),
		RateLimit: func(_ string, ids ...string) (preAuth, postAuth []gin.HandlerFunc) {
			for _, id := range ids {
				switch id {
				case "default.ip":
					preAuth = append(preAuth, mark("ratelimit.ip"))
				case "default.user":
					postAuth = append(postAuth, mark("ratelimit.user"))
				}
			}
			return preAuth, postAuth
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/user", nil)
	engine.ServeHTTP(httptest.NewRecorder(), req)

	idxOf := func(name string) int {
		return slices.Index(order, name)
	}
	ip, auth, user := idxOf("ratelimit.ip"), idxOf("auth"), idxOf("ratelimit.user")

	if ip < 0 || auth < 0 || user < 0 {
		t.Fatalf("执行序缺项: %v", order)
	}
	if ip > auth {
		t.Errorf("来源限流在认证之后执行（序列 %v）："+
			"被限流的请求会先付一次完整的令牌校验，而那正是攻击者想消耗的资源", order)
	}
	if user < auth {
		t.Errorf("身份限流在认证之前执行（序列 %v）："+
			"此时取不到身份，该路由的每个请求都会被判 429", order)
	}
}

// TestRegistrationSucceedsWithEmptyRateLimitGroups 钉住注册期与判定期的分工：
// stager 返回空分组时模块注册本身不 panic、路由照常产出——
// 引用解析不到的失败由装配层在全部注册完成后统一报出，不在注册现场炸。
func TestRegistrationSucceedsWithEmptyRateLimitGroups(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	Register(engine.Group("/api/v1"), Deps{
		Auth:      passthrough(),
		RateLimit: func(string, ...string) ([]gin.HandlerFunc, []gin.HandlerFunc) { return nil, nil },
	})

	if len(engine.Routes()) == 0 {
		t.Fatal("registration produced no routes when the stager returned empty groups")
	}
}

func passthrough() gin.HandlerFunc {
	return func(c *gin.Context) { c.Next() }
}

func keys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestBodyGovernanceRunsBeforeAuth 钉住请求体治理先于认证执行
// （http-request-contract spec「请求体治理位于认证之前」）。
//
// 为什么这条测试必须断言**执行序**：gin 的组中间件恒先于路由级中间件，
// 所以"每条路由都写了 JSONBody"看起来是对的，执行序却可能是反的——
// 认证挂在组上时，它永远先跑。注册序看不出这件事。
//
// 后果不是安全漏洞而是代价错配：一个 Content-Type 不符的请求要先完成
// 令牌解析、用户查库与撤销查 Redis，才被 415 拒绝。而 body 治理只读
// header 与 ContentLength，本该先拒。
func TestBodyGovernanceRunsBeforeAuth(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	var order []string
	mark := func(name string) gin.HandlerFunc {
		return func(c *gin.Context) {
			order = append(order, name)
			c.Next()
		}
	}

	engine := gin.New()
	// body 治理由模块自己声明，无法从外部注入 mark，因此用"认证是否已执行"
	// 反推：415 由 body 治理产生，若此时 auth 已在序列里，说明顺序反了。
	Register(engine.Group("/api/v1"), Deps{
		Auth: mark("auth"),
		RateLimit: func(_ string, ids ...string) (preAuth, postAuth []gin.HandlerFunc) {
			for _, id := range ids {
				switch id {
				case "default.ip":
					preAuth = append(preAuth, mark("ratelimit.ip"))
				case "default.user":
					postAuth = append(postAuth, mark("ratelimit.user"))
				}
			}
			return preAuth, postAuth
		},
	})

	// 写路由声明只接受 JSON，这里故意送 text/plain。
	req := httptest.NewRequest(http.MethodPut, "/api/v1/user", strings.NewReader("plain"))
	req.Header.Set("Content-Type", "text/plain")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnsupportedMediaType {
		t.Fatalf("status = %d, want 415（Content-Type 不符应由 body 治理拒绝）", rec.Code)
	}
	if slices.Contains(order, "auth") {
		t.Errorf("认证在请求体治理之前执行（序列 %v）："+
			"一个内容类型不符的请求先付掉了令牌解析、用户查库与撤销查询", order)
	}
	// 免身份限流仍必须在 body 治理之前：它挡的是尚未证明身份的流量，
	// 而 body 治理本身也是可被无身份流量触发的判定。
	if !slices.Contains(order, "ratelimit.ip") {
		t.Errorf("免身份限流未在请求体治理之前执行（序列 %v）", order)
	}
}

// TestGetRouteRejectsRequestBody 钉住只读路由显式声明不接受请求体
// （http-request-contract spec「请求体形态必须显式声明」三选一）。
//
// 静默放行的 body 意味着客户端以为自己传了参数，而服务端从未读过它——
// 那种不一致只有在业务结果不对时才会被发现。
func TestGetRouteRejectsRequestBody(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	Register(engine.Group("/api/v1"), Deps{
		Auth: func(c *gin.Context) { c.Next() },
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/user", strings.NewReader(`{"a":1}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400：只读路由应显式拒绝请求体", rec.Code)
	}
}
