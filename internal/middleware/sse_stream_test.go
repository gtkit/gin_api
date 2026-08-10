package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/ssex"
)

// serveSSEStream 跑一次 SSE handler，返回响应与续期次数。
//
// renewErr 非 nil 时模拟续期失败。
func serveSSEStream(
	t *testing.T, renewErr error, body func(*SSEStream) error,
) (*httptest.ResponseRecorder, *int) {
	t.Helper()

	renewals := 0
	engine := gin.New()
	engine.GET("/stream", func(c *gin.Context) {
		// 模拟 StreamTimeout 挂上后的效果：注入一个可观测的续期函数。
		c.Set(streamRenewKey, func() error {
			renewals++
			return renewErr
		})
		if err := body(NewSSEStream(c)); err != nil && renewErr == nil {
			t.Errorf("handler body: %v", err)
		}
	})

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/stream", nil))
	return recorder, &renewals
}

// TestSSEStreamRenewsAfterEveryWrite 是本类型存在的理由。
//
// 全局写超时 60s 会掐断长连接，流式路由靠 SetWriteDeadline 覆盖它，而覆盖
// 需要每次写出后续期。忘了续期的表现是"SSE 连接 60 秒后莫名断开"——没有
// 错误、没有日志。因此**每个**写方法都必须续期，一个都不能漏。
func TestSSEStreamRenewsAfterEveryWrite(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		write func(*SSEStream) error
	}{
		{name: "Start", write: func(s *SSEStream) error { return s.Start() }},
		{name: "Data", write: func(s *SSEStream) error { return s.Data(map[string]int{"n": 1}) }},
		{name: "Event", write: func(s *SSEStream) error { return s.Event("tick", 1) }},
		{name: "EventWithID", write: func(s *SSEStream) error { return s.EventWithID("7", "tick", 1) }},
		{name: "Send", write: func(s *SSEStream) error { return s.Send(ssex.Event{Name: "tick", Data: 1}) }},
		{name: "Comment", write: func(s *SSEStream) error { return s.Comment("hb") }},
		{name: "Ping", write: func(s *SSEStream) error { return s.Ping(time.Unix(0, 0)) }},
		{name: "Retry", write: func(s *SSEStream) error { return s.Retry(3000) }},
		{name: "Error", write: func(s *SSEStream) error { return s.Error("boom") }},
		{name: "Close", write: func(s *SSEStream) error { return s.Close("bye") }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, renewals := serveSSEStream(t, nil, tt.write)
			if *renewals == 0 {
				t.Fatalf("%s 写出后没有续期：连接会在全局写超时到期时被掐断", tt.name)
			}
		})
	}
}

// TestSSEStreamSurfacesRenewFailure 钉住续期失败被上报而不是吞掉。
//
// 吞掉的后果是连接注定要断，而 handler 以为一切正常，继续为一个即将消失的
// 消费者干活。
func TestSSEStreamSurfacesRenewFailure(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("deadline not supported")
	var gotErr error
	engine := gin.New()
	engine.GET("/stream", func(c *gin.Context) {
		c.Set(streamRenewKey, func() error { return wantErr })
		gotErr = NewSSEStream(c).Data("x")
	})
	engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/stream", nil))

	if !errors.Is(gotErr, wantErr) {
		t.Fatalf("error = %v, want it to wrap %v", gotErr, wantErr)
	}
	if !strings.Contains(gotErr.Error(), "renew stream deadline") {
		t.Fatalf("错误 %q 没有指出是续期失败，排查时看不出原因", gotErr)
	}
}

// TestSSEStreamDelegatesProtocolToSsex 钉住协议细节确实由 ssex 产出。
//
// 本类型不实现任何 SSE 组帧——这里验的是委托生效，不是重新实现一遍格式断言。
func TestSSEStreamDelegatesProtocolToSsex(t *testing.T) {
	t.Parallel()

	recorder, _ := serveSSEStream(t, nil, func(s *SSEStream) error {
		return s.EventWithID("42", "tick", map[string]string{"k": "v"})
	})

	body := recorder.Body.String()
	for _, want := range []string{"id: 42", "event: tick", "data: "} {
		if !strings.Contains(body, want) {
			t.Fatalf("响应缺少 %q:\n%q", want, body)
		}
	}
	if got := recorder.Header().Get("Content-Type"); !strings.Contains(got, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", got)
	}
}

// TestSSEStreamHeartbeatRenewsEachTick 钉住心跳每一拍都续期。
//
// 直接用 ssex 的 Heartbeat 做不到：它的内部循环无法在每拍之后插入续期，
// 空闲预算会在两拍之间耗尽——而心跳的全部意义就是维持这条连接。
func TestSSEStreamHeartbeatRenewsEachTick(t *testing.T) {
	t.Parallel()

	_, renewals := serveSSEStream(t, nil, func(s *SSEStream) error {
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Millisecond)
		defer cancel()
		return s.Heartbeat(ctx, 20*time.Millisecond)
	})

	// 至少若干拍，每拍一次续期。不断言精确次数：定时器节拍数受调度影响，
	// 精确断言会把随机性写进门禁。
	if *renewals < 2 {
		t.Fatalf("心跳期间只续期 %d 次，want >= 2", *renewals)
	}
}

