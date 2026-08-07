package middleware

import "github.com/gin-gonic/gin"

// NoStore 给响应打上 Cache-Control: no-store（local-authentication spec）。
//
// 挂在认证端点与其他携带令牌/凭据的敏感响应上：这类响应一旦进浏览器
// 缓存、共享代理或 CDN，令牌的寿命就不再由服务端控制。头在 handler
// 之前设置，流式/提前写出的响应同样带上。
func NoStore() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Cache-Control", "no-store")
		c.Next()
	}
}
