package middleware

import (
	"net/http"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"
)

// allowedRequestHeaders 是预检允许的请求头，全部小写以便比对。
//
// 与响应里的 Access-Control-Allow-Headers 同源：两处各写一份的话，预检放行的
// 头与实际声明的头会分叉，而分叉的方向通常是预检更宽——于是浏览器放行了一个
// 服务端并不接受的头。
var allowedRequestHeaders = []string{
	"content-type", "accesstoken", "x-csrf-token", "authorization",
	"token", "x-token", "x-user-id", "app-token",
}

// allowHeadersValue 是上面列表的响应形式。
const allowHeadersValue = "Content-Type,AccessToken,X-CSRF-Token,Authorization," +
	"Token,X-Token,X-User-Id,App-Token"

// Cors 返回 CORS 中间件，仅允许 allowedOrigins 中列出的来源跨域访问。
//
// 特殊值 "*" 表示允许所有来源（开发环境），此时 Access-Control-Allow-Credentials
// 设为 false（规范要求 wildcard 与 credentials 不兼容）。
//
// **要求 engine 开启 HandleMethodNotAllowed**：预检的可用方法取自 gin 在
// 405 路径上写的 `Allow` 头，该标志关闭时 gin 既不产生 405 也不写该头，
// 于是每个预检都会被判成"路径不存在"。生产 engine 对该标志有断言
// （internal/router 的 engine 字段清单测试）。
//
// 预检的"这个路径上这个方法能用吗"取自 gin 已经写好的 `Allow` 响应头，
// 本中间件不自建路由索引：gin 在进入中间件链之前就用**真实的路由树匹配**
// 算出了该路径支持的方法（RFC 7231 §6.5.5 要求 405 携带 Allow），
// 而应用层自己推导必然要重写一遍那套匹配——两份匹配逻辑在带参路由这类
// 边界上一旦不一致，表现就是"预检说路径不存在、实际请求却成功"。
func Cors(allowedOrigins []string) gin.HandlerFunc {
	allowAll := slices.Contains(allowedOrigins, "*")

	// 预构建小写白名单，用于精确匹配。
	lowerOrigins := make([]string, 0, len(allowedOrigins))
	for _, o := range allowedOrigins {
		lowerOrigins = append(lowerOrigins, strings.ToLower(strings.TrimSpace(o)))
	}

	return func(c *gin.Context) {
		origin := c.Request.Header.Get("Origin")

		// **Vary 无条件写**，包括没有 Origin 的请求。
		//
		// 响应内容依 Origin 而变（Allow-Origin 回显请求来源），共享缓存却按
		// URL 缓存。不声明 Vary 时，CDN 会把 A 源拿到的响应原样发给 B 源——
		// 那既是跨源数据泄漏，也会让 B 源莫名其妙地被浏览器拒绝。
		//
		// 写在最前面：后面每条分支都会 return，逐条补写必然漏掉其中一条。
		c.Header("Vary", "Origin")

		if origin == "" {
			c.Next()
			return
		}

		switch {
		case allowAll:
			c.Header("Access-Control-Allow-Origin", "*")
			// wildcard 模式下规范禁止同时设置 credentials。
		case slices.Contains(lowerOrigins, strings.ToLower(origin)):
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Credentials", "true")
		default:
			// 不匹配：不写 CORS 头，浏览器会自动拒绝。
			if c.Request.Method == http.MethodOptions {
				c.AbortWithStatus(http.StatusForbidden)
				return
			}
			c.Next()
			return
		}

		c.Header("Access-Control-Allow-Headers", allowHeadersValue)
		c.Header("Access-Control-Expose-Headers",
			"Content-Length,Access-Control-Allow-Origin,Access-Control-Allow-Headers,"+
				"Content-Type,New-Token,New-Expires-At")

		// Access-Control-Allow-Methods 只写在预检响应里。
		//
		// 按 CORS 规范，浏览器只在预检响应中消费该头。写在每个跨源响应上不仅无用，
		// 还需要为"已匹配到路由"的实际请求另找一个方法来源——而实际请求走的不是
		// 405 路径，gin 不会设 Allow，于是只能自建索引或退回保守常量，两者都是
		// 本次要消除的形态。
		if c.Request.Method == http.MethodOptions {
			handlePreflight(c)
			return
		}

		c.Next()
	}
}

