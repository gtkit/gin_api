package middleware

import (
	"bytes"
	"errors"
	"io"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/goerr"

	"github.com/gtkit/gin-api/internal/pkg/apisign"
)

// 签名校验的参数载体头。用 X-* 前缀：取值走请求头而非 query，
// query 会进访问日志、Referer 与浏览器历史，是长期泄漏面（与认证令牌同一条原则）。
const (
	headerAppID     = "X-App-Id"
	headerTimestamp = "X-Timestamp"
	headerNonce     = "X-Nonce"
	headerSign      = "X-Sign"
)

// 签名校验失败的技术原因。只进日志，不对客；对客文案由 Status 内置文案承担。
var (
	errSignatureParamsMissing = errors.New("signature parameters are missing")
	errAppIDUnknown           = errors.New("signature app id is unknown")
	errTimestampOutOfRange    = errors.New("signature timestamp is out of range")
	errNonceReused            = errors.New("signature nonce was reused")
	errSignatureMismatch      = errors.New("signature does not match")
)

// SignatureOptions 是请求签名校验中间件的依赖。
type SignatureOptions struct {
	// Secrets 是 appId → appSecret 的静态映射。空 map 时任何 appId 都判为未知。
	Secrets map[string]string
	// Nonce 是一次性随机数的原子去重存储；为 nil 时跳过 nonce 校验（不推荐）。
	Nonce apisign.NonceStore
	// MaxSkew 是允许的时钟偏移；ValidWindow 是时间戳有效期。
	MaxSkew     time.Duration
	ValidWindow time.Duration
	// Now 是时间源，nil 用 time.Now；仅供测试注入。
	Now func() time.Time
}

// Signature 校验服务端间调用的请求签名：完整性（HMAC）、新鲜性（时间戳）、
// 一次性（nonce）、来源合法性（appSecret）。与 JWT 鉴权是不同防线，可叠加挂载。
//
// 覆盖范围：method + path + query（排序后）+ body + appId + timestamp + nonce，
// 任一字段被改签名即失配。时间戳防长时重放，nonce（Redis SETNX）防有效期内重放。
//
// 只挂在需要服务端间签名校验的路由组上，不进全局链：普通用户请求走 JWT，
// 加一层 HMAC + 每请求一次 Redis 写入是纯开销。
func Signature(opts SignatureOptions) gin.HandlerFunc {
	if opts.Secrets == nil {
		panic("middleware: Signature requires a secrets map")
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}

	return func(c *gin.Context) {
		appID, timestamp, nonce, sign, err := signatureParams(c)
		if err != nil {
			abortWith(c, goerr.New(err, goerr.StatusAuthHeader()))
			return
		}

		secret, ok := opts.Secrets[appID]
		if !ok {
			abortWith(c, goerr.New(errAppIDUnknown, goerr.StatusAppKey()))
			return
		}

		ts, err := strconv.ParseInt(timestamp, 10, 64)
		if err != nil {
			abortWith(c, goerr.New(err, goerr.StatusAuthHeader()))
			return
		}
		now := opts.Now()
		age := now.Unix() - ts
		if age > int64((opts.ValidWindow+opts.MaxSkew)/time.Second) {
			abortWith(c, goerr.New(errTimestampOutOfRange, goerr.StatusAuthExpired()))
			return
		}
		if -age > int64(opts.MaxSkew/time.Second) {
			abortWith(c, goerr.New(errTimestampOutOfRange, goerr.StatusAuth()))
			return
		}

		if opts.Nonce != nil {
			// TTL 略长于时间戳有效期（含时钟偏移），确保窗口内的重放都能被识别。
			recorded, err := opts.Nonce.Record(c.Request.Context(), appID, nonce, opts.ValidWindow+opts.MaxSkew)
			if err != nil && abortOnContextError(c, err) {
				return
			}
			if err != nil {
				// nonce 存储不可用时必须拒绝而不能放行：放行等于让防重放失效。
				abortWith(c, goerr.New(err, goerr.StatusDependencyUnavailable()))
				return
			}
			if !recorded {
				abortWith(c, goerr.New(errNonceReused, goerr.StatusAuth()))
				return
			}
		}

		body, err := readBody(c)
		if err != nil {
			abortWith(c, goerr.New(err, goerr.StatusRequestFail()))
			return
		}

		canonical := apisign.CanonicalString(
			c.Request.Method,
			c.Request.URL.Path,
			c.Request.URL.Query().Encode(),
			string(body),
			appID, timestamp, nonce,
		)
		expected := apisign.Sign(secret, canonical)
		if !apisign.ConstantTimeEquals(expected, sign) {
			abortWith(c, goerr.New(errSignatureMismatch, goerr.StatusAuth()))
			return
		}

		c.Next()
	}
}

// signatureParams 提取四个签名参数，任一缺失或冲突即报错。
func signatureParams(c *gin.Context) (appID, timestamp, nonce, sign string, err error) {
	for _, name := range []string{headerAppID, headerTimestamp, headerNonce, headerSign} {
		if _, err := singleHeaderValue(c, name); err != nil {
			return "", "", "", "", err
		}
	}
	appID = c.GetHeader(headerAppID)
	timestamp = c.GetHeader(headerTimestamp)
	nonce = c.GetHeader(headerNonce)
	sign = c.GetHeader(headerSign)
	if appID == "" || timestamp == "" || nonce == "" || sign == "" {
		return "", "", "", "", errSignatureParamsMissing
	}
	return appID, timestamp, nonce, sign, nil
}

// readBody 读取并缓存请求体：验签要读 body 重算签名，后续业务也要读 body，
// 而 HTTP 请求体是只读一次的流，读完必须原样放回。
func readBody(c *gin.Context) ([]byte, error) {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return nil, err
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	return body, nil
}
