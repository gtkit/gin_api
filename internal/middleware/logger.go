package middleware

import (
	"bytes"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/gtkit/gin-api/internal/pkg/ginc"
	applog "github.com/gtkit/gin-api/internal/pkg/log"
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
		if _, err := r.body.Write(b[:min(len(b), remain)]); err != nil {
			return 0, fmt.Errorf("capture response log prefix: %w", err)
		}
	}
	return r.ResponseWriter.Write(b)
}

// Unwrap 暴露被包装的 ResponseWriter，让 http.ResponseController 能递归找到
// 底层连接。
//
// 没有它，开启响应体日志就会让 SetWriteDeadline 返回 ErrNotSupported——
// 一个纯观测开关会**静默关掉超时保护**。而这条失效是无声的：
// 日志照常记录，只是所有 socket deadline 都不再生效。
//
// 凡是透明包装 ResponseWriter 的类型都必须实现它，由 writer 能力测试统一断言。
func (r responseBodyWriter) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

// GinLogger 记录 HTTP 访问日志，使用 BufferedWriteSyncer 的 access logger。
// 按响应状态码分级：4xx→Warn, 5xx→Error, 其余→Info。
//
// body 记录由 opts 控制且默认关闭：只有显式开启对应开关时才读取请求体 /
// 包装响应体，且落盘前一律经敏感字段脱敏。query string 无论开关如何都脱敏后记录。
func GinLogger(opts AccessLogOptions) gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path

		// 不按后缀跳过任何请求。
		//
		// 旧实现跳过 .css/.js/.json 等后缀，理由是"静态资源不必记录"。
		// 但路径是**客户端完全可控的**：给任意 API 路径加一个 .json 后缀，
		// 那次请求就不再产生访问记录——攻击者据此可以让自己的探测与越权尝试
		// 在访问日志里完全消失。而本仓库是纯 JSON API，根本不提供静态资源，
		// 这条规则从一开始就只有副作用。
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
		// **旁路捕获，不主动预读**。
		//
		// 主动预读发生在 c.Next() 之前——而字节上限中间件在链的更内层，那时
		// 还没接管 body。于是预读读到的是**未受上限约束**的流，此前那段代码里
		// 判定 MaxBytesError 的分支在这条路径上根本不可达。
		//
		// 改为包装：handler 真正读 body 时顺带抄走前若干字节。好处有三——
		// handler 不读 body 时（例如 GET）零开销；读取发生在内层上限接管之后，
		// 上限真正生效；错误处置回归它本来的位置（上限中间件与绑定逻辑），
		// 日志中间件不再替它们报错。
		var tee *teeBody
		if logReqBody && c.Request.Body != nil && c.Request.Body != http.NoBody {
			tee = &teeBody{ReadCloser: c.Request.Body, limit: bodyLogCaptureLimit}
			c.Request.Body = tee
			requestBodyType = c.ContentType()
		}

		c.Next()

		if tee != nil {
			requestBody = tee.captured
		}

		status := c.Writer.Status()
		level := applog.LevelInfo
		if status >= 500 {
			level = applog.LevelError
		} else if status >= 400 {
			level = applog.LevelWarn
		}
		if !applog.Enabled(level, applog.AccessStream) {
			return
		}
		fields := []applog.Field{
			applog.Int("status", status),
			applog.String("method", method),
			applog.String("path", path),
			applog.String("query", redactQuery(c.Request.URL.RawQuery)),
			applog.String("ip", ginc.ClientIP(c)),
			applog.Duration("latency", time.Since(start)),
			applog.String("requestId", RequestIDFromContext(c)),
			applog.String("userAgent", c.GetHeader("User-Agent")),
		}

		if logReqBody {
			fields = append(fields, applog.String("requestBody",
				truncate(redactBody(requestBodyType, requestBody), maxBodyLogSize)))
		}
		if logRespBody {
			fields = append(fields, applog.String("responseBody",
				truncate(redactBody(w.Header().Get("Content-Type"), w.body.Bytes()), maxBodyLogSize)))
		}

		applog.Log(c.Request.Context(), applog.LogEntry{Level: level, Message: path, Fields: fields, Stream: applog.AccessStream})
	}
}

// teeBody 在 handler 读取请求体时旁路抄走前若干字节。
//
// 只抄前缀而不是全量：日志要回答的是"这次请求大概发了什么"，而全量会让中间件
// 持有的内存与请求体大小挂钩——一个刚好在上限内的大请求就能撑爆日志缓冲。
//
// Close 由内嵌的 ReadCloser 透传至原始请求体。
type teeBody struct {
	io.ReadCloser
	captured []byte
	limit    int
}

func (t *teeBody) Read(p []byte) (int, error) {
	n, err := t.ReadCloser.Read(p)
	if n > 0 && len(t.captured) < t.limit {
		room := min(n, t.limit-len(t.captured))
		t.captured = append(t.captured, p[:room]...)
	}
	return n, err
}

// truncate 截断字符串到 maxLen 字节，超出部分标记 ...(truncated)。
func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "...(truncated)"
}
