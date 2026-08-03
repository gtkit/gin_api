package resp

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/goerr"
	logger "github.com/gtkit/logger/v2"
	"go.uber.org/zap"
)

// ResResult 统一响应信封。
type ResResult struct {
	Code    goerr.Code `json:"code"`
	Message string     `json:"msg"`
	Data    any        `json:"data"`
}

// ErrMapping 将一个 sentinel error 映射到 goerr.Status + 可选的客户端文案。
type ErrMapping struct {
	Target  error
	Status  *goerr.Status
	Message string // 可选，覆盖默认 Status 文案
}

// ErrMapper 持有一组 sentinel error → goerr.Status 映射。
type ErrMapper struct {
	mappings []ErrMapping
}

// NewErrMapper 创建错误映射器。
func NewErrMapper(mappings ...ErrMapping) *ErrMapper {
	return &ErrMapper{mappings: mappings}
}

// Resolve 尝试把 err 匹配到已注册的 sentinel error。
// 匹配成功返回对应的 goerr.Error，未匹配返回 nil。
func (m *ErrMapper) Resolve(err error) goerr.Error {
	if err == nil || m == nil {
		return nil
	}
	for _, mapping := range m.mappings {
		if errors.Is(err, mapping.Target) {
			if mapping.Message != "" {
				return goerr.New(err, mapping.Status, mapping.Message)
			}
			return goerr.New(err, mapping.Status)
		}
	}
	return nil
}

var (
	emptyData            = [0]int{}
	clientMessageOptions = goerr.SanitizeOptions{
		MaxRunes:    goerr.DefaultMaxMessageRunes,
		RedactPhone: true,
		RedactEmail: true,
	}
)

func newResponse(code goerr.Code, msg string, data any) *ResResult {
	return &ResResult{
		Code:    code,
		Message: msg,
		Data:    normalizeData(data),
	}
}

func Ok(c *gin.Context, data any) {
	respJSON(c, http.StatusOK, newResponse(0, "OK", data))
}

func Err(c *gin.Context, err error) {
	ErrWithData(c, err, nil)
}

// BindErr 处理参数绑定/验证错误，返回 HTTP 422；请求体超限单独映射为 413。
func BindErr(c *gin.Context, err error) {
	if payloadTooLargeFromBind(c, err) {
		return
	}
	message := "bad request"
	if err != nil {
		message = err.Error()
	}
	Err(c, goerr.New(err, goerr.StatusValidateParams(), message))
}

// MapErr 通过 mapper 解析业务错误并返回统一响应。
// 匹配到已注册的 sentinel error 时使用对应业务码和文案，未匹配时按内部错误处理。
func MapErr(c *gin.Context, err error, mapper *ErrMapper) {
	if goErr := mapper.Resolve(err); goErr != nil {
		Err(c, goErr)
		return
	}
	Err(c, goerr.New(err, goerr.StatusInternalServer()))
}

// ErrWithData 返回统一错误响应，并把额外元数据放在 data 中。
// HTTP 状态码由 item.HTTPCode() 决定：
//   - 业务错误（Status HTTPCode=200）→ HTTP 200
//   - 基础设施错误 → 对应 HTTP 语义（400/401/500 等）
//
// 日志分级：5xx → error，4xx → warn，2xx → 不记录。
func ErrWithData(c *gin.Context, err error, data any) {
	item := toError(err, goerr.StatusInternalServer)
	httpCode := item.HTTPCode()
	logError(c, item, httpCode)
	respJSON(c, httpCode, newResponse(item.Code(), clientMessage(item), data))
}

func NotFoundError(c *gin.Context, errmsg string) {
	Err(c, goerr.New(nil, goerr.StatusNotFound(), errmsg))
}

// MethodNotAllowedCode 是方法不允许对客户端暴露的业务错误码。
const MethodNotAllowedCode int32 = 10010405

// NotAllowedMethod 返回 405。
//
// 此前它走 StatusRequestFail()，实际发出的是 **400**——客户端无从区分
// "请求格式错误"与"这个路径不支持该方法"，配上 Allow 头更是自相矛盾
// （Allow 是 405 的语义，HTTP 也要求 405 必须带 Allow）。
// 调用方负责在此之前写好 Allow 头。
func NotAllowedMethod(c *gin.Context, errmsg string) {
	if errmsg == "" {
		errmsg = "method not allowed"
	}
	respJSON(c, http.StatusMethodNotAllowed, NewResponse(MethodNotAllowedCode, errmsg, nil))
}

func normalizeData(data any) any {
	if data != nil {
		return data
	}
	return emptyData
}

// clientMessage 决定返回给客户端的错误文案。
// 统一只使用 Message()，避免把 Error() 中的底层 cause 或实现细节暴露出去。
func clientMessage(item goerr.Error) string {
	if item == nil {
		return ""
	}
	return goerr.SanitizeForClient(item.Message(), clientMessageOptions)
}

func toError(err error, fallback func() *goerr.Status) goerr.Error {
	if item, ok := goerr.AsItem(err); ok {
		return item
	}
	if err == nil {
		return goerr.New(nil, fallback())
	}
	return goerr.New(err, fallback())
}

