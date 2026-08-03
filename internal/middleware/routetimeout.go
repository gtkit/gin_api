package middleware

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	resp "github.com/gtkit/gin-api/internal/pkg/response"
	"github.com/gtkit/gin-api/internal/router/routespec"

	"github.com/gin-contrib/timeout"
	"github.com/gin-gonic/gin"
	logger "github.com/gtkit/logger/v2"
	"go.uber.org/zap"
)

// RouteTimeout 按路由的 TimeoutPolicy 返回对应的超时中间件。
//
// **这套机制是协作式的错误传播，不是强制终止器。** 三条必须写清楚的边界：
//
//  1. 业务 deadline 到点只**取消 ctx**。忽略 ctx 的 handler 会继续占用 goroutine
//     直到它自己返回——没有任何机制能从外部终止一个 Go 函数。
//  2. socket write deadline 到点只承诺**解除写阻塞 / 断连**，不承诺返回任何状态码，
//     更不能终止 CPU 工作。
//  3. 只有调用链**真的观察 ctx 并返回 DeadlineExceeded** 时，终态中间件才会写 504。
//
// 文档、注释与门禁都不得声称"ctx 到点必然 504"或"write deadline 能终止 goroutine"。
func RouteTimeout(policy routespec.TimeoutPolicy) (gin.HandlerFunc, error) {
	switch policy.Mode {
	case routespec.TimeoutCooperative:
		return cooperativeTimeout(policy), nil
	case routespec.TimeoutClientVisible:
		return clientVisibleTimeout(policy), nil
	case routespec.TimeoutStreaming:
		return streamingTimeout(policy), nil
	case routespec.TimeoutUnspecified:
		return nil, errors.New("route timeout policy is unspecified")
	default:
		return nil, fmt.Errorf("unknown timeout mode %d", policy.Mode)
	}
}

// cooperativeTimeout 施加两级预算：业务 ctx deadline 与更晚的 socket 写 deadline。
//
// 两级缺一不可，且不能合并成一级：标准库明确 —— write deadline 一旦过期，
// 这条连接的写就废了，**无法再可靠写出 504**。所以负责回 504 的必须是更短的
// 业务预算（经协作式 ctx），socket deadline 只兜住慢客户端或失控 handler 的写阻塞。
func cooperativeTimeout(policy routespec.TimeoutPolicy) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), policy.Deadline)
		defer cancel()
		c.Request = c.Request.WithContext(ctx)

		restore := applyWriteDeadline(c, policy.Deadline+policy.WriteGrace)
		defer restore()

		c.Next()
	}
}

// clientVisibleTimeout 用 gin-contrib/timeout 给客户端一个可见的延迟上界。
//
// 它到点先写 504，但随后**仍会等待 handler goroutine**，且全程缓存响应体。
// 因此它只提供"客户端可见的 deadline"，不提供强制取消、资源上界或关停上界。
//
// socket deadline 必须在**进入该 wrapper 之前**设置：wrapper 自身的 writer
// 缓存响应且不透明，进去之后 ResponseController 拿不到底层连接。
// 同理，本模式明确禁止流式与 Hijack（由 routespec 的方法约束与本注释共同约束）。
func clientVisibleTimeout(policy routespec.TimeoutPolicy) gin.HandlerFunc {
	inner := timeout.New(
		timeout.WithTimeout(policy.Deadline),
		timeout.WithResponse(timeoutResponse),
	)

	return func(c *gin.Context) {
		restore := applyWriteDeadline(c, policy.Deadline+policy.WriteGrace)
		defer restore()

		inner(c)
	}
}

// streamingTimeout 施加**可续期**的写空闲超时，不设总时长上限。
//
// 与固定的 WriteTimeout 的差别正是关键：后者是"从响应开始"的绝对上限，
// 一条健康的长连接同样会被它切断；前者只在**对端不再收数据**时生效。
//
// 续期由 StreamKeepAlive 在每次成功 flush 后调用，必须发生在过期**之前**——
// 标准库约定 write deadline 过期后再设置不能延长它。心跳间隔严格小于
// IdleWrite 由 routespec 在注册期强制。
func streamingTimeout(policy routespec.TimeoutPolicy) gin.HandlerFunc {
	return func(c *gin.Context) {
		restore := applyWriteDeadline(c, policy.IdleWrite)
		defer restore()

		// 把续期入口放进 context，供流式 handler 每次 flush 后调用。
		c.Set(streamRenewKey, func() error { return renewWriteDeadline(c, policy.IdleWrite) })

		c.Next()
	}
}

