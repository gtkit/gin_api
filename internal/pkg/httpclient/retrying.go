package httpclient

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

// validate 拒绝不安全的重试参数。
func (p RetryPolicy) validate() error {
	if p.Attempts < 1 || p.Attempts > maxRetryAttempts {
		return fmt.Errorf(
			"httpclient: retry attempts %d out of range 1..%d: more retries turn one downstream "+
				"hiccup into an N-fold traffic amplification", p.Attempts, maxRetryAttempts)
	}
	if p.MaxWaitTime > 0 && p.WaitTime > p.MaxWaitTime {
		return fmt.Errorf(
			"httpclient: retry wait %s exceeds max wait %s", p.WaitTime, p.MaxWaitTime)
	}
	return nil
}

// RetryableFailure 判定一次失败是否值得重试。
//
// 导出是为了让使用方在自定义调用编排时复用同一份判据——判据散落在各处，
// 迟早会有一处把 4xx 也重试掉。
func RetryableFailure(status int, err error) bool {
	if err != nil {
		// 纯粹的取消/超时来自调用方：请求已被放弃，重试它只是给一个
		// 没人等结果的调用再打一次下游。
		if errx.IsPureAbort(err) {
			return false
		}
		// 其余传输层错误（连接被拒、TLS 失败、响应没回来）：可能只是一次
		// 抖动。注意"请求已发出但响应没回来"对非幂等请求正是最危险的一类，
		// 所以本判据的安全性完全依赖调用方只对幂等调用启用重试。
		return true
	}

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
		// 4xx 是请求本身的问题（参数错、没权限、找不到）。重试不会让它
		// 变对，只是把一次客户端错误放大成 N 次。
		return false
	}
}

// retryAttempts 以指数退避执行 attempt，直到成功、不可重试或次数用尽。
//
// 退避等待可被 ctx 取消：取消时返回最后一次尝试的错误与 ctx 错误的聚合——
// 真实的下游失败不能因为"调用方在退避期放弃了"而被抹掉（熔断的失败判据
// 依赖它：errx.IsPureAbort 对聚合错误只在**全部**成员都是取消时才判真）。
func retryAttempts(ctx context.Context, retry RetryPolicy, client *httpc.Client, attempt Attempt) (int, error) {
	wait := retry.WaitTime
	var status int
	var err error
	for try := 0; ; try++ {
		status, err = attempt(ctx, client)
		if !RetryableFailure(status, err) || try >= retry.Attempts {
			return status, err
		}

		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-ctx.Done():
				timer.Stop()
				if err == nil {
					return status, fmt.Errorf("httpclient: retry wait canceled: %w", ctx.Err())
				}
				return status, fmt.Errorf("httpclient: retry wait canceled: %w", errors.Join(err, ctx.Err()))
			case <-timer.C:
			}
		}

		wait *= 2
		if retry.MaxWaitTime > 0 && wait > retry.MaxWaitTime {
			wait = retry.MaxWaitTime
		}
	}
}
