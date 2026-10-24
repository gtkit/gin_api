package upload

import (
	"path"
	"strings"

	"github.com/gin-gonic/gin"
)

// FileHeaders 为上传文件的回源路由（静态服务 / 下载接口）设置响应安全头：
//
//   - `X-Content-Type-Options: nosniff` 恒定输出——浏览器不得对响应做类型嗅探提升；
//   - 按请求路径扩展名推断的类型不在 policy 白名单内时，强制
//     `Content-Disposition: attachment`，浏览器只能下载不能渲染。
//
// 头必须在 c.Next() 之前写：http.ServeFile 在 handler 内开始写响应，
// 事后没有机会再改头。ServeFile 只设置 Content-Type，不会覆盖这两个头。
// 零值 policy 全部强制下载（fail-closed）。
func FileHeaders(policy Policy) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		if !policy.AllowsInlinePath(c.Request.URL.Path) {
			c.Header("Content-Disposition", attachmentDisposition(path.Base(c.Request.URL.Path)))
		}
		c.Next()
	}
}

// attachmentDisposition 构造附件下载头。文件名只在保守字符集内才并入
// filename 参数——引号、控制字符、分号等都可能构成响应头注入或解析歧义，
// 含它们时退化为不带 filename 的裸 attachment（浏览器回退用路径末段命名）。
func attachmentDisposition(filename string) string {
	if filename == "" || filename == "/" || filename == "." || !isSafeFilename(filename) {
		return "attachment"
	}
	return `attachment; filename="` + filename + `"`
}

func isSafeFilename(name string) bool {
	if len(name) > 255 {
		return false
	}
	return strings.IndexFunc(name, func(r rune) bool {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
			return false
		case r == '.' || r == '-' || r == '_':
			return false
		default:
			return true
		}
	}) == -1
}