// TestSSEStreamHeartbeatRejectsNonPositiveInterval 钉住非正间隔被拒绝。
//
// 放行的后果是 time.NewTicker panic，或者一个永不触发的心跳——后者更坏，
// 它看起来在工作。
func TestSSEStreamHeartbeatRejectsNonPositiveInterval(t *testing.T) {
	t.Parallel()

	var gotErr error
	engine := gin.New()
	engine.GET("/stream", func(c *gin.Context) {
		gotErr = NewSSEStream(c).Heartbeat(context.Background(), 0)
	})
	engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/stream", nil))

	if gotErr == nil {
		t.Fatal("非正的心跳间隔被接受了")
	}
}

// TestNilSSEStreamIsSafe 钉住 nil 流不 panic。
//
// SSE handler 常在循环里写，一次 panic 会连带 recover 中间件把整条连接终止，
// 而调用方本可以据错误正常收尾。Context() 返回已取消的 ctx，让调用方的
// select 循环立即退出，而不是永久阻塞在一个永远不会就绪的 channel 上。
func TestNilSSEStreamIsSafe(t *testing.T) {
	t.Parallel()

	var s *SSEStream

	if err := s.Data("x"); !errors.Is(err, ErrNoStream) {
		t.Fatalf("Data on nil = %v, want ErrNoStream", err)
	}
	if err := s.Start(); !errors.Is(err, ErrNoStream) {
		t.Fatalf("Start on nil = %v, want ErrNoStream", err)
	}
	if s.Started() {
		t.Fatal("nil 流报告已开始")
	}

	ctx := s.Context()
	if ctx == nil {
		t.Fatal("nil 流的 Context() 返回 nil，调用方会 panic")
	}
	select {
	case <-ctx.Done():
	default:
		t.Fatal("nil 流的 ctx 未取消：调用方的 select 会永久阻塞")
	}

	// 心跳在 nil 流上必须立即返回，而不是空转 ticker。
	done := make(chan error, 1)
	go func() { done <- s.Heartbeat(context.Background(), 10*time.Millisecond) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("nil 流的 Heartbeat 没有退出")
	}
}

// TestSSEStreamRejectsWritesAfterClose 钉住 Close 之后拒绝写入，且错误可判别。
//
// EventSource 在服务端结束流后会自动重连，约定前端收到 close 事件后主动关闭。
// Close 之后若还能写，那些帧会发给一个正在关闭的消费者——而调用方需要能
// 用 errors.Is 区分"流已终止"与真实的写失败。
func TestSSEStreamRejectsWritesAfterClose(t *testing.T) {
	t.Parallel()

	var afterClose error
	engine := gin.New()
	engine.GET("/stream", func(c *gin.Context) {
		c.Set(streamRenewKey, func() error { return nil })
		s := NewSSEStream(c)
		if err := s.Close("bye"); err != nil {
			t.Errorf("Close: %v", err)
		}
		afterClose = s.Data("late")
	})
	engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/stream", nil))

	if afterClose == nil {
		t.Fatal("Close 之后仍然接受写入")
	}
	if !errors.Is(afterClose, ssex.ErrStreamClosed) {
		t.Fatalf("error = %v, want it to wrap ssex.ErrStreamClosed（调用方需要能区分流已终止与真实写失败）", afterClose)
	}
}

// TestSSEStreamHeartbeatStopsWhenClientGone 钉住客户端断开即停止心跳。
//
// 不停的话会继续打到某一拍写失败为止，那期间的每一拍都是白做的——
// 而且它占着 handler goroutine 不返回。
func TestSSEStreamHeartbeatStopsWhenClientGone(t *testing.T) {
	t.Parallel()

	engine := gin.New()
	done := make(chan error, 1)
	engine.GET("/stream", func(c *gin.Context) {
		c.Set(streamRenewKey, func() error { return nil })
		s := NewSSEStream(c)

		// 模拟客户端断开：换掉请求的 ctx 并立即取消。
		ctx, cancel := context.WithCancel(c.Request.Context())
		c.Request = c.Request.WithContext(ctx)
		streamOnCancelledCtx := NewSSEStream(c)
		cancel()

		go func() { done <- streamOnCancelledCtx.Heartbeat(context.Background(), time.Hour) }()
		_ = s
	})
	engine.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/stream", nil))

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("客户端断开后 Heartbeat 返回错误 %v，want nil（这是预期的收尾）", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("客户端断开后 Heartbeat 没有退出：它会一直占着 handler goroutine")
	}
}
