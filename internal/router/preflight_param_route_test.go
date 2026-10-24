package router

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// newParamRouteEngine 注册一条带路径参数的路由，并开启 CORS。
func newParamRouteEngine(t *testing.T) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	engine, err := newRouterForTest(t, "test", Options{
		AllowedOrigins: []string{"https://example.com"},
		RegisterModules: func(v1 *gin.RouterGroup) {
			v1.GET("/thing/:id", func(c *gin.Context) { c.Status(http.StatusOK) })
			v1.PUT("/thing/:id", func(c *gin.Context) { c.Status(http.StatusOK) })
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return engine
}

func doPreflight(t *testing.T, engine *gin.Engine, path, method string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodOptions, path, nil)
	req.Header.Set("Origin", "https://example.com")
	req.Header.Set("Access-Control-Request-Method", method)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

// TestPreflightOnParameterizedRouteIsNotMistakenForMissingPath 是本 change 的反证测试。
//
// 缺陷形态：可用方法集此前由应用层用 `r.Path == path` 推导——左边是路由**模式**
// （`/api/v1/thing/:id`），右边是**具体路径**（`/api/v1/thing/42`）。模式含参数时
// 永不相等，于是返回空串，而空串的约定含义是"路径不存在"，预检因此回 404。
//
// 后果比"少一个头"严重得多：浏览器据 404 直接拦下请求，而同一路径的实际请求
// 返回 200。开发者看到"CORS 失败"、用 curl 一切正常，排查方向被引向错误的地方。
//
// 开启 CORS 的参数路由必须使用实际路径匹配，不能只比较注册时的模式字符串。
func TestPreflightOnParameterizedRouteIsNotMistakenForMissingPath(t *testing.T) {
	t.Parallel()

	engine := newParamRouteEngine(t)

	rec := doPreflight(t, engine, "/api/v1/thing/42", http.MethodGet)
	if rec.Code == http.StatusNotFound {
		t.Fatalf("带参路由的预检被判为路径不存在（404）：" +
			"而 GET /api/v1/thing/42 的实际请求是可用的，浏览器会因此拦下一个本该成功的请求")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
}

// TestPreflightOnParameterizedRouteRejectsUnregisteredMethod 反向锚。
//
// 缺了它，一个"带参路由一律放行"的实现也能让上一条通过——而那会让浏览器
// 放行一个随后必然 405 的请求。
func TestPreflightOnParameterizedRouteRejectsUnregisteredMethod(t *testing.T) {
	t.Parallel()

	engine := newParamRouteEngine(t)

	rec := doPreflight(t, engine, "/api/v1/thing/42", http.MethodDelete)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("status = %d, want 405（DELETE 未注册）", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); allow == "" {
		t.Fatal("405 预检必须带 Allow，否则调用方不知道该路径支持什么")
	}
}

// TestPreflightOnMissingPathStillReturnsNotFound 守住"路径不存在"这条判定没有被一起改掉。
func TestPreflightOnMissingPathStillReturnsNotFound(t *testing.T) {
	t.Parallel()

	engine := newParamRouteEngine(t)

	rec := doPreflight(t, engine, "/api/v1/nope/42", http.MethodGet)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404：预检不该假称一个不存在的路径可用", rec.Code)
	}
}

// TestPreflightAndDirect405ReportTheSameAllow 钉住两条 405 路径同源。
//
// 两处各自推导时报出的 Allow 值会不一致（此前一处按 gin 的方法树序、
// 另一处按字典序），客户端按哪一个退避都可能错。
func TestPreflightAndDirect405ReportTheSameAllow(t *testing.T) {
	t.Parallel()

	engine := newParamRouteEngine(t)

	preflight := doPreflight(t, engine, "/api/v1/thing/42", http.MethodDelete)

	direct := httptest.NewRecorder()
	engine.ServeHTTP(direct, httptest.NewRequest(http.MethodDelete, "/api/v1/thing/42", nil))

	if direct.Code != http.StatusMethodNotAllowed {
		t.Fatalf("直接请求 status = %d, want 405", direct.Code)
	}
	if got, want := preflight.Header().Get("Allow"), direct.Header().Get("Allow"); got != want {
		t.Fatalf("预检的 Allow = %q，直接 405 的 Allow = %q，两者必须同源", got, want)
	}
}

// TestEmptyRemoteIPHeadersDoesNotFallBackToFrameworkDefaults 是转发头这条分叉的反证测试。
//
// 缺陷形态：装配此前写成 `if len(opt.RemoteIPHeaders) > 0 { engine.RemoteIPHeaders = ... }`，
// 于是留空时 gin 的默认值（["X-Forwarded-For", "X-Real-IP"]）继续生效。而随仓样例
// config/env.yml.example 把空值注释为"不配置=不从任何 header 取 IP"，
// trusted-proxy-client-ip spec 更明确要求 MUST NOT 依赖框架默认值。
//
// 客户端 IP 是审计事实与限流主体的来源。运维据注释判断它只来自 TCP 对端，
// 实际却来自请求头——而这类分叉不会有任何报错。
//
// 关键是必须让请求"来自可信代理"：gin 只在 trusted && ForwardedByClientIP &&
// RemoteIPHeaders != nil 时才读转发头。不配 trusted_proxies 的话，无论转发头
// 怎么设都不会被采信，这个测试就会假绿。
func TestEmptyRemoteIPHeadersDoesNotFallBackToFrameworkDefaults(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	const proxyIP = "192.0.2.10"

	var seenIP string
	engine, err := newRouterForTest(t, "test", Options{
		// 把请求来源声明为可信代理，否则转发头根本不会进入取值路径。
		TrustedProxies: []string{proxyIP + "/32"},
		// 显式留空：这正是随仓样例的出厂值。
		RemoteIPHeaders: nil,
		RegisterModules: func(v1 *gin.RouterGroup) {
			v1.GET("/ip", func(c *gin.Context) {
				seenIP = c.ClientIP()
				c.Status(http.StatusOK)
			})
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/ip", nil)
	req.RemoteAddr = proxyIP + ":34567"
	// gin 的默认转发头，两个都设上。
	req.Header.Set("X-Forwarded-For", "203.0.113.9")
	req.Header.Set("X-Real-IP", "203.0.113.9")
	engine.ServeHTTP(httptest.NewRecorder(), req)

	if seenIP == "203.0.113.9" {
		t.Fatalf("remote_ip_headers 留空却采信了转发头（得到 %q）："+
			"随仓样例注释承诺的是「不从任何 header 取 IP」，而审计与限流的主体都取自这个值", seenIP)
	}
	if seenIP != proxyIP {
		t.Fatalf("ClientIP() = %q, want %q（连接对端）", seenIP, proxyIP)
	}
}

// TestPreflightDefersToRegisteredOptionsHandler 守住"空 Allow 的两种成因必须区分"。
//
// 这条是对抗性审查抓出来的**回归**：预检的可用方法改为读 gin 写的 `Allow` 头之后，
// 路径上若注册了 OPTIONS（或 Any），gin 会直接匹配到那条路由、根本不进 405 分支，
// 于是 `Allow` 为空——而空串此前的唯一含义是"路径不存在"，预检因此返回 404，
// 而同一路径的实际请求返回 200。
//
// 那与本中间件要消除的缺陷**完全同形**，只是触发条件从"带参路由"换成
// "注册了 OPTIONS 的路径"。母版尤其要守住它：派生项目用 `Any` 或手写 OPTIONS
// 都是常见写法。
func TestPreflightDefersToRegisteredOptionsHandler(t *testing.T) {
	t.Parallel()
	gin.SetMode(gin.TestMode)

	var optionsHandlerRan bool
	engine, err := newRouterForTest(t, "test", Options{
		AllowedOrigins: []string{"https://example.com"},
		RegisterModules: func(v1 *gin.RouterGroup) {
			v1.GET("/thing/:id", func(c *gin.Context) { c.Status(http.StatusOK) })
			v1.OPTIONS("/thing/:id", func(c *gin.Context) {
				optionsHandlerRan = true
				c.Status(http.StatusNoContent)
			})
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	rec := doPreflight(t, engine, "/api/v1/thing/42", http.MethodGet)
	if rec.Code == http.StatusNotFound {
		t.Fatal("路径上注册了 OPTIONS 时预检被判为路径不存在：" +
			"gin 直接匹配到该路由、不进 405 分支，因此不写 Allow——空 Allow 不等于路径不存在")
	}
	if rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want 204", rec.Code)
	}
	if !optionsHandlerRan {
		t.Error("应用自己注册的 OPTIONS handler 未被执行：显式注册的预检处理不该被中间件吞掉")
	}
}

// TestPreflightStillRejectsMissingPathWithoutOptions 是上一条的反向锚。
//
// 缺了它，一个"空 Allow 一律放行"的实现也能让上面通过——而那会让不存在的路径
// 的预检回 204，浏览器据此发出实际请求再拿到 404。
func TestPreflightStillRejectsMissingPathWithoutOptions(t *testing.T) {
	t.Parallel()

	rec := doPreflight(t, newParamRouteEngine(t), "/api/v1/nope/42", http.MethodGet)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404：不存在的路径不该因为空 Allow 就被放行", rec.Code)
	}
}
