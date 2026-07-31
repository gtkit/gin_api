package middleware

import (
	"bytes"
	"net/http"

	"github.com/gtkit/gin-api/internal/pkg/cache"

	"github.com/gin-gonic/gin"
	logger "github.com/gtkit/logger/v2"
	"go.uber.org/zap"
)

// emptyDegradedResponse 是本地缓存也没有数据时的兜底响应。
// 返回空数组而非错误，前端拿到空列表正常渲染（"暂无内容"），不会弹错误。
var emptyDegradedResponse = []byte(`{"code":0,"msg":"OK","data":[]}`)

// FallbackMiddleware 为读接口提供本地内存降级能力。
//
// 工作流程：
//  1. Handler 正常返回 200 → 将响应体缓存到本地内存，写出正常响应
//  2. Handler 返回 5xx → 尝试从本地缓存返回上一次的成功响应
//  3. 本地缓存也没有 → 原样返回错误响应
//
// 适用场景：导航栏、商品列表、文章列表等变更低频的 GET 接口。
// 不适用场景：写操作、支付、需要实时数据的接口。
func FallbackMiddleware(fc *cache.FallbackCache) gin.HandlerFunc {
	return func(c *gin.Context) {
		key := c.Request.URL.RequestURI()

		// 用缓冲 Writer 拦截响应，先缓冲再决策。
		bw := &bufferedWriter{
			ResponseWriter: c.Writer,
			buf:            &bytes.Buffer{},
		}
		c.Writer = bw

		c.Next()

		status := bw.code
		body := bw.buf.Bytes()

		if status == http.StatusOK && len(body) > 0 {
			// 成功：更新兜底缓存，写出原始响应。
			fc.Set(key, body)
			bw.flush()
			return
		}

		if status >= http.StatusInternalServerError {
			// 5xx：尝试用本地缓存替换。
			if cached, ok := fc.Get(key); ok {
				logger.Warn("serving degraded response from local cache",
					zap.String("cache", fc.Name()),
					zap.String("uri", key),
					zap.Int("original_status", status),
				)
				bw.overrideWith(http.StatusOK, cached)
				return
			}

			// 本地缓存也没有（如服务刚启动）：返回友好的空数据响应，
			// 而不是透传 500 的内部错误信息。
			logger.Warn("serving empty degraded response (no cache available)",
				zap.String("cache", fc.Name()),
				zap.String("uri", key),
				zap.Int("original_status", status),
			)
			bw.overrideWith(http.StatusOK, emptyDegradedResponse)
			return
		}

		// 其他情况（4xx 等）：原样写出。
		bw.flush()
	}
}

// bufferedWriter 缓冲 Gin 的全部响应，延迟到 flush/overrideWith 时才真正写出。
type bufferedWriter struct {
	gin.ResponseWriter
	buf  *bytes.Buffer
	code int
}

func (w *bufferedWriter) WriteHeader(code int) {
	w.code = code
	// 不立即调用底层 WriteHeader，延迟到 flush。
}

func (w *bufferedWriter) Write(b []byte) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	return w.buf.Write(b)
}

func (w *bufferedWriter) WriteString(s string) (int, error) {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	return w.buf.WriteString(s)
}

// flush 将缓冲内容原样写出。
func (w *bufferedWriter) flush() {
	if w.code == 0 {
		w.code = http.StatusOK
	}
	w.ResponseWriter.WriteHeader(w.code)
	_, _ = w.ResponseWriter.Write(w.buf.Bytes())
}

// overrideWith 丢弃缓冲内容，用替代数据写出。
func (w *bufferedWriter) overrideWith(code int, body []byte) {
	w.ResponseWriter.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.ResponseWriter.Header().Set("X-Degraded", "true")
	w.ResponseWriter.WriteHeader(code)
	_, _ = w.ResponseWriter.Write(body)
}
