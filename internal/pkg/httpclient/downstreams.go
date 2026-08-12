// downstreams.go 是出站调用的具名执行入口（outbound-http-resilience spec）。
//
// 它把此前三套各自孤立的能力收敛到一个装配点：具名客户端（连接池、总超时、
// 响应体上限）、熔断（按下游名登记）、幂等重试（有界退避）。收敛的意义在于
// 组合关系一次定死——熔断在外、重试在内、重试复用同一个客户端——而不是
// 让每个调用点自己拼装三件事，拼错任何一处都没有编译期迹象。
package httpclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/gtkit/gin-api/internal/pkg/breaker"
	"github.com/gtkit/gin-api/internal/platform/config"

	"github.com/gtkit/httpc"
)

// ErrDownstreamServerError 表示下游以 5xx 状态码结束了本次调用。
//
// httpc 把 5xx 留给调用方判断（它们是"下游的回答"，不是传输失败）；
// 但对执行入口而言，一个持续返回 500 的下游必须能把熔断器打开——
// 归一化成错误是让状态码进入熔断统计的唯一通道。用 errors.Is 判别。
var ErrDownstreamServerError = errors.New("httpclient: downstream returned a server error status")

// Attempt 是一次下游调用的执行体。
//
// status 是本次调用的 HTTP 状态码（没有响应时为 0），err 是传输层错误。
// 执行体拿到的 client 就是该下游在工厂里的那个实例——连接池、总超时与
// 响应体上限全部随之生效，不存在"重试路径用了另一个没有防护的客户端"。
type Attempt func(ctx context.Context, client *httpc.Client) (status int, err error)

// Downstreams 是全部具名下游的执行入口，构造后不可变、并发安全。
//
// 下游集合是启动期事实：客户端与熔断实例按同一批名字在构造时建好，
// 运行期只能按名字取用，取不到就是编码错误。
type Downstreams struct {
	factory  *Factory
	breakers *breaker.Registry
}

// NewDownstreams 按出站配置构造执行入口。
//
// 客户端与熔断实例用**同一批下游名**：两者名字不一致时，"这个下游的连接池"
// 与"这个下游的熔断器"会各指一处，而那正是分开装配时最容易犯的错。
// Clients 为空时返回空入口：任何按名取用都会得到 ErrUnknownClient。
func NewDownstreams(cfg config.OutboundConfig) (*Downstreams, error) {
	factory, err := New(cfg)
	if err != nil {
		return nil, err
	}
	registry := breaker.NewRegistry(breaker.Config{
		ConsecutiveFailures: cfg.Breaker.ConsecutiveFailures,
		OpenTimeout:         cfg.Breaker.OpenTimeout,
		HalfOpenMaxRequests: cfg.Breaker.HalfOpenMaxRequests,
	}, factory.Names()...)
	return &Downstreams{factory: factory, breakers: registry}, nil
}

// Do 在名为 name 的熔断实例内执行一次下游调用。
//
// 熔断开启时 attempt 不执行、立即返回熔断错误（用 breaker.IsOpen 判别，
// 此时应走降级而不是重试）。attempt 的 err 按熔断的失败判据计入；err 为 nil
// 而状态码为 5xx 时归一化为 ErrDownstreamServerError 并同样计入。
// 返回的 status 是最后一次真实调用的状态码（未发生调用时为 0）。
func (d *Downstreams) Do(ctx context.Context, name string, attempt Attempt) (int, error) {
	return d.execute(ctx, name, normalized(name, attempt))
}

// DoIdempotent 同 Do，但对可重试的失败按 retry 做有界退避重试。
//
// **它只能用于幂等调用。** 重试的判据涵盖"请求已发出但响应没回来"——
// 对非幂等请求正是最危险的一类：一次网络抖动会变成两笔订单。写调用走 Do，
// 由业务自己用幂等键处理重放。
//
// 嵌套是熔断在外、重试在内：熔断跳闸后连第一次尝试都不该发生——只有重试
// 的话，下游全挂时每个请求都会重试满次数，等于给一个已经倒下的依赖再加
// N 倍压力。整轮重试对熔断计一次结果。
func (d *Downstreams) DoIdempotent(
	ctx context.Context, name string, retry RetryPolicy, attempt Attempt,
) (int, error) {
	if err := retry.validate(); err != nil {
		return 0, err
	}
	run := normalized(name, attempt)
	return d.execute(ctx, name, func(ctx context.Context, client *httpc.Client) (int, error) {
		return retryAttempts(ctx, retry, client, run)
	})
}

// normalized 把"下游以 5xx 回答"归一化为 ErrDownstreamServerError。
//
// 归一化必须发生在**每次尝试**的边界，而不是整轮重试之后：退避等待被取消时，
// 最后一次真实失败要与 ctx 错误聚合返回——归一化太晚，那次 5xx 就只剩一个
// 状态码，熔断的失败判据（只看错误）会把这轮当成"纯粹的取消"排除掉。
func normalized(name string, attempt Attempt) Attempt {
	return func(ctx context.Context, client *httpc.Client) (int, error) {
		status, err := attempt(ctx, client)
		if err == nil && status >= http.StatusInternalServerError {
			return status, fmt.Errorf("%w: %q returned %d", ErrDownstreamServerError, name, status)
		}
		return status, err
	}
}

// execute 是 Do 与 DoIdempotent 共用的骨架：取客户端 → 熔断内执行。
func (d *Downstreams) execute(
	ctx context.Context, name string, run func(context.Context, *httpc.Client) (int, error),
) (int, error) {
	client, err := d.factory.Client(name)
	if err != nil {
		return 0, err
	}

	var status int
	doErr := d.breakers.Do(name, func() error {
		s, runErr := run(ctx, client)
		status = s
		return runErr
	})
	return status, doErr
}

// Client 返回具名下游的原始客户端，供 Do/DoIdempotent 覆盖不了的编排
// （流式下载、自定义中间件）使用。同名多次取到同一实例。
//
// 注意它**不带熔断**：常规调用走 Do/DoIdempotent，别在调用点自己包一层。
func (d *Downstreams) Client(name string) (*httpc.Client, error) {
	return d.factory.Client(name)
}

// Names 返回已登记的下游名（有序），供启动日志与诊断使用。
func (d *Downstreams) Names() []string {
	if d == nil {
		return nil
	}
	return d.factory.Names()
}

// Close 释放全部下游客户端的空闲连接。幂等。
func (d *Downstreams) Close() error {
	if d == nil {
		return nil
	}
	return d.factory.Close()
}
