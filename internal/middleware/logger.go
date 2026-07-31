package middleware

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/gin-contrib/requestid"
	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	applog "github.com/gtkit/gin-api/internal/pkg/log"
	resp "github.com/gtkit/gin-api/internal/pkg/response"
)

// maxBodyLogSize 记录 request/response body 的最大字节数，
// 超过部分截断，避免大 body 撑爆日志缓冲区。
const maxBodyLogSize = 4096

// AccessLogOptions 控制访问日志的 body 记录策略。
// 两个开关相互独立，默认（零值）均为 false，即不记录任何 body；
// query string 无论开关如何始终经脱敏后记录。
type AccessLogOptions struct {
	// LogRequestBody 开启后记录经脱敏的请求体。
	LogRequestBody bool
	// LogResponseBody 开启后记录经脱敏的响应体。
	LogResponseBody bool
}

// skipExts 不需要记录访问日志的静态资源后缀。
var skipExts = []string{
	".css", ".js", ".png", ".jpg", ".jpeg",
	".gif", ".ico", ".svg", ".woff", ".woff2",
	".ttf", ".map", ".json",
}

// bodyLogCaptureLimit 是 body 日志捕获的内存上界：比 maxBodyLogSize 多 1 字节，
// 使恰好超限的捕获仍能被 truncate() 打上 ...(truncated) 标记。
const bodyLogCaptureLimit = maxBodyLogSize + 1

// responseBodyWriter 旁路捕获响应前缀用于访问日志。
// 捕获有界（bodyLogCaptureLimit）：超出部分直通客户端、不再入缓冲，
// 大文件/SSE/流式响应的内存成本与响应总量解耦。
type responseBodyWriter struct {
	gin.ResponseWriter
	body *bytes.Buffer
}

func (r responseBodyWriter) Write(b []byte) (int, error) {
	if remain := bodyLogCaptureLimit - r.body.Len(); remain > 0 {
		r.body.Write(b[:min(len(b), remain)])
	}
	return r.ResponseWriter.Write(b)
}

// GinLogger 记录 HTTP 访问日志，使用 BufferedWriteSyncer 的 access logger。
// 按响应状态码分级：4xx→Warn, 5xx→Error, 其余→Info。
//
// body 记录由 opts 控制且默认关闭：只有显式开启对应开关时才读取请求体 /
// 包装响应体，且落盘前一律经敏感字段脱敏。query string 无论开关如何都脱敏后记录。
func GinLogger(opts AccessLogOptions) gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path

		// 跳过静态资源。
		for _, ext := range skipExts {
			if strings.HasSuffix(path, ext) {
				c.Next()
				return
			}
		}

		start := time.Now()
		method := c.Request.Method
		writeMethod := method == "POST" || method == "PUT" || method == "PATCH" || method == "DELETE"
		logReqBody := opts.LogRequestBody && writeMethod
		logRespBody := opts.LogResponseBody && writeMethod

		// 仅在开启响应体日志时才包装 writer；仅在开启请求体日志时才读取 body。
		// 关闭时零额外开销，也不会把 body 读进内存。
		var (
			requestBody     []byte
			requestBodyType string
			w               *responseBodyWriter
		)
		if logRespBody {
			w = &responseBodyWriter{body: &bytes.Buffer{}, ResponseWriter: c.Writer}
			c.Writer = w
		}
		// 读取失败时跳过 handler：响应已由下面的错误分支写出、body 已部分消费，
		// 把截断数据交给下游只会产生二次错误；访问日志仍照常落盘。
		skipHandler := false
		if logReqBody && c.Request.Body != nil {
			// 只预读日志需要的前缀（有界），剩余字节无损拼回交给 handler——
			// 中间件为日志持有的内存与 body 总量解耦（原实现全量预读，上界高达 MaxBodyBytes）。
			// 前缀仍经 MaxBytesReader 读出，超限/读取错误依旧在 handler 前暴露。
			var readErr error
			requestBody, readErr = io.ReadAll(io.LimitReader(c.Request.Body, bodyLogCaptureLimit))
			if readErr == nil {
				c.Request.Body = &prefixedBody{
					Reader: io.MultiReader(bytes.NewReader(requestBody), c.Request.Body),
					closer: c.Request.Body,
				}
				requestBodyType = c.ContentType()
			} else {
				// 超限（MaxBytesReader 返回 *http.MaxBytesError）→ 413；
				// 其他读取失败按绑定失败口径 → 422。不再静默吞错误。
				if maxBytesErr, ok := errors.AsType[*http.MaxBytesError](readErr); ok {
					resp.PayloadTooLargeErr(c, "", maxBytesErr.Limit)
				} else {
					resp.BindStructErr(c, readErr)
				}
				skipHandler = true
			}
		}

		if !skipHandler {
			c.Next()
		}

		status := c.Writer.Status()
		fields := []zap.Field{
			zap.Int("status", status),
			zap.String("method", method),
			zap.String("path", path),
			zap.String("query", redactQuery(c.Request.URL.RawQuery)),
			zap.String("ip", c.ClientIP()),
			zap.Duration("latency", time.Since(start)),
			zap.String("requestId", requestid.Get(c)),
			zap.String("userAgent", c.GetHeader("User-Agent")),
		}

		if logReqBody {
			fields = append(fields, zap.String("requestBody",
				truncate(redactBody(requestBodyType, requestBody), maxBodyLogSize)))
		}
		if logRespBody {
			fields = append(fields, zap.String("responseBody",
				truncate(redactBody(w.Header().Get("Content-Type"), w.body.Bytes()), maxBodyLogSize)))
		}

		accessLogger := applog.AccessLog()
		switch {
		case status >= 500:
			accessLogger.Error(path, fields...)
		case status > 400:
			accessLogger.Warn(path, fields...)
		default:
			accessLogger.Info(path, fields...)
		}
	}
}

// prefixedBody 把已预读的前缀与未消费的原始 body 拼回一个 ReadCloser，
// Close 透传给原始 body（maxbody 的 wrapper Close 幂等）。
type prefixedBody struct {
	io.Reader
	closer io.Closer
}

func (p *prefixedBody) Close() error { return p.closer.Close() }

// truncate 截断字符串到 maxLen 字节，超出部分标记 ...(truncated)。
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "...(truncated)"
}
