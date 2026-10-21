package middleware

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httputil"
	"syscall"

	resp "github.com/gtkit/gin-api/internal/pkg/response"

	"github.com/gin-gonic/gin"
	logger "github.com/gtkit/gin-api/internal/pkg/log"
	"github.com/gtkit/goerr"
)

const (
	// recoveryResponseCode 是对客户端暴露的统一错误码。
	// panic 是否带 stack、是否在日志里记录更多上下文，属于服务端实现细节，
	// 不应该改变客户端看到的 error code。
	recoveryResponseCode = int32(goerr.ErrInternalServer)

	// 下面这组 code 仅用于日志分类和运维排查，不直接返回给客户端。
	// 继续沿用项目已有的 100100xx 号段，避免再出现孤立的 102/103/104。
	recoveryLogCodePanicRecovered   = int32(10010016)
	recoveryLogCodeClientClosedConn = int32(10010017)
	recoveryLogCodeResponseStarted  = int32(10010018)
)

var (
	// recoveryPublicMessage 使用项目统一错误体系里的默认文案，避免把 panic 细节泄露给客户端。
	recoveryPublicMessage = "服务器内部错误"

	// 这些 header 常常包含凭证或会话信息，写入日志前必须脱敏。
	// App-Token 是本项目实际认证头（见 middleware/auth.go），必须在列。
	recoverySensitiveHeaders = []string{
		"Authorization",
		"Cookie",
		"Proxy-Authorization",
		"App-Token",
	}
)

// GinRecovery recover 掉请求链路中的 panic，并按场景做三件事：
// 1. 统一记录结构化日志，方便排查。
// 2. 对普通 panic 返回项目统一的 500 响应。
// 3. 对连接已断开或响应已开始写出的场景，只记录日志，不再强行改写响应。
//
// 刻意不接告警（不用 H 系列）：请求级异常接入告警会在一次流量异常里形成
// 告警风暴，淹没真正的进程级事件。告警执行点收口在 bootstrap
// （worker 异常/降级与进程退出错误），本中间件只走日志与指标。
func GinRecovery(includeStack bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}

			// Step 1: 把任意 panic 值统一转换成 error，后续日志和分类逻辑都基于它处理。
			panicErr := normalizeRecoveredPanic(recovered)

			// Step 2: 尝试生成脱敏后的请求转储。
			// 请求 dump 失败不影响 recovery 主流程，只把失败原因带进日志里。
			requestDump, dumpErr := dumpRequestWithoutSensitiveHeaders(c.Request)

			// Step 3: 处理客户端连接已断开的场景。
			// 这类错误通常发生在服务端写响应时，客户端已经提前关闭连接；
			// 此时继续写 JSON 没有意义，直接记录日志并终止当前上下文即可。
			if isBrokenConnectionError(panicErr) {
				logger.Warn("[Recovery] client connection closed",
					buildRecoveryLogFields(
						c,
						panicErr,
						recovered,
						requestDump,
						dumpErr,
						recoveryLogCodeClientClosedConn,
						includeStack,
					)...,
				)
				c.Abort()

				return
			}

			// Step 4: 如果响应已经开始写出，就不能再安全地改成统一 JSON 结构了。
			// 比如 SSE / 流式响应 / 已经写出部分 body 的接口，这时只能记录日志并中止后续处理。
			if c.Writer != nil && c.Writer.Written() {
				logger.Error("[Recovery] panic recovered after response started",
					buildRecoveryLogFields(
						c,
						panicErr,
						recovered,
						requestDump,
						dumpErr,
						recoveryLogCodeResponseStarted,
						includeStack,
					)...,
				)
				c.Abort()

				return
			}

			// Step 5: 普通 panic 记录完整上下文，并返回统一的 500 响应。
			logger.Error("[Recovery] panic recovered",
				buildRecoveryLogFields(
					c,
					panicErr,
					recovered,
					requestDump,
					dumpErr,
					recoveryLogCodePanicRecovered,
					includeStack,
				)...,
			)
			abortWithRecoveryResponse(c)
		}()

		c.Next()
	}
}

