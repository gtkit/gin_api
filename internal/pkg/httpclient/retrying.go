package httpclient

import (
	"fmt"
	"net/http"
	"time"

	"github.com/gtkit/gin-api/internal/platform/config"

	"github.com/go-resty/resty/v2"
	"github.com/gtkit/restyx"
)

// maxRetryAttempts 是允许声明的重试次数上限。
//
// 重试放大流量：N 次重试意味着下游故障期间它承受的请求量变成 N+1 倍。
// 一个已经在超时边缘的下游，正是被这份放大推过临界点的。
const maxRetryAttempts = 3

// RetryPolicy 是重试节奏。
//
// 退避区间必须有上界：没有上界的指数退避会让最后一次重试等到调用方
// 早已放弃之后才发出——那次请求对谁都没有意义，只是白白打了一次下游。
type RetryPolicy struct {
	// Attempts 是**首次失败之后**的重试次数，取值 1..maxRetryAttempts。
	Attempts int
	// WaitTime 是首次重试前的等待。
	WaitTime time.Duration
	// MaxWaitTime 是退避的上界。
	MaxWaitTime time.Duration
}

// NewRetrying 构造一个带重试的出站客户端。
//
// **它只能用于幂等请求。** resty 的重试是客户端级的：这个客户端发出的每一个
// 请求都会重试，包括 POST。把一个下单接口挂到它上面，一次网络抖动就会变成
// 两笔订单——而且失败发生在响应回程时，调用方连"到底成没成"都不知道。
//
// 因此约定是：**按幂等性分客户端**，而不是按下游分。同一个下游的幂等只读
// 端点走这个客户端，写端点走 Factory 给的普通客户端（无重试），由业务自己
// 用幂等键处理重放。
//
// 与熔断的分工：重试处理**偶发**失败（一次抖动、一台后端实例挂了），熔断
// 处理**持续**失败（整个下游不可用）。两者必须一起用——只有重试，下游全挂时
// 每个请求都会重试满次数，等于给一个已经倒下的依赖再加 N 倍压力。
// 正确的嵌套是熔断在外、重试在内：熔断跳闸后连重试都不该发生。
func NewRetrying(cfg config.OutboundClientConfig, retry RetryPolicy) (*restyx.Client, error) {
	resolved := cfg.Resolved(config.OutboundClientConfig{})
	if resolved.BaseURL == "" {
		return nil, fmt.Errorf("httpclient: retrying client has no base_url")
	}
	if retry.Attempts < 1 || retry.Attempts > maxRetryAttempts {
		return nil, fmt.Errorf(
			"httpclient: retry attempts %d out of range 1..%d: more retries turn one downstream "+
				"hiccup into an N-fold traffic amplification", retry.Attempts, maxRetryAttempts)
	}
	if retry.MaxWaitTime > 0 && retry.WaitTime > retry.MaxWaitTime {
		return nil, fmt.Errorf(
			"httpclient: retry wait %s exceeds max wait %s", retry.WaitTime, retry.MaxWaitTime)
	}

	return restyx.New(
		restyx.WithBaseURL(resolved.BaseURL),
		// 总超时是**每次尝试**的超时，不是全部重试的总预算——
		// 最坏情况下调用耗时约为 (Attempts+1) × Timeout 加上退避。
		// 排端点超时预算时按这个算，不要按 Timeout 算。
		restyx.WithTimeout(resolved.Timeout),
		restyx.WithRetry(retry.Attempts, retry.WaitTime, retry.MaxWaitTime),
		restyx.WithRetryCondition(RetryableFailure),
	), nil
}

// RetryableFailure 判定一次失败是否值得重试。
//
// 导出是为了让使用方在自定义客户端时复用同一份判据——判据散落在各处，
// 迟早会有一处把 4xx 也重试掉。
func RetryableFailure(resp *resty.Response, err error) bool {
	// 传输层错误（连接被拒、TLS 失败、读超时）：可能只是一次抖动。
	// 注意它也涵盖"请求已发出但响应没回来"——对非幂等请求正是最危险的
	// 那一类，所以本判据的安全性完全依赖调用方遵守"只挂幂等端点"。
	if err != nil {
		return true
	}
	if resp == nil {
		return false
	}

	switch status := resp.StatusCode(); {
	case status == http.StatusTooManyRequests:
		// 被限流：退避后重试正是对方要求的行为。
		return true
	case status == http.StatusNotImplemented:
		// 501 是"这个下游根本不做这件事"，重试多少次都一样。
		return false
	case status >= http.StatusInternalServerError:
		return true
	default:
		// 4xx 是请求本身的问题（参数错、没权限、找不到）。重试不会让它
		// 变对，只是把一次客户端错误放大成 N 次。
		return false
	}
}
