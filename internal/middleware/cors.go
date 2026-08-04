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

// AllowedMethodsFunc 返回某条路径允许的方法（形如 "GET, HEAD, OPTIONS"）。
//
// 路径不存在时返回空串。由路由索引提供——CORS 中间件自己不知道注册了哪些路由，
// 而预检必须回答"这个路径上这个方法真的能用吗"。
type AllowedMethodsFunc func(path string) string

// Cors 返回 CORS 中间件，仅允许 allowedOrigins 中列出的来源跨域访问。
//
// 特殊值 "*" 表示允许所有来源（开发环境），此时 Access-Control-Allow-Credentials
// 设为 false（规范要求 wildcard 与 credentials 不兼容）。
//
// allowedMethods 为 nil 时预检不校验目标路由。传 nil 只应出现在没有路由索引的
// 场景（例如本包的单测）；生产装配必须传入，否则预检会对不存在的路径回 204，
// 而随后的实际请求拿到 404——客户端据此排查的方向是错的。
func Cors(allowedOrigins []string, allowedMethods AllowedMethodsFunc) gin.HandlerFunc {
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
		c.Header("Access-Control-Allow-Methods", "POST,GET,OPTIONS,DELETE,PUT")
		c.Header("Access-Control-Expose-Headers",
			"Content-Length,Access-Control-Allow-Origin,Access-Control-Allow-Headers,"+
				"Content-Type,New-Token,New-Expires-At")

		if c.Request.Method == http.MethodOptions {
			handlePreflight(c, allowedMethods)
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
func handlePreflight(c *gin.Context, allowedMethods AllowedMethodsFunc) {
	// 预检响应还依这两个请求头而变。
	c.Writer.Header().Add("Vary", "Access-Control-Request-Method")
	c.Writer.Header().Add("Vary", "Access-Control-Request-Headers")

	requested := strings.ToUpper(strings.TrimSpace(c.GetHeader("Access-Control-Request-Method")))
	if requested == "" {
		// 没有声明目标方法的 OPTIONS 不是预检请求，无从校验。
		c.AbortWithStatus(http.StatusNoContent)
		return
	}

	if allowedMethods != nil {
		allow := allowedMethods(c.Request.URL.Path)
		if allow == "" {
			// 路径不存在：预检不该假称它可用。
			c.AbortWithStatus(http.StatusNotFound)
			return
		}
		if !methodAllowed(allow, requested) {
			c.Header("Allow", allow)
			c.AbortWithStatus(http.StatusMethodNotAllowed)
			return
		}
	}

	if !requestHeadersAllowed(c.GetHeader("Access-Control-Request-Headers")) {
		// 明确拒绝而不是静默放行：放行后浏览器会发出实际请求，而那个头在服务端
		// 并不被接受——失败点被推迟，且更难定位到是哪个头引起的。
		c.AbortWithStatus(http.StatusForbidden)
		return
	}

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