// normalizeRecoveredPanic 把 recover 出来的任意值收敛成 error。
// 这样可以保证日志字段和错误分类逻辑始终处理同一种类型。
func normalizeRecoveredPanic(recovered any) error {
	switch value := recovered.(type) {
	case nil:
		return errors.New("panic recovered with nil value")
	case error:
		return value
	case string:
		return errors.New(value)
	default:
		return fmt.Errorf("panic recovered: %v", value)
	}
}

// isBrokenConnectionError 判断 panic 是否由客户端断连引起。
// 这里采用 Gin 官方 recovery 的判定方式：EPIPE、ECONNRESET、ErrAbortHandler。
func isBrokenConnectionError(err error) bool {
	return errors.Is(err, syscall.EPIPE) ||
		errors.Is(err, syscall.ECONNRESET) ||
		errors.Is(err, http.ErrAbortHandler)
}

// dumpRequestWithoutSensitiveHeaders 生成不带 body 的请求转储，并脱敏敏感头。
// recovery 日志需要足够的请求上下文，但不能把 token / cookie 原样写入日志。
func dumpRequestWithoutSensitiveHeaders(r *http.Request) (string, error) {
	if r == nil {
		return "", nil
	}

	// 先克隆请求和 header，避免直接修改原始请求对象，影响后续逻辑。
	// r.Clone 深拷贝 URL，故可安全改写克隆体的 RawQuery 而不影响原请求。
	clonedRequest := r.Clone(r.Context())
	clonedRequest.Header = r.Header.Clone()

	// 逐个覆盖敏感 header，保留 key，隐藏 value，方便排查时知道请求携带了哪些凭证类型。
	for _, headerName := range recoverySensitiveHeaders {
		if clonedRequest.Header.Get(headerName) == "" {
			continue
		}
		clonedRequest.Header.Set(headerName, "*")
	}

	// 脱敏请求行里的 query token（如 SSE 允许把 App-Token 放 query），
	// 复用访问日志同一套脱敏规则，避免 DumpRequest 把 query 凭证写进日志。
	// 注意：DumpRequest 优先用 RequestURI 拼请求行，故清空它，强制回退到已脱敏的 URL。
	if clonedRequest.URL != nil && clonedRequest.URL.RawQuery != "" {
		clonedRequest.URL.RawQuery = redactQuery(clonedRequest.URL.RawQuery)
		clonedRequest.RequestURI = ""
	}

	httpRequest, err := httputil.DumpRequest(clonedRequest, false)
	if err != nil {
		return "", err
	}

	return string(httpRequest), nil
}

// buildRecoveryLogFields 统一拼装 recovery 日志字段，避免三条分支各自散落一套字段定义。
func buildRecoveryLogFields(
	c *gin.Context,
	panicErr error,
	recovered any,
	requestDump string,
	dumpErr error,
	recoveryCode int32,
	includeStack bool,
) []logger.Field {
	fields := []logger.Field{
		logger.Int32("recovery_code", recoveryCode),
		logger.String("path", requestPath(c)),
		logger.Err(panicErr),
		logger.Any("panic_value", recovered),
	}

	if requestDump != "" {
		fields = append(fields, logger.String("request", requestDump))
	}
	if dumpErr != nil {
		fields = append(fields, logger.NamedError("request_dump_error", dumpErr))
	}
	if includeStack {
		fields = append(fields, logger.Stack("stack"))
	}

	return fields
}

// abortWithRecoveryResponse 返回项目统一错误结构，避免 recovery 自己拼一套特殊 JSON。
func abortWithRecoveryResponse(c *gin.Context) {
	c.AbortWithStatusJSON(http.StatusInternalServerError, resp.NewResponse(recoveryResponseCode, recoveryPublicMessage))
}

// requestPath 安全提取当前请求路径，避免在异常场景下再因为空指针触发二次 panic。
func requestPath(c *gin.Context) string {
	if c == nil || c.Request == nil || c.Request.URL == nil {
		return ""
	}

	return c.Request.URL.Path
}
