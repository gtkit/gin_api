package ai

import (
	"errors"
	"io"
	"net"
	"net/http"
	"time"
)

// LLM 出站客户端的传输层参数。
//
// 这些不是配置项，是**常量**：没有哪个运维会去调 TLS 握手超时，而每多一个
// 旋钮就多一处可以配错的地方。真正需要按部署调整的两个预算——非流式请求总时长
// 与流式整体时长——都在 modules.ai 里，且经 ctx 生效。
//
// 各项取值理由：
//
//	dialTimeout            10s —— 建连超过这个量级基本是网络不通，而不是慢。
//	tlsHandshakeTimeout    10s —— 同上。
//	responseHeaderTimeout  90s —— **必须比 dial 宽得多**：这是"发出请求到收到
//	                       响应头"的等待，而推理模型在首 token 之前可能思考很久。
//	                       它是防止连接被无限期挂住的最后一道，不能设得太紧。
//	idleConnTimeout        90s —— 空闲连接的保留时长；与多数网关的空闲阈值同量级。
//	maxIdleConnsPerHost    16  —— 每家厂商一个独立 client，各自一个池。
const (
	dialTimeout           = 10 * time.Second
	keepAlive             = 30 * time.Second
	tlsHandshakeTimeout   = 10 * time.Second
	responseHeaderTimeout = 90 * time.Second
	idleConnTimeout       = 90 * time.Second
	expectContinueTimeout = time.Second
	maxIdleConns          = 32
	maxIdleConnsPerHost   = 16

	// Protocol framing needs more room than the retained model reply. Both are
	// memory safety limits, independent of model token counts and time budgets.
	maxProviderResponseBytes int64 = 64 << 20
	maxModelReplyBytes             = 8 << 20
)

var errResponseTooLarge = errors.New("ai: provider response exceeds byte limit")

// limitedHTTPClient bounds decoded HTTP bodies before an SDK can buffer an
// unterminated SSE line, error payload or JSON value. The original client still
// owns its connection pool and is closed by the module assembly.
type limitedHTTPClient struct {
	client   *http.Client
	maxBytes int64
}

func (c *limitedHTTPClient) Do(req *http.Request) (*http.Response, error) {
	// #nosec G704 -- SDK 依据部署者配置的固定 BaseURL 构造请求；HTTP DTO 不接受目标 URL。
	response, err := c.client.Do(req)
	if err == nil && response.Body != nil {
		response.Body = &limitedResponseBody{ReadCloser: response.Body, remaining: c.maxBytes}
	}
	return response, err
}

type limitedResponseBody struct {
	io.ReadCloser
	remaining int64
	failure   error
}

func (b *limitedResponseBody) Read(p []byte) (int, error) {
	if b.failure != nil {
		return 0, b.failure
	}
	if len(p) == 0 {
		return 0, nil
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining+1] // One extra byte distinguishes exact size from truncation.
	}
	n, err := b.ReadCloser.Read(p)
	if int64(n) > b.remaining {
		allowed := int(b.remaining)
		b.remaining, b.failure = 0, errResponseTooLarge
		// EOF is a normal ending; including it would let an SDK misclassify
		// overflow as successful completion. Preserve other read failures.
		if err != nil && err != io.EOF {
			b.failure = errors.Join(b.failure, err)
		}
		return allowed, b.failure
	}
	b.remaining -= int64(n)
	return n, err
}

// newHTTPClient 构造一家厂商专用的出站 HTTP 客户端。
//
// **http.Client.Timeout 刻意留零**。它的语义是"从发起请求到读完响应体的总时长"，
// 而流式对话的响应体要读几十秒到几分钟——设上它等于给每个流一个硬上限，
// 到点直接断开且没有任何可读的错误。预算统一由 ctx 表达（非流式
// request_timeout、流式 stream_total_timeout），两种调用形态因此有一致语义，
// 取消也能一路传到底层连接。
//
// 传输层的三个超时（dial / TLS / response header）是留零 Timeout 之后的必需
// 补偿：没有它们，一个建连就卡住的对端会把 goroutine 与连接一起无限期扣住，
// 而 ctx 的 deadline 只在**流式**路径上足够长到无法及时兜住。
//
// 不复用 internal/pkg/httpclient 的总超时与普通 JSON 限额。AI 在 SDK 读取
// 之前单独限制协议字节，适配器另限制正文加推理的累计字节。
//
// 每家厂商一个 client：连接池按 host 隔离，一家厂商的慢响应不该占用另一家的
// 连接配额；同一家的多次调用则复用同一个池。
func newHTTPClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			// 走环境里的代理设置：国内部署访问部分厂商需要经代理，
			// 而那是部署环境的事实，不该要求改配置文件。
			Proxy: http.ProxyFromEnvironment,
			DialContext: (&net.Dialer{
				Timeout:   dialTimeout,
				KeepAlive: keepAlive,
			}).DialContext,
			ForceAttemptHTTP2:     true,
			MaxIdleConns:          maxIdleConns,
			MaxIdleConnsPerHost:   maxIdleConnsPerHost,
			IdleConnTimeout:       idleConnTimeout,
			TLSHandshakeTimeout:   tlsHandshakeTimeout,
			ResponseHeaderTimeout: responseHeaderTimeout,
			ExpectContinueTimeout: expectContinueTimeout,
		},
	}
}