// logError 在响应层统一记录错误日志。
// 5xx → error 级别，4xx → warn 级别，2xx（业务错误）→ 不记录（属于正常业务流程）。
func logError(c *gin.Context, item goerr.Error, httpCode int) {
	if c == nil || item == nil {
		return
	}

	if httpCode < http.StatusBadRequest {
		return
	}

	fields := []zap.Field{}
	if c.Request != nil {
		fields = append(fields,
			zap.String("method", c.Request.Method),
			zap.String("path", c.Request.URL.Path),
		)
	}
	fields = append(fields,
		zap.Int("http_status", httpCode),
		zap.Int("error_code", int(item.Code())),
		zap.String("error", item.Error()),
	)

	if httpCode >= http.StatusInternalServerError {
		logger.ErrorCtx(requestCtx(c), "[Response] request failed", fields...)
		return
	}
	logger.WarnCtx(requestCtx(c), "[Response] request failed", fields...)
}

// NewResponse 创建自定义 code 的响应，供中间件直接用 c.AbortWithStatusJSON 时使用。
// data 可选，传入时附加到响应的 data 字段。
func NewResponse(code int32, msg string, data ...any) *ResResult {
	var d any
	if len(data) > 0 {
		d = data[0]
	}
	return newResponse(goerr.Code(code), msg, d)
}

// Error 是 Err 的别名，兼容中间件层调用。
func Error(c *gin.Context, err error) {
	Err(c, err)
}

// OkErr 返回 HTTP 200 + 业务错误码响应（用于"请求成功但业务被拒"场景）。
func OkErr(c *gin.Context, err goerr.Error) {
	respJSON(c, http.StatusOK, newResponse(err.Code(), clientMessage(err), nil))
}

// BindStructErr 处理结构体绑定/验证错误，使用 HTTP 422；请求体超限单独映射为 413。
func BindStructErr(c *gin.Context, err error) {
	if payloadTooLargeFromBind(c, err) {
		return
	}
	message := "bad request"
	if err != nil {
		message = err.Error()
	}
	Err(c, goerr.New(err, goerr.StatusValidateParams(), message))
}

// TooManyRequestsCode 是被限流时对客户端暴露的业务错误码。
const TooManyRequestsCode int32 = 10010429

// TooManyRequestsErr 返回 429。
//
// 必须是 429 而不是 403 或 503：客户端与网关对这三者的处置完全不同——
// 429 是"退避后重试"，403 是"别再试了"，503 是"服务端故障"。
// 用错的代价是客户端要么放弃一次本该成功的请求，要么立刻重试加剧过载。
//
// 调用方负责在此之前写好 Retry-After 头。
func TooManyRequestsErr(c *gin.Context, message string) {
	if message == "" {
		message = "请求过于频繁"
	}
	respJSON(c, http.StatusTooManyRequests, NewResponse(TooManyRequestsCode, message, nil))
}

// GatewayTimeoutCode 是业务预算到点对客户端暴露的业务错误码。
const GatewayTimeoutCode int32 = 10010504

// GatewayTimeoutErr 返回 504。
//
// 显式写 504 而不是走 goerr.StatusTimeout()：后者的 HTTPCode 是 500，
// 而 500 与 504 对客户端是两件事——500 说"服务端出错了，重试大概率还是错"，
// 504 说"这次没在预算内完成，重试可能成功"。把超时报成 500 会让客户端
// 与网关的重试策略都失去依据，也让错误率指标把一次容量问题记成一次故障。
func GatewayTimeoutErr(c *gin.Context, message string) {
	if message == "" {
		message = "请求超时"
	}
	respJSON(c, http.StatusGatewayTimeout, NewResponse(GatewayTimeoutCode, message, nil))
}

// PayloadTooLargeCode 是请求体超限对客户端暴露的业务错误码（沿用历史号段）。
const PayloadTooLargeCode int32 = 10010019

// PayloadTooLargeErr 返回统一的 413 响应。
// message 为空时使用默认文案；data 携带上限值，方便客户端按限制调整上传逻辑。
func PayloadTooLargeErr(c *gin.Context, message string, maxBytes int64) {
	if message == "" {
		message = "请求体过大"
	}
	respJSON(c, http.StatusRequestEntityTooLarge, NewResponse(PayloadTooLargeCode, message, gin.H{
		"max_bytes":       maxBytes,
		"max_bytes_human": FormatBytes(maxBytes),
	}))
}

// payloadTooLargeFromBind 识别绑定错误链上的请求体超限（http.MaxBytesReader
// 超限时从 Read 返回 *http.MaxBytesError，gin 各类 binding 原样或包装上抛），
// 命中即按 413 统一处理。这是 chunked 请求（无声明长度）唯一可靠的判定点。
func payloadTooLargeFromBind(c *gin.Context, err error) bool {
	maxBytesErr, ok := errors.AsType[*http.MaxBytesError](err)
	if !ok {
		return false
	}
	PayloadTooLargeErr(c, "", maxBytesErr.Limit)

	return true
}

// FormatBytes 把字节数格式化为人类可读形式（B/KB/MB/…）。
func FormatBytes(bytes int64) string {
	const unit = 1024
	if bytes < unit {
		return fmt.Sprintf("%d B", bytes)
	}
	div, exp := int64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB",
		float64(bytes)/float64(div),
		"KMGTPE"[exp])
}

func respJSON(c *gin.Context, httpCode int, data any) {
	if c == nil {
		return
	}
	c.Abort()
	c.JSON(httpCode, data)
}

// requestCtx 取请求 context（含 requestId）供日志 *Ctx 方法提取；请求缺失时退回 Background。
func requestCtx(c *gin.Context) context.Context {
	if c != nil && c.Request != nil {
		return c.Request.Context()
	}
	return context.Background()
}