// handlePreflight 校验预检请求，而不是无条件回 204。
//
// 无条件 204 的后果是**预检成功、实际请求失败**：浏览器据此放行，随后拿到
// 404 或 405，而开发者看到的现象是"CORS 明明通过了却还是失败"——
// 排查方向被引向了错误的地方。
func handlePreflight(c *gin.Context) {
	// 预检响应还依这两个请求头而变。
	c.Writer.Header().Add("Vary", "Access-Control-Request-Method")
	c.Writer.Header().Add("Vary", "Access-Control-Request-Headers")

	requested := strings.ToUpper(strings.TrimSpace(c.GetHeader("Access-Control-Request-Method")))
	if requested == "" {
		// 没有声明目标方法的 OPTIONS 不是预检请求，无从校验。
		c.AbortWithStatus(http.StatusNoContent)
		return
	}

	// 可用方法取自 gin 已写好的 Allow 头。
	//
	// OPTIONS 通常不是注册过的方法，因此 gin 走的是"方法不匹配"分支：它用真实的
	// 路由树匹配枚举该路径支持的方法、Set("Allow", ...)，然后才进入中间件链。
	// 于是这里读到的就是框架的答案——带路径参数的路由也被正确匹配，而路径确实
	// 不存在时它为空（gin 落到未匹配路由分支，不设该头）。
	//
	// 不自建索引：那要在应用层重写一遍路由树匹配，而两份匹配逻辑一旦在边界上
	// 不一致，表现就是"预检说路径不存在、实际请求却成功"。
	allow := c.Writer.Header().Get("Allow")
	if allow == "" {
		// 空 Allow 有两种成因，必须区分，否则会把可用的路径判成不存在。
		//
		// ① 应用**自己注册了 OPTIONS**：gin 直接匹配到那条路由，根本不进 405 分支，
		//    因此不会写 Allow。此时路径是存在的，且应用显式接管了预检——交给它处理。
		//    判据用 c.FullPath()：405 与 404 两条路径上它都为空，只有真的匹配到
		//    路由时才非空。
		// ② 路径确实不存在：gin 落到未匹配路由分支，同样不写 Allow。
		//
		// 不区分的后果与本中间件要消除的缺陷完全同形：预检回 404 而实际请求 200，
		// 浏览器据此拦下一个本该成功的请求。区别只是触发条件从"带参路由"换成
		// "注册了 OPTIONS 的路径"。
		if c.FullPath() != "" {
			c.Next()
			return
		}
		// 路径不存在：预检不该假称它可用。
		c.AbortWithStatus(http.StatusNotFound)
		return
	}
	if !methodAllowed(allow, requested) {
		// Allow 已由 gin 写在响应头上，无需重复设置。
		c.AbortWithStatus(http.StatusMethodNotAllowed)
		return
	}

	if !requestHeadersAllowed(c.GetHeader("Access-Control-Request-Headers")) {
		// 明确拒绝而不是静默放行：放行后浏览器会发出实际请求，而那个头在服务端
		// 并不被接受——失败点被推迟，且更难定位到是哪个头引起的。
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

	// 预检通过：把该路径实际支持的方法回给浏览器。
	c.Header("Access-Control-Allow-Methods", allow)
	c.AbortWithStatus(http.StatusNoContent)
}

// methodAllowed 判断请求的方法是否在 Allow 头里。
func methodAllowed(allow, method string) bool {
	for candidate := range strings.SplitSeq(allow, ",") {
		if strings.EqualFold(strings.TrimSpace(candidate), method) {
			return true
		}
	}
	return false
}

// requestHeadersAllowed 报告预检声明的请求头是否全部被允许。
func requestHeadersAllowed(requested string) bool {
	if strings.TrimSpace(requested) == "" {
		return true
	}
	for raw := range strings.SplitSeq(requested, ",") {
		name := strings.ToLower(strings.TrimSpace(raw))
		if name == "" {
			continue
		}
		if !slices.Contains(allowedRequestHeaders, name) {
			return false
		}
	}
	return true
}
