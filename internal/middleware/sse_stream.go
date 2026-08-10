package middleware

import (
	"context"
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
// 并发约定与 ssex.Stream 一致：它内部用互斥锁串行化写方法，因此可从多个
// goroutine 写入同一连接。
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

// Start 发出响应头。
func (s *SSEStream) Start() error { return s.write("start", s.inner.Start) }

// Started 报告响应头是否已发出。
func (s *SSEStream) Started() bool { return s.inner.Started() }

// Context 返回该流的上下文，客户端断开时它被取消。
func (s *SSEStream) Context() context.Context { return s.inner.Context() }

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

// Heartbeat 按固定间隔自动心跳直到 ctx 取消。
//
// 它在 ssex 内部循环写出，每一拍都要续期——否则空闲预算会在两拍之间耗尽，
// 而心跳的全部意义就是维持这条连接。
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
	if err := fn(); err != nil {
		return fmt.Errorf("middleware: SSE %s: %w", op, err)
	}
	if err := RenewStreamDeadline(s.c); err != nil {
		return fmt.Errorf("middleware: renew stream deadline after %s: %w", op, err)
	}
	return nil
}
