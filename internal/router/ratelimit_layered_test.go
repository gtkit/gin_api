package router

import (
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/gtkit/gin-api/internal/pkg/ginc"
	"github.com/gtkit/gin-api/internal/router/routespec"
	"github.com/gtkit/gin-api/pkg/ratelimit"
)

// layeredEngine 建一条同时挂 IP 与 user 两层限流的路由。
func layeredEngine(t *testing.T, ipPolicy, userPolicy ratelimit.Policy) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	engine, err := InitRouterWithVersionedModules("test", VersionedModules{
		V1: []Module{rateLimitModule{routes: []routespec.RouteSpec{{
			Method:      http.MethodGet,
			OperationID: "opLayered", Path: "/layered",
			Handler:            func(c *gin.Context) { c.String(http.StatusOK, "ok") },
			AccessPolicy:       routespec.AccessPolicy{Mode: routespec.AccessAuthenticated},
			TimeoutPolicy:      cooperativeTimeoutPolicy(),
			ResponseShape:      routespec.ResponseShapeEmpty,
			RateLimitPolicyIDs: []string{ipPolicy.ID, userPolicy.ID},
		}}}},
	}, RouterOptions{
		AuthMiddleware: func(c *gin.Context) {
			ginc.SetUserID(c, 4242)
			c.Next()
		},
		RateLimits:    newRateLimitRegistry(t, ipPolicy, userPolicy),
		RateLimitKeys: newKeyBuilder(t),
	})
	if err != nil {
		t.Fatalf("InitRouterWithVersionedModules: %v", err)
	}
	return engine
}

// TestLayeredRateLimitsBothApply 是本次改动的核心证明：两层同时生效。
//
// 只挂一条时，另一维完全不设防——只限身份时一个来源可以用大量账号并发打满，
// 只限来源时就失去了按身份的精确限额。而"两条都声明了"不等于"两条都生效"：
// 链构造若只取其中一条，配置上完全看不出来。
func TestLayeredRateLimitsBothApply(t *testing.T) {
	t.Run("the user-scoped layer rejects while the ip layer still has room", func(t *testing.T) {
		// IP 层宽松、user 层收紧：被拒必然来自 user 层。
		engine := layeredEngine(t,
			ratelimit.Policy{
				ID: "layer.ip.loose", Rate: 0.01, Burst: 100,
				Scope: ratelimit.ScopeIP, Store: ratelimit.StoreLocal,
				FailureMode: ratelimit.FailureClosed, MaxKeys: 100,
			},
			ratelimit.Policy{
				ID: "layer.user.tight", Rate: 0.01, Burst: 1,
				Scope: ratelimit.ScopeUser, Store: ratelimit.StoreLocal,
				FailureMode: ratelimit.FailureClosed, MaxKeys: 100,
			})

		if got := layeredCall(engine, "192.0.2.20:1111"); got != http.StatusOK {
			t.Fatalf("first call = %d, want 200", got)
		}
		if got := layeredCall(engine, "192.0.2.20:1111"); got != http.StatusTooManyRequests {
			t.Fatalf("second call = %d, want 429 from the user layer", got)
		}
	})

	t.Run("the ip-scoped layer rejects while the user layer still has room", func(t *testing.T) {
		// 反过来：IP 层收紧、user 层宽松，且换 IP 但同一用户。
		// 被拒必然来自 IP 层——若链上只挂了 user 层，这里会全部放行。
		engine := layeredEngine(t,
			ratelimit.Policy{
				ID: "layer.ip.tight", Rate: 0.01, Burst: 1,
				Scope: ratelimit.ScopeIP, Store: ratelimit.StoreLocal,
				FailureMode: ratelimit.FailureClosed, MaxKeys: 100,
			},
			ratelimit.Policy{
				ID: "layer.user.loose", Rate: 0.01, Burst: 100,
				Scope: ratelimit.ScopeUser, Store: ratelimit.StoreLocal,
				FailureMode: ratelimit.FailureClosed, MaxKeys: 100,
			})

		if got := layeredCall(engine, "192.0.2.21:1111"); got != http.StatusOK {
			t.Fatalf("first call = %d, want 200", got)
		}
		if got := layeredCall(engine, "192.0.2.21:2222"); got != http.StatusTooManyRequests {
			t.Fatalf("second call from the same ip = %d, want 429 from the ip layer", got)
		}
	})
}

// TestLayeredRateLimitHeadersShowTheTightest 钉住响应头取最紧的一条。
//
// 报出较宽松的那条会误导客户端：它据此认为还有余量，继续发送并被另一条
// 策略拒绝，退避逻辑因此失效。
//
// 两个方向都测：结果必须不依赖哪一层先执行——链上先执行的看不到后面的结果，
// 若实现成"后写覆盖先写"，两种顺序会给出不同答案。
func TestLayeredRateLimitHeadersShowTheTightest(t *testing.T) {
	tests := []struct {
		name       string
		ipBurst    int
		userBurst  int
		wantAtMost int
	}{
		{name: "the tighter layer runs first", ipBurst: 2, userBurst: 50, wantAtMost: 1},
		{name: "the tighter layer runs last", ipBurst: 50, userBurst: 2, wantAtMost: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			engine := layeredEngine(t,
				ratelimit.Policy{
					ID: "hdr.ip", Rate: 0.01, Burst: tt.ipBurst,
					Scope: ratelimit.ScopeIP, Store: ratelimit.StoreLocal,
					FailureMode: ratelimit.FailureClosed, MaxKeys: 100,
				},
				ratelimit.Policy{
					ID: "hdr.user", Rate: 0.01, Burst: tt.userBurst,
					Scope: ratelimit.ScopeUser, Store: ratelimit.StoreLocal,
					FailureMode: ratelimit.FailureClosed, MaxKeys: 100,
				})

			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/api/v1/layered", nil)
			req.RemoteAddr = "192.0.2.30:1234"
			engine.ServeHTTP(w, req)

			raw := w.Header().Get("X-Ratelimit-Remaining")
			if raw == "" {
				t.Fatal("no remaining header was written")
			}
			remaining, err := strconv.Atoi(raw)
			if err != nil {
				t.Fatalf("remaining header %q is not a number", raw)
			}
			if remaining > tt.wantAtMost {
				t.Fatalf("remaining = %d, want at most %d: the header must reflect the tightest "+
					"layer, otherwise clients back off against the wrong budget",
					remaining, tt.wantAtMost)
			}
		})
	}
}

func layeredCall(engine *gin.Engine, remoteAddr string) int {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/layered", nil)
	req.RemoteAddr = remoteAddr
	engine.ServeHTTP(w, req)
	return w.Code
}
