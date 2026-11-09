package outbound

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gtkit/gin-api/internal/pkg/errx"

	"github.com/gtkit/httpc"
)

// maxRetryAttempts 是允许声明的重试次数上限。
//
// 重试放大流量：N 次重试意味着下游故障期间它承受的请求量变成 N+1 倍。
// 一个已经在超时边缘的下游，正是被这份放大推过临界点的。
const maxRetryAttempts = 3
const maxRetryWait = 30 * time.Second

// RetryPolicy 是重试节奏。
//
// 退避区间必须有上界：没有上界的指数退避会让最后一次重试等到调用方
// 早已放弃之后才发出——那次请求对谁都没有意义，只是白白打了一次下游。
type RetryPolicy struct {
	// Attempts 是首次失败之后的最多重试次数，取值 1..maxRetryAttempts。
	// 成功、不可重试、取消或累计等待预算可使实际次数减少。
	Attempts int
	// WaitTime 是首次重试前的等待。
	WaitTime time.Duration
	// MaxWaitTime 是本地退避上界；0 使用 30s。服务端等待不被它截短，
	// 但单次与累计等待均不能超过私有 30s 预算。
	MaxWaitTime time.Duration
}

// validate 拒绝不安全的重试参数。
func (p RetryPolicy) validate() error {
	if p.Attempts < 1 || p.Attempts > maxRetryAttempts {
		return fmt.Errorf(
			"httpclient: retry attempts %d out of range 1..%d: more retries turn one downstream "+
				"hiccup into an N-fold traffic amplification", p.Attempts, maxRetryAttempts)
	}
	if p.WaitTime < 0 || p.MaxWaitTime < 0 || p.WaitTime > maxRetryWait || p.MaxWaitTime > maxRetryWait {
		return fmt.Errorf("httpclient: retry waits must be between zero and %s", maxRetryWait)
	}
	if p.MaxWaitTime > 0 && p.WaitTime > p.MaxWaitTime {
		return fmt.Errorf(
			"httpclient: retry wait %s exceeds max wait %s", p.WaitTime, p.MaxWaitTime)
	}
	return nil
}

// RetryableFailure 判定一次失败是否值得重试。
//
// **有明确 HTTP 回答时以状态码为准**，err 只在没有状态码时说了算：下游回了
// 400 而响应体解码失败，这是一次"请求本身不对"，重试它不会让参数变对，
// 只会把一次客户端错误放大成 N 次，还把它计进熔断——用一个与下游健康无关
// 的信号去熔断一个健康的下游。err 优先的判定曾把这类组合全部判成可重试。
//
// 导出是为了让使用方在自定义调用编排时复用同一份判据——判据散落在各处，
// 迟早会有一处把 4xx 也重试掉。
func RetryableFailure(status int, err error) bool {
	// 调用方取消/超时：请求已被放弃，没人在等它的结果，重试只是白打一次下游。
	if err != nil && errx.IsPureAbort(err) {
		return false
	}

	if status > 0 {
		switch {
		case status == http.StatusTooManyRequests:
			// 被限流：退避后重试正是对方要求的行为。
			return true
		case status == http.StatusNotImplemented:
			// 501 是"这个下游根本不做这件事"，重试多少次都一样。
			return false
		case status >= http.StatusInternalServerError:
			return true
		default:
			// 2xx/3xx/4xx：下游给出了明确回答。即便随后的解码失败，
			// 重发同一个请求也只会得到同一个回答。
			return false
		}
	}

	// 没有状态码 = 没拿到回答：连接被拒、TLS 失败、响应没回来。可能只是一次
	// 抖动。注意它涵盖"请求已发出但响应没回来"——对非幂等请求正是最危险的
	// 一类，所以本判据的安全性完全依赖调用方只对幂等调用启用重试。
	return err != nil
}

// retryAttempts 以指数退避执行 attempt，直到成功、不可重试或次数用尽。
//
// 退避等待可被 ctx 取消：取消时返回最后一次尝试的错误与 ctx 错误的聚合——
// 真实的下游失败不能因为"调用方在退避期放弃了"而被抹掉（熔断的失败判据
// 依赖它：errx.IsPureAbort 对聚合错误只在**全部**成员都是取消时才判真）。
func retryAttempts(ctx context.Context, retry RetryPolicy, client *httpc.Client, attempt Attempt) (int, error) {
	result := runRetries(ctx, retry, func(ctx context.Context) retryResult {
		metadata := new(retryMetadata)
		status, err := attempt(context.WithValue(ctx, retryMetadataKey{}, metadata), client)
		return retryResult{status: status, err: err, retryAfter: metadata.read()}
	}, defaultRetryTiming())
	return result.status, result.err
}

// retryAbandoned 把"最后一次真实失败"与调用方取消聚合返回。
//
// 真实的下游失败不能因为调用方在退避期放弃而被抹掉：熔断的失败判据依赖它
// （errx.IsPureAbort 对聚合错误只在**全部**成员都是取消时才判真）。
func retryAbandoned(last, ctxErr error) error {
	if last == nil {
		return fmt.Errorf("httpclient: retry abandoned: %w", ctxErr)
	}
	return fmt.Errorf("httpclient: retry abandoned: %w", errors.Join(last, ctxErr))
}
