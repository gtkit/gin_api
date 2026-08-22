package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestSecurityHeadersValuesAreExact 钉住五个安全头的**取值**。
//
// 为什么这个测试之前不存在也没人发现：`internal/router` 的
// `TestGlobalMiddlewareOrderIsExact` 已经锁住了 SecurityHeaders 在链上的**位置**，
// router 的其他测试也会真实发请求把它执行一遍——所以"挂了没有""排在哪"都有判据。
// 缺的是**写了什么**：把 headerFrameOptions 从 DENY 改成 SAMEORIGIN、把 CSP 清空、
// 或者删掉其中一个 h.Set，全仓测试都不会变红。
//
// 这些取值是安全决策而非风格选择（包内注释逐条写了理由），因此必须逐项断言，
// 不能只断言"header 非空"。
func TestSecurityHeadersValuesAreExact(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	rec := serveWithSecurityHeaders(t, func(c *gin.Context) {
		// 写 body：安全头必须在此之前就已设置。body 一开始写，响应头就发出了，
		// 之后再 Set 不生效且**不报错**——那正是"配了但线上没有"的成因。
		c.String(http.StatusOK, "payload")
	})

	tests := []struct {
		header string
		want   string
		why    string
	}{
		{
			header: "X-Content-Type-Options",
			want:   "nosniff",
			why:    "关闭 MIME 嗅探；不关时 text/plain 响应仍可能被当作脚本执行",
		},
		{
			header: "X-Frame-Options",
			want:   "DENY",
			why:    "JSON API 无需被嵌套，取最严格值挡点击劫持",
		},
		{
			header: "Referrer-Policy",
			want:   "no-referrer",
			why:    "默认策略会把含资源 ID 的完整 URL 发给第三方",
		},
		{
			header: "Content-Security-Policy",
			want:   "default-src 'none'; frame-ancestors 'none'; sandbox",
			why:    "万一响应被当作 HTML 渲染，其中脚本也不得执行",
		},
		{
			header: "Permissions-Policy",
			want:   "geolocation=(), microphone=(), camera=(), payment=()",
			why:    "关闭浏览器高权限能力",
		},
	}

	for _, tt := range tests {
		t.Run(tt.header, func(t *testing.T) {
			t.Parallel()
			if got := rec.Header().Get(tt.header); got != tt.want {
				t.Fatalf("%s = %q, want %q（%s）", tt.header, got, tt.want, tt.why)
			}
		})
	}
}

// TestSecurityHeadersOmitHSTS 钉住"不发 HSTS"这条显式决定。
//
// 它不是漏了：HSTS 由 TLS 终止层负责。在明文 HTTP 后端发 HSTS，要么无效，
// 要么在某些拓扑下把一个错误的策略钉进浏览器一年——而那是无法远程撤销的。
// 没有这条断言，"顺手补上 HSTS" 会被当成一次改进合入。
func TestSecurityHeadersOmitHSTS(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	rec := serveWithSecurityHeaders(t, func(c *gin.Context) { c.Status(http.StatusOK) })

	if got := rec.Header().Get("Strict-Transport-Security"); got != "" {
		t.Fatalf("Strict-Transport-Security = %q, want absent: HSTS 由 TLS 终止层负责，"+
			"明文后端发它要么无效、要么把错误策略钉进浏览器一年", got)
	}
}

// TestSecurityHeadersSurviveAbortedRequests 钉住被中断的请求同样带安全头。
//
// 4xx/5xx 的响应体一样会被浏览器渲染，因此它们同样需要这些头。中间件在
// c.Next() **之前**设置就是为了覆盖这条路径；改成之后设置会让所有 abort 掉的
// 响应失去保护，而正常路径看起来毫无异常。
func TestSecurityHeadersSurviveAbortedRequests(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	rec := serveWithSecurityHeaders(t, func(c *gin.Context) {
		c.AbortWithStatus(http.StatusForbidden)
	})

	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("被中断的响应缺少安全头: X-Content-Type-Options = %q", got)
	}
	if got := rec.Header().Get("Content-Security-Policy"); got == "" {
		t.Fatal("被中断的响应缺少 Content-Security-Policy")
	}
}

// serveWithSecurityHeaders 挂上 SecurityHeaders 跑一次请求。
func serveWithSecurityHeaders(t *testing.T, handler gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()

	engine := gin.New()
	engine.Use(SecurityHeaders())
	engine.GET("/x", handler)

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/x", nil))
	return rec
}
