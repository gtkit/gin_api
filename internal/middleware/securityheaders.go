package middleware

import "github.com/gin-gonic/gin"

// 安全响应头的固定取值。
//
// 全部写死而不是配置项：这些取值没有"按环境调整"的合理形态，
// 一个可配置的安全头等于一个可以被关掉的安全头。确需放宽的项目
// 应当改这里的代码并在 code review 里说明理由。
const (
	// headerContentTypeOptions 关闭浏览器的 MIME 嗅探。
	//
	// 不关的话，一个被判定为 text/plain 的响应仍可能被浏览器当成 HTML 或脚本执行——
	// 用户上传的内容经由本服务回显时，这就是一条存储型 XSS 通道。
	headerContentTypeOptions = "nosniff"

	// headerFrameOptions 禁止本服务的响应被嵌进 iframe，挡点击劫持。
	//
	// 本仓库是 JSON API，没有任何需要被嵌套的页面，因此取最严格的 DENY。
	headerFrameOptions = "DENY"

	// headerReferrerPolicy 跨源跳转时不发送 Referer。
	//
	// 默认策略会把完整 URL 发给第三方，而 API 的 URL 常含资源 ID
	// （/api/v1/orders/8812）——那是一次无声的标识符泄漏。
	headerReferrerPolicy = "no-referrer"

	// headerCSP 对 API 响应禁用一切子资源加载。
	//
	// JSON API 永远不需要加载脚本、样式或框架内容。给它一条最严格的 CSP，
	// 万一某个响应被浏览器当作 HTML 渲染，其中的脚本也不会执行。
	headerCSP = "default-src 'none'; frame-ancestors 'none'; sandbox"

	// headerPermissionsPolicy 关闭浏览器高权限能力。
	headerPermissionsPolicy = "geolocation=(), microphone=(), camera=(), payment=()"
)

// SecurityHeaders 为所有响应写入固定的安全头。
//
// 在写入响应体**之前**设置：一旦 handler 开始写 body，响应头就已经发出，
// 此时再设置任何 header 都不会生效，而且不会有任何报错——
// 这正是"安全头看起来配了但线上没有"的典型成因。因此本中间件
// 必须排在 handler 之前，且在 c.Next() 之前设置。
//
// 不设置 HSTS：它由 TLS 终止层（网关/LB）负责。在明文 HTTP 后端发 HSTS，
// 要么无效，要么在某些拓扑下把一个错误的策略钉进浏览器一年。
func SecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		h := c.Writer.Header()
		h.Set("X-Content-Type-Options", headerContentTypeOptions)
		h.Set("X-Frame-Options", headerFrameOptions)
		h.Set("Referrer-Policy", headerReferrerPolicy)
		h.Set("Content-Security-Policy", headerCSP)
		h.Set("Permissions-Policy", headerPermissionsPolicy)
		c.Next()
	}
}