// streamRenewKey 是流式续期函数在 gin.Context 中的键。
const streamRenewKey = "route.stream.renew"

// RenewStreamDeadline 续期当前流式响应的写空闲超时。
//
// 流式 handler 应在**每次成功 flush 之后**调用。非流式路由上调用返回 nil
// （没有可续期的空闲预算，不是错误）。
func RenewStreamDeadline(c *gin.Context) error {
	v, ok := c.Get(streamRenewKey)
	if !ok {
		return nil
	}
	renew, ok := v.(func() error)
	if !ok {
		return nil
	}
	return renew()
}

// applyWriteDeadline 设置连接级写 deadline，并返回正常结束时的清理函数。
//
// **清理是必需的**：SetWriteDeadline 作用于底层 keep-alive 连接，不是只作用于
// 当前请求。不清零的话，第一条路由的短 deadline 会污染同一连接上的下一个请求——
// 一个 200ms 预算的探针接口之后紧跟一个 30s 的业务接口，后者会在 200ms 处写失败。
//
// 已过期时不再清零：连接的写已经废了，重置只会掩盖真实状态。
func applyWriteDeadline(c *gin.Context, budget time.Duration) func() {
	if budget <= 0 {
		return func() {}
	}

	deadline := time.Now().Add(budget)
	rc := http.NewResponseController(c.Writer)
	if err := rc.SetWriteDeadline(deadline); err != nil {
		// 不支持时降级为无 socket deadline，但必须留痕：
		// 静默忽略会让"保护配了但没生效"完全不可见。
		// 错误按固定原因记录，不带请求路径等高基数字段。
		logWriteDeadlineIssue("set", err)
		return func() {}
	}

	return func() {
		if time.Now().After(deadline) {
			// 已过期：这条连接的写已经废了，重置无意义且会掩盖真实状态。
			return
		}
		if err := rc.SetWriteDeadline(time.Time{}); err != nil {
			logWriteDeadlineIssue("clear", err)
		}
	}
}

// renewWriteDeadline 把写 deadline 向后推一个空闲预算。
func renewWriteDeadline(c *gin.Context, idle time.Duration) error {
	rc := http.NewResponseController(c.Writer)
	if err := rc.SetWriteDeadline(time.Now().Add(idle)); err != nil {
		return fmt.Errorf("renew stream write deadline: %w", err)
	}
	return nil
}

// logWriteDeadlineIssue 以**固定原因**记录写 deadline 操作失败。
//
// 只记 op 与错误，不记路径/方法等高基数字段：这条日志在慢客户端场景下
// 可能高频出现，带上高基数字段会让日志后端的索引失控。
func logWriteDeadlineIssue(op string, err error) {
	logger.Warn("write deadline operation failed",
		zap.String("op", op), zap.Error(err))
}

// timeoutResponse 是 ClientVisible 模式到点时写给客户端的响应。
func timeoutResponse(c *gin.Context) {
	respondGatewayTimeout(c)
}

// IsTimeoutError 报告错误是否为业务 deadline 到点。
//
// 只认 context.DeadlineExceeded：客户端断开（context.Canceled）不是超时，
// 而且**不该写任何响应**——连接已经没了，写只会产生一条无意义的错误日志。
func IsTimeoutError(err error) bool {
	return err != nil && errors.Is(err, context.DeadlineExceeded)
}

// IsClientGone 报告错误是否为客户端主动断开。
func IsClientGone(err error) bool {
	return err != nil && errors.Is(err, context.Canceled)
}

// respondGatewayTimeout 写 504 并中止链路。
func respondGatewayTimeout(c *gin.Context) {
	resp.GatewayTimeoutErr(c, "request timed out")
	c.Abort()
}
