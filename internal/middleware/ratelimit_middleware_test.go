package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/gtkit/gin-api/internal/pkg/ginc"
	"github.com/gtkit/gin-api/pkg/ratelimit"
)

// fakeLimiter 返回预设判定，让中间件的翻译逻辑可以被单独验证。
//
// 用假执行体而不是真后端：这里要验的是"判定 → HTTP 响应"这一段，
// 而真后端（令牌桶、Redis、降级）在 pkg/ratelimit 自己的测试里已有覆盖。
type fakeLimiter struct {
	policy   ratelimit.Policy
	decision ratelimit.Decision
	err      error
	// gotKey 记录送达的键，用于确认中间件确实调用了执行体。
	gotKey string
}

func (f *fakeLimiter) Check(_ context.Context, key string) (ratelimit.Decision, error) {
	f.gotKey = key
	return f.decision, f.err
}

func (f *fakeLimiter) Policy() ratelimit.Policy { return f.policy }
func (f *fakeLimiter) Close()                   {}

// testPolicy 造一条合法策略。
func testPolicy(id string, scope ratelimit.Scope) ratelimit.Policy {
	return ratelimit.Policy{
		ID: id, Rate: 1, Burst: 10, Scope: scope,
		Store: ratelimit.StoreLocal, FailureMode: ratelimit.FailureOpen, MaxKeys: 100,
	}
}

func testKeyBuilder(t *testing.T) *ratelimit.KeyBuilder {
	t.Helper()
	keys, err := ratelimit.NewKeyBuilder("test-secret-with-enough-length-000000", "test")
	if err != nil {
		t.Fatalf("NewKeyBuilder: %v", err)
	}
	return keys
}

// serveRateLimited 挂上给定的限流中间件跑一次请求。
//
// recorder 传 nil：中间件的 nil-safe 契约由 metrics 包的 TestNilRecorderIsSafe
// 反证，这里顺带走一遍那条路径——母版默认装配里 recorder 非 nil，但注释声明了
// 它可为 nil，一条没人走过的声明分支就是一条没有判据的声明。
func serveRateLimited(t *testing.T, mws ...gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	for _, mw := range mws {
		engine.Use(mw)
	}
	engine.GET("/x", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.RemoteAddr = "192.0.2.10:1234"
	engine.ServeHTTP(rec, req)
	return rec
}

// TestRateLimitAllowedRequestStillCarriesQuotaHeaders 钉住"放行也带限额头"
// （rate-limiting spec「限流响应使用 429 且 Retry-After 向上取整至整秒」）。
//
// 只在被拒时写头的话，客户端只能靠撞 429 试错来发现限额——而提前退避
// 才是这组头存在的意义。
func TestRateLimitAllowedRequestStillCarriesQuotaHeaders(t *testing.T) {
	t.Parallel()

	limiter := &fakeLimiter{
		policy:   testPolicy("p.allow", ratelimit.ScopeIP),
		decision: ratelimit.Decision{Outcome: ratelimit.OutcomeAllowed, Limit: 10, Remaining: 7},
	}

	rec := serveRateLimited(t, RateLimit(limiter, testKeyBuilder(t), nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if got := rec.Header().Get("X-Ratelimit-Limit"); got != "10" {
		t.Errorf("X-Ratelimit-Limit = %q, want 10", got)
	}
	if got := rec.Header().Get("X-Ratelimit-Remaining"); got != "7" {
		t.Errorf("X-Ratelimit-Remaining = %q, want 7", got)
	}
	if limiter.gotKey == "" {
		t.Error("中间件未调用执行体：键为空")
	}
}

// TestRateLimitRejectionUsesTooManyRequestsWithLegalRetryAfter 钉住拒绝路径。
//
// 三件事各有独立的失效形态：
//   - 状态码必须是 429。403 让客户端放弃一次本该重试的请求，503 让它以为是服务端故障。
//   - Retry-After 必须是**整数秒**。小数或毫秒是非法头。
//   - 不足一秒必须向上取整为 1，不能截断成 0——`Retry-After: 0` 既非法，
//     语义上又等于"立刻重试"，恰好是限流最不希望客户端做的事。
func TestRateLimitRejectionUsesTooManyRequestsWithLegalRetryAfter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		retryAfter  time.Duration
		wantSeconds int
	}{
		{name: "不足一秒向上取整为 1", retryAfter: 120 * time.Millisecond, wantSeconds: 1},
		{name: "零值也不得输出 0", retryAfter: 0, wantSeconds: 1},
		{name: "整秒保持", retryAfter: 3 * time.Second, wantSeconds: 3},
		{name: "带小数向上取整", retryAfter: 2500 * time.Millisecond, wantSeconds: 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			limiter := &fakeLimiter{
				policy: testPolicy("p.deny", ratelimit.ScopeIP),
				decision: ratelimit.Decision{
					Outcome: ratelimit.OutcomeRateLimited, Limit: 10, Remaining: 0,
					RetryAfter: tt.retryAfter,
				},
			}

			rec := serveRateLimited(t, RateLimit(limiter, testKeyBuilder(t), nil))

			if rec.Code != http.StatusTooManyRequests {
				t.Fatalf("status = %d, want 429", rec.Code)
			}
			raw := rec.Header().Get("Retry-After")
			got, err := strconv.Atoi(raw)
			if err != nil {
				t.Fatalf("Retry-After = %q 不是整数秒（HTTP 要求 delta-seconds 整数）", raw)
			}
			if got != tt.wantSeconds {
				t.Fatalf("Retry-After = %d, want %d", got, tt.wantSeconds)
			}
			if got < 1 {
				t.Fatalf("Retry-After = %d，下限必须为 1：0 等于让客户端立刻重试", got)
			}
		})
	}
}

