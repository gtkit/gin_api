package middleware

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/ssex"
)

// SSEStream 把 gtkit/ssex 的写出与本仓的 socket 写 deadline 续期绑在一起。
//
// **SSE 协议本身一行都不在这里实现**——组帧、id/event/data 行、多行载荷拆分、
// 心跳、Last-Event-ID、并发串行化全部由 ssex.Stream 负责。本类型只做一件事：
// 每次写出之后续期连接的写 deadline。
//
// 为什么必须有这一层：HTTP server 设了 60s 的全局写超时兜底（见 bootstrap 的
// serverWriteTimeout），流式路由要靠 SetWriteDeadline 覆盖它。ssex 的
// WithWriteTimeout 是**应用层**的写操作超时，管不到 socket 层的那个 deadline；
// 而本仓的 StreamTimeout 中间件不认识 ssex，不知道它什么时候写出了数据。
// 两边各自都是完整的，缺的正是这条连接——忘了续期的表现是"SSE 连接 60 秒后
// 莫名断开"，没有错误、没有日志，handler 侧完全看不出发生了什么。
//
// 写方法逐个显式转发而**不内嵌** *ssex.Stream：内嵌会让未覆盖的方法绕过续期，
// 而"绕过"在这里是静默的——正是这一层要消灭的失效形态。
//
// 并发与生命周期约定（两条都来自 ssex.Stream，缺一条就会出事）：
//
//	并发——内部用互斥锁串行化写方法，可从多个 goroutine 并发调用。
//	生命周期——**后台写入的 goroutine 必须在 handler 返回前退出**。
//	  底层 http.ResponseWriter 只在 handler 执行期间有效：gin 的 c.Writer 是
//	  Context 的内部字段，handler 返回后归还对象池，下个请求会把它重置到
//	  另一个响应上。晚退出一步，写的就是**别人的响应**——一次跨请求的数据
//	  泄漏，而且不会报错。
//
// 因此心跳要么在 handler goroutine 里同步调用 Heartbeat（它阻塞到 ctx 结束），
// 要么起 goroutine 但用 errgroup/WaitGroup 保证 handler 返回前它已经退出。
type SSEStream struct {
	inner *ssex.Stream
	c     *gin.Context
}

// NewSSEStream 在当前请求上建立 SSE 流。
//
// 路由需挂 StreamTimeout 才有可续期的空闲预算；未挂时续期是空操作
// （RenewStreamDeadline 返回 nil），此时受 60s 全局兜底约束。
func NewSSEStream(c *gin.Context, opts ...ssex.Option) *SSEStream {
	return &SSEStream{inner: ssex.NewStream(c.Writer, c.Request, opts...), c: c}
}

// ErrNoStream 表示在 nil 流上写入。
//
// 返回错误而不是 panic：SSE handler 常在循环里写，一次 panic 会连带
// recover 中间件把整条连接终止，而调用方本可以据错误正常收尾。
var ErrNoStream = errors.New("middleware: SSE stream is nil")

// Start 发出响应头。
//
// 用闭包而不是方法值 s.inner.Start：方法值在**传参时**就解引用 s.inner，
// 早于 write 里的 nil 检查——nil 流会在这里 panic 而不是拿到 ErrNoStream。
func (s *SSEStream) Start() error {
	return s.write("start", func() error { return s.inner.Start() })
}

// Started 报告响应头是否已发出。
func (s *SSEStream) Started() bool { return s != nil && s.inner.Started() }

// Context 返回该流的上下文，客户端断开时它被取消。
//
// nil 流返回一个已取消的 ctx：调用方的 select 循环因此立即退出，
// 而不是永久阻塞在一个永远不会就绪的 channel 上。
func (s *SSEStream) Context() context.Context {
	if s == nil {
		return canceledContext()
	}
	return s.inner.Context()
}

// canceledContext 返回一个已取消的 context。
func canceledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// Data 写出一个 data-only 事件。
func (s *SSEStream) Data(payload any) error {
	return s.write("data", func() error { return s.inner.Data(payload) })
}

// Event 写出一个具名事件。
func (s *SSEStream) Event(name string, payload any) error {
	return s.write("event", func() error { return s.inner.Event(name, payload) })
}

// EventWithID 写出带 id 的具名事件，供客户端断线重连时回传 Last-Event-ID。
func (s *SSEStream) EventWithID(id, name string, payload any) error {
	return s.write("event", func() error { return s.inner.EventWithID(id, name, payload) })
}

// Send 写出一个完整事件。
func (s *SSEStream) Send(e ssex.Event) error {
	return s.write("send", func() error { return s.inner.Send(e) })
}

// Comment 写出一行注释（不触发客户端的 message 回调）。
func (s *SSEStream) Comment(text string) error {
	return s.write("comment", func() error { return s.inner.Comment(text) })
}

// Ping 写出一次心跳。
func (s *SSEStream) Ping(at time.Time) error {
	return s.write("ping", func() error { return s.inner.Ping(at) })
}

// Retry 告知客户端重连间隔。
func (s *SSEStream) Retry(milliseconds int) error {
	return s.write("retry", func() error { return s.inner.Retry(milliseconds) })
}

// Error 写出一个 error 事件。
func (s *SSEStream) Error(payload any) error {
	return s.write("error", func() error { return s.inner.Error(payload) })
}

// Close 写出收尾事件并结束流。
func (s *SSEStream) Close(payload any) error {
	return s.write("close", func() error { return s.inner.Close(payload) })
}

// Heartbeat 按固定间隔自动心跳，直到 ctx 取消或客户端断开。
//
// 每一拍都续期——否则空闲预算会在两拍之间耗尽，而心跳的全部意义就是维持
// 这条连接。ssex 自己的 Heartbeat 做不到这一点：它的内部循环插不进续期。
//
// 与 ssex 的两处差异：
//
//	正常收尾返回 nil 而不是 ctx.Err()——ctx 到期是**预期**的结束方式，
//	  返回错误会让每次正常收尾都在调用方那里记一条错误日志。
//	额外监听客户端断开（stream 自身的 ctx），断开即退出；否则会继续打到
//	  某一拍写失败为止，那期间的每一拍都是白做的。
//
// **它阻塞到结束**：这正是安全的用法——在 handler goroutine 里同步调用，
// 天然满足"后台写入必须在 handler 返回前退出"。
func (s *SSEStream) Heartbeat(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return fmt.Errorf("middleware: SSE heartbeat interval must be positive")
	}

	// 不直接调 ssex 的 Heartbeat：它的内部循环无法在每拍之后插入续期。
	// 自己驱动节拍，每拍走 Ping（因此每拍都过 write 的续期）。
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-s.Context().Done():
			return nil
		case at := <-ticker.C:
			if err := s.Ping(at); err != nil {
				return err
			}
		}
	}
}

// write 执行一次写出并续期 socket 写 deadline。
//
// 顺序不能反：续期是为"下一次写"争取空闲预算，而本次写出必须先真的发生。
// 反过来做会在写出阻塞时用掉刚续的那份预算。
func (s *SSEStream) write(op string, fn func() error) error {
	if s == nil {
		return fmt.Errorf("%w: %s", ErrNoStream, op)
	}
	if err := fn(); err != nil {
		return fmt.Errorf("middleware: SSE %s: %w", op, err)
	}
	if err := RenewStreamDeadline(s.c); err != nil {
		return fmt.Errorf("middleware: renew stream deadline after %s: %w", op, err)
	}
	return nil
}
