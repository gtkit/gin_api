package middleware

import (
	"io"
	"net/http"
	"path/filepath"
	"strings"

	resp "github.com/gtkit/gin-api/internal/pkg/response"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/goerr"
)

var (
	// 允许的文件类型.
	allowedMimeTypes = map[string]bool{
		"image/jpeg":         true,
		"image/png":          true,
		"image/gif":          true,
		"application/pdf":    true,
		"text/plain":         true,
		"application/msword": true,
	}
	// 允许的文件扩展名.
	allowedExtensions = map[string]bool{
		".jpg":  true,
		".jpeg": true,
		".png":  true,
		".gif":  true,
		".pdf":  true,
		".txt":  true,
		".doc":  true,
		".docx": true,
	}
)

// FileTypeMiddleware 文件类型验证中间件.
func FileTypeMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		file, err := c.FormFile("file")
		if err != nil {
			c.Next()
			return
		}

		// Step 1: 先通过文件扩展名做快速校验。
		// 这一步成本最低，能尽早拦住明显不允许的上传类型。
		ext := strings.ToLower(filepath.Ext(file.Filename))
		if !allowedExtensions[ext] {
			abortFileValidationError(c, "不支持的文件类型: "+ext, []string{".jpg", ".png", ".pdf", ".txt"})
			return
		}

		// Step 2: 再通过 MIME 做更可靠的内容校验。
		// 扩展名可以伪造，MIME 检测能进一步减少伪装文件混入的风险。
		f, err := file.Open()
		if err != nil {
			c.Next()
			return
		}
		defer f.Close()
		// 读取前512字节检测MIME类型
		buffer := make([]byte, 512)
		_, err = f.Read(buffer)
		if err != nil && err != io.EOF {
			c.Next()
			return
		}
		mimeType := http.DetectContentType(buffer)
		if !allowedMimeTypes[mimeType] {
			abortFileValidationError(c, "不支持的文件类型: "+mimeType, []string{
				"image/jpeg",
				"image/png",
				"application/pdf",
			})
			return
		}

		// Step 3: 重置文件指针，确保后续 handler 读取到完整文件内容。
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			c.Next()
			return
		}
		c.Next()
	}
}

// abortFileValidationError 统一返回文件上传校验失败响应。
// 这类错误属于客户端输入问题，因此沿用参数错误码，并把允许列表放到 data 里返回。
func abortFileValidationError(c *gin.Context, message string, allowed []string) {
	c.AbortWithStatusJSON(http.StatusBadRequest, resp.NewResponse(
		int32(goerr.ErrParams),
		message,
		gin.H{"allowed": allowed},
	))
}