// TestRateLimitHeadersKeepTheTightestRegardlessOfOrder 钉住"响应头反映最紧的一条策略，
// 且结果不依赖执行顺序"（rate-limiting spec「限流响应头反映最紧的一条策略」）。
//
// 响应头只有一份，而多条策略分布在链的不同位置——先执行的看不到后执行的结果，
// 无法先聚合再决定。报出较宽松的那条会误导客户端：它据此认为还有余量，
// 继续发送并被另一条策略拒绝，退避逻辑因此失效。
//
// 两种顺序都测：只测一种的话，一个"后者无条件覆盖前者"的实现会在其中一种下通过。
func TestRateLimitHeadersKeepTheTightestRegardlessOfOrder(t *testing.T) {
	t.Parallel()

	loose := func() gin.HandlerFunc {
		return RateLimit(&fakeLimiter{
			policy:   testPolicy("p.loose", ratelimit.ScopeIP),
			decision: ratelimit.Decision{Outcome: ratelimit.OutcomeAllowed, Limit: 100, Remaining: 90},
		}, testKeyBuilder(t), nil)
	}
	tight := func() gin.HandlerFunc {
		return RateLimit(&fakeLimiter{
			policy:   testPolicy("p.tight", ratelimit.ScopeIP),
			decision: ratelimit.Decision{Outcome: ratelimit.OutcomeAllowed, Limit: 10, Remaining: 2},
		}, testKeyBuilder(t), nil)
	}

	for _, tt := range []struct {
		name string
		mws  []gin.HandlerFunc
	}{
		{name: "宽松在前", mws: []gin.HandlerFunc{loose(), tight()}},
		{name: "紧在前", mws: []gin.HandlerFunc{tight(), loose()}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			rec := serveRateLimited(t, tt.mws...)

			if got := rec.Header().Get("X-Ratelimit-Remaining"); got != "2" {
				t.Fatalf("X-Ratelimit-Remaining = %q, want 2（最紧的那条）", got)
			}
			if got := rec.Header().Get("X-Ratelimit-Limit"); got != "10" {
				t.Fatalf("X-Ratelimit-Limit = %q, want 10（与最紧的剩余量同源）", got)
			}
		})
	}
}

// TestRateLimitUserScopeWithoutIdentityRejects 钉住取不到主体时**拒绝**而不是放行。
//
// user 作用域的策略挂在公开路由上属装配错误。此时放行等于这条路由完全没有限流，
// 而现象是"限流配了但不起作用"——没有任何报错。把 0 当成合法主体更糟：
// 所有匿名请求共用同一个桶，一个用户就能把它打满，而看起来限流是生效的。
func TestRateLimitUserScopeWithoutIdentityRejects(t *testing.T) {
	t.Parallel()

	limiter := &fakeLimiter{
		policy: testPolicy("p.user", ratelimit.ScopeUser),
		// 判定不会被用到——主体解析就该先失败。给一个放行判定，
		// 这样若实现改成"解析失败也继续查"，测试会因为 200 而失败。
		decision: ratelimit.Decision{Outcome: ratelimit.OutcomeAllowed, Limit: 10, Remaining: 9},
	}

	rec := serveRateLimited(t, RateLimit(limiter, testKeyBuilder(t), nil))

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429：user 作用域取不到身份时唯一安全的行为是拒绝", rec.Code)
	}
	if limiter.gotKey != "" {
		t.Errorf("主体解析失败后不该再查执行体，却送达了键 %q", limiter.gotKey)
	}
}

// TestRateLimitUserScopeWithIdentityPasses 是上一条的反向锚。
//
// 缺了它，一个"user 作用域一律拒绝"的实现也能让上面的用例通过——
// 而那会把所有带身份的请求全部挡掉。
func TestRateLimitUserScopeWithIdentityPasses(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	limiter := &fakeLimiter{
		policy:   testPolicy("p.user", ratelimit.ScopeUser),
		decision: ratelimit.Decision{Outcome: ratelimit.OutcomeAllowed, Limit: 10, Remaining: 9},
	}

	engine := gin.New()
	// 在限流之前放置身份，模拟认证已通过的链上位置。
	engine.Use(func(c *gin.Context) {
		ginc.SetUserID(c, 4242)
		c.Next()
	})
	engine.Use(RateLimit(limiter, testKeyBuilder(t), nil))
	engine.GET("/x", func(c *gin.Context) { c.String(http.StatusOK, "ok") })

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	req.RemoteAddr = "192.0.2.10:1234"
	engine.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200：带身份的 user 作用域请求应被放行", rec.Code)
	}
	if limiter.gotKey == "" {
		t.Error("带身份时应查执行体，却没有送达键")
	}
}

