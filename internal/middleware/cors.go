package middleware

import (
	"net/http"
	"slices"
	"strings"

	"github.com/gin-gonic/gin"
)

// Cors 返回 CORS 中间件，仅允许 allowedOrigins 中列出的来源跨域访问。
//
// 特殊值 "*" 表示允许所有来源（开发环境），此时 Access-Control-Allow-Credentials
// 设为 false（规范要求 wildcard 与 credentials 不兼容）。
func Cors(allowedOrigins []string) gin.HandlerFunc {
	allowAll := slices.Contains(allowedOrigins, "*")

	// 预构建小写白名单，用于精确匹配。
	lowerOrigins := make([]string, 0, len(allowedOrigins))
	for _, o := range allowedOrigins {
		lowerOrigins = append(lowerOrigins, strings.ToLower(strings.TrimSpace(o)))
	}

	return func(c *gin.Context) {
		origin := c.Request.Header.Get("Origin")
		if origin == "" {
			c.Next()
			return
		}

		if allowAll {
			c.Header("Access-Control-Allow-Origin", "*")
			// wildcard 模式下规范禁止同时设置 credentials。
		} else if slices.Contains(lowerOrigins, strings.ToLower(origin)) {
			c.Header("Access-Control-Allow-Origin", origin)
			c.Header("Access-Control-Allow-Credentials", "true")
		} else {
			// 不匹配：不写 CORS 头，浏览器会自动拒绝。
			if c.Request.Method == http.MethodOptions {
				c.AbortWithStatus(http.StatusForbidden)
				return
			}
			c.Next()
			return
		}

		c.Header("Access-Control-Allow-Headers", "Content-Type,AccessToken,X-CSRF-Token,Authorization,Token,X-Token,X-User-Id,App-Token")
		c.Header("Access-Control-Allow-Methods", "POST,GET,OPTIONS,DELETE,PUT")
		c.Header("Access-Control-Expose-Headers", "Content-Length,Access-Control-Allow-Origin,Access-Control-Allow-Headers,Content-Type,New-Token,New-Expires-At")

		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}
