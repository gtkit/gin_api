package ai

import (
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
)

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
// **不复用 internal/pkg/httpclient**：那套 httpc.Client 给每个下游设总超时与
// 响应体字节上限，两者对 SSE 长连接都是错的语义——响应体上限会把一个正常的
// 长回复判成异常。名字相同、语义不同的复用比不复用更危险。
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