// TestRateLimitBackendErrorFollowsTheDecision 钉住后端故障时以判定为准。
//
// 执行体已按 FailureMode 把故障翻译成最终判定，中间件只负责留痕。
// 若中间件自己再决定一次（比如见 err 就拒），fail_open 策略在后端抖动时
// 会开始拒绝请求——而配置明确要求的是放行。
func TestRateLimitBackendErrorFollowsTheDecision(t *testing.T) {
	t.Parallel()

	limiter := &fakeLimiter{
		policy: testPolicy("p.degraded", ratelimit.ScopeIP),
		decision: ratelimit.Decision{
			Outcome: ratelimit.OutcomeAllowed, Limit: 10, Remaining: 5, Degraded: true,
		},
		err: context.DeadlineExceeded,
	}

	rec := serveRateLimited(t, RateLimit(limiter, testKeyBuilder(t), nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200：判定为放行时后端错误不改变结果", rec.Code)
	}
}

// TestRateLimitRejectsZeroValueDecision 钉住零值判定落在拒绝方向
// （rate-limiting spec「判定结果的零值必须落在拒绝方向」）。
//
// Go 的零值无处不在：结构体字面量漏填、map 取不到键都会产出它。
// 让零值判定等于放行，意味着任何一次疏忽都朝不设防的方向失败，
// 而这类疏忽在编译期与测试里都不显形。
func TestRateLimitRejectsZeroValueDecision(t *testing.T) {
	t.Parallel()

	limiter := &fakeLimiter{
		policy:   testPolicy("p.zero", ratelimit.ScopeIP),
		decision: ratelimit.Decision{}, // 未赋值
	}

	rec := serveRateLimited(t, RateLimit(limiter, testKeyBuilder(t), nil))

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429：未赋值的判定不得被当成放行", rec.Code)
	}
}

// TestRateLimitGlobalScopeNeedsNoIdentity 覆盖 global 作用域。
//
// global 的主体是"整条路由"，不依赖 IP 也不依赖身份，因此匿名请求必须能通过
// 主体解析。若它被误并进需要身份的分支，一条全局限额会把所有匿名请求拒掉。
func TestRateLimitGlobalScopeNeedsNoIdentity(t *testing.T) {
	t.Parallel()

	limiter := &fakeLimiter{
		policy:   testPolicy("p.global", ratelimit.ScopeGlobal),
		decision: ratelimit.Decision{Outcome: ratelimit.OutcomeAllowed, Limit: 10, Remaining: 9},
	}

	rec := serveRateLimited(t, RateLimit(limiter, testKeyBuilder(t), nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200：global 作用域不需要身份", rec.Code)
	}
	if limiter.gotKey == "" {
		t.Error("global 作用域应完成主体解析并查执行体")
	}
}

// TestRateLimitUnspecifiedScopeRejects 钉住未指定作用域落在拒绝方向。
//
// Scope 的零值不是一个合法作用域。放行等于这条路由完全不设防，而现象是
// "限流配了但不起作用"——与 user 作用域取不到身份是同一类失效，
// 兜底方向必须一致。
func TestRateLimitUnspecifiedScopeRejects(t *testing.T) {
	t.Parallel()

	policy := testPolicy("p.noscope", ratelimit.ScopeIP)
	policy.Scope = 0 // 未指定

	limiter := &fakeLimiter{
		policy:   policy,
		decision: ratelimit.Decision{Outcome: ratelimit.OutcomeAllowed, Limit: 10, Remaining: 9},
	}

	rec := serveRateLimited(t, RateLimit(limiter, testKeyBuilder(t), nil))

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429：未指定作用域不得被当成可放行", rec.Code)
	}
	if limiter.gotKey != "" {
		t.Errorf("作用域非法时不该查执行体，却送达了键 %q", limiter.gotKey)
	}
}

// TestRateLimitPanicsOnMissingDependencies 钉住装配期缺依赖立即 panic。
//
// 这是装配期契约：一个没有执行体或键构造器的限流中间件无法完成任何判定，
// 而它若静默放行，这条路由看起来"挂了限流"却完全不设防。装配期 panic
// 让它在启动时就暴露，而不是在被打穿时。
func TestRateLimitPanicsOnMissingDependencies(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		limiter ratelimit.Limiter
		keys    *ratelimit.KeyBuilder
	}{
		{name: "缺执行体", limiter: nil, keys: testKeyBuilder(t)},
		{name: "缺键构造器", limiter: &fakeLimiter{policy: testPolicy("p", ratelimit.ScopeIP)}, keys: nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			defer func() {
				if recover() == nil {
					t.Fatal("装配期缺依赖必须 panic，否则这条路由看起来挂了限流却完全不设防")
				}
			}()
			RateLimit(tt.limiter, tt.keys, nil)
		})
	}
}
