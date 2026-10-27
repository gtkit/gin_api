package middleware

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/testutil"

	"github.com/gin-gonic/gin"
)

// newTimeoutEngine 组装一条"终态错误处理 + 路由超时 + handler"的最小链。
//
// 顺序与生产一致：ErrorHandler 在全局层、RouteTimeout 在路由层，
// 因此超时中间件返回后错误才被终态中间件观察到。
func newTimeoutEngine(t *testing.T, timeoutMW gin.HandlerFunc, handler gin.HandlerFunc) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	engine.Use(ErrorHandler())
	engine.GET("/probe", timeoutMW, handler)
	engine.POST("/probe", timeoutMW, handler)
	return engine
}

func cooperativePolicy(deadline, grace time.Duration) gin.HandlerFunc {
	return Timeout(deadline, grace)
}

type deadlineRecorder struct {
	*httptest.ResponseRecorder
	deadlines []time.Time
}

func (r *deadlineRecorder) SetWriteDeadline(deadline time.Time) error {
	r.deadlines = append(r.deadlines, deadline)
	return nil
}

func TestNestedTimeoutKeepsEarliestWriteDeadline(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(ErrorHandler())
	engine.GET("/probe", Timeout(time.Second, time.Second), Timeout(100*time.Millisecond, 50*time.Millisecond),
		func(c *gin.Context) { c.Status(http.StatusNoContent) })
	w := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/probe", nil))
	if len(w.deadlines) != 2 || w.deadlines[0].IsZero() || w.deadlines[1].IsZero() || !w.deadlines[1].Before(w.deadlines[0]) {
		t.Fatalf("nested deadlines must only tighten and never clear: %v", w.deadlines)
	}
}

func TestNestedStreamRenewalRespectsRouteDeadline(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, streamFirst := range []bool{false, true} {
		engine := gin.New()
		chain := []gin.HandlerFunc{Timeout(time.Second, time.Second), StreamTimeout(50 * time.Millisecond)}
		if streamFirst {
			chain[0], chain[1] = chain[1], chain[0]
		}
		chain = append(chain, func(c *gin.Context) {
			if err := RenewStreamDeadline(c); err != nil {
				t.Error(err)
			}
			c.Status(http.StatusNoContent)
		})
		engine.GET("/probe", chain...)
		w := &deadlineRecorder{ResponseRecorder: httptest.NewRecorder()}
		start := time.Now()
		engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/probe", nil))
		for _, deadline := range w.deadlines {
			if deadline.IsZero() || deadline.After(start.Add(2100*time.Millisecond)) {
				t.Fatalf("stream/route deadline was extended or cleared: %v", w.deadlines)
			}
		}
		if streamFirst && w.deadlines[1].After(w.deadlines[0]) {
			t.Fatalf("inner route extended the existing idle deadline: %v", w.deadlines)
		}
	}
}

func TestResponseTailRetainsWriteDeadline(t *testing.T) {
	testutil.RequireLoopback(t)
	for _, serverTimeout := range []time.Duration{0, 60 * time.Second} {
		t.Run(serverTimeout.String(), func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			type tailResult struct {
				status int
				err    error
			}
			finished := make(chan tailResult, 1)
			engine := gin.New()
			engine.Use(func(c *gin.Context) {
				c.Next()
				status := c.Writer.Status()
				chunk := bytes.Repeat([]byte("x"), 64<<10)
				var err error
				for range 4096 {
					if _, err = c.Writer.Write(chunk); err != nil {
						break
					}
				}
				finished <- tailResult{status, err}
			})
			engine.Use(ErrorHandler())
			engine.GET("/probe", Timeout(20*time.Millisecond, 100*time.Millisecond), func(c *gin.Context) {
				<-c.Request.Context().Done()
				c.Error(c.Request.Context().Err()).Type = gin.ErrorTypePrivate
			})
			srv := httptest.NewUnstartedServer(engine)
			srv.Config.WriteTimeout = serverTimeout
			srv.Start()
			t.Cleanup(srv.Close)
			t.Cleanup(srv.CloseClientConnections)
			conn, err := net.DialTimeout("tcp", srv.Listener.Addr().String(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := conn.Close(); err != nil {
					t.Error(err)
				}
			})
			if _, err := fmt.Fprint(conn, "GET /probe HTTP/1.1\r\nHost: test\r\n\r\n"); err != nil {
				t.Fatal(err)
			}
			// 不读取响应，迫使外层中间件在 Timeout 已返回后遭遇背压。
			select {
			case result := <-finished:
				if result.status != http.StatusGatewayTimeout {
					t.Errorf("tail status = %d, want 504", result.status)
				}
				if networkErr, ok := errors.AsType[net.Error](result.err); !ok || !networkErr.Timeout() {
					t.Fatalf("tail write = %v, want a deadline error", result.err)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("tail write lost its route deadline after Timeout returned")
			}
		})
	}
}

func TestHTTP2TimeoutStreamDoesNotBlockSibling(t *testing.T) {
	testutil.RequireLoopback(t)
	gin.SetMode(gin.TestMode)
	written := make(chan error, 1)
	engine := gin.New()
	engine.GET("/slow", Timeout(20*time.Millisecond, 100*time.Millisecond), func(c *gin.Context) {
		c.Status(http.StatusOK)
		c.Writer.Flush()
		chunk := bytes.Repeat([]byte("x"), 64<<10)
		for range 4096 {
			if _, err := c.Writer.Write(chunk); err != nil {
				written <- err
				return
			}
		}
		written <- nil
	})
	engine.GET("/fast", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	var connections atomic.Int32
	srv := httptest.NewUnstartedServer(engine)
	srv.EnableHTTP2 = true
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			connections.Add(1)
		}
	}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	t.Cleanup(srv.CloseClientConnections)
	client := srv.Client()
	client.Timeout = 3 * time.Second
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("client transport = %T, want *http.Transport", client.Transport)
	}
	transport.MaxConnsPerHost = 1
	slow, err := client.Get(srv.URL + "/slow")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := slow.Body.Close(); err != nil {
			t.Error(err)
		}
	})
	if slow.ProtoMajor != 2 {
		t.Fatalf("slow protocol = %s, want HTTP/2", slow.Proto)
	}
	fast := func() {
		t.Helper()
		response, err := client.Get(srv.URL + "/fast")
		if err != nil {
			t.Fatal(err)
		}
		if err := response.Body.Close(); err != nil {
			t.Error(err)
		}
		if response.StatusCode != http.StatusNoContent || response.ProtoMajor != 2 {
			t.Fatalf("sibling response = %s %d", response.Proto, response.StatusCode)
		}
	}
	fast()
	select {
	case err := <-written:
		if err == nil {
			t.Fatal("unread stream did not hit its write deadline")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("HTTP/2 stream write deadline did not fire")
	}
	fast()
	if connections.Load() != 1 {
		t.Fatalf("used %d connections, want one HTTP/2 connection", connections.Load())
	}
}

// TestCooperativeTimeoutMapsDeadlineExceededTo504 验证协作式链路的 504。
//
// 判据是"handler 观察了 ctx 并把 DeadlineExceeded 交给终态中间件"——
// 这正是这套机制唯一承诺的东西。
func TestCooperativeTimeoutMapsDeadlineExceededTo504(t *testing.T) {
	engine := newTimeoutEngine(t, cooperativePolicy(50*time.Millisecond, time.Second),
		func(c *gin.Context) {
			<-c.Request.Context().Done()
			c.Error(fmt.Errorf("slow query: %w", c.Request.Context().Err())).Type = gin.ErrorTypePrivate
		})

	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/probe", nil))

	if w.Code != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want %d (body %s)", w.Code, http.StatusGatewayTimeout, w.Body.String())
	}
}

// TestClientDisconnectWritesNothing 验证客户端断开**不写响应**。
//
// 连接已经没了，写只会产生一条无意义的错误日志；更糟的是它把
// "客户端走了"记成一次服务端错误，污染错误率指标。
func TestClientDisconnectWritesNothing(t *testing.T) {
	engine := newTimeoutEngine(t, cooperativePolicy(time.Second, time.Second),
		func(c *gin.Context) {
			c.Error(fmt.Errorf("upstream: %w", context.Canceled)).Type = gin.ErrorTypePrivate
		})

	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/probe", nil))

	if w.Body.Len() != 0 {
		t.Fatalf("body = %q, want empty on client disconnect", w.Body.String())
	}
	if w.Code == http.StatusGatewayTimeout {
		t.Fatal("client disconnect was reported as a gateway timeout")
	}
}

// TestTimeoutDoesNotOverwriteAWrittenResponse 验证已写出响应时不再补写 504。
//
// 重复写会破坏已经发出的响应体（流式尤甚），且客户端会看到两段拼接的内容。
func TestTimeoutDoesNotOverwriteAWrittenResponse(t *testing.T) {
	engine := newTimeoutEngine(t, cooperativePolicy(50*time.Millisecond, time.Second),
		func(c *gin.Context) {
			c.String(http.StatusOK, "partial")
			<-c.Request.Context().Done()
			c.Error(c.Request.Context().Err()).Type = gin.ErrorTypePrivate
		})

	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/probe", nil))

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: an already-written response must not be overwritten", w.Code)
	}
	if w.Body.String() != "partial" {
		t.Fatalf("body = %q, want %q", w.Body.String(), "partial")
	}
}

// TestHandlerIgnoringContextIsNotTerminated 钉住这套机制**不是**强制终止器。
//
// 忽略 ctx 的 handler 会一直跑到自己返回。这条断言存在的意义是防止
// 文档与后续改动声称"超时到点必然 504"——那个承诺做不到，
// 而基于错误承诺做的容量规划会直接失效。
func TestHandlerIgnoringContextIsNotTerminated(t *testing.T) {
	var finished bool
	engine := newTimeoutEngine(t, cooperativePolicy(20*time.Millisecond, time.Second),
		func(c *gin.Context) {
			// 完全不看 ctx。
			time.Sleep(80 * time.Millisecond)
			finished = true
			c.String(http.StatusOK, "done")
		})

	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/probe", nil))

	if !finished {
		t.Fatal("the handler did not run to completion; the mechanism must not claim to terminate goroutines")
	}
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: an unresponsive handler still produces its own response", w.Code)
	}
}

// TestTimeoutRejectsInvalidDurations 验证非法时长在装配期就被拒绝。
//
// panic 而不是返回 error：这与 gin 生态的惯例一致，且它发生在启动期——
// 一个时长为 0 的超时中间件等于没有超时，必须响亮地失败。
func TestTimeoutRejectsInvalidDurations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		deadline, grace time.Duration
	}{
		{name: "zero deadline", deadline: 0, grace: time.Second},
		{name: "negative deadline", deadline: -time.Second, grace: time.Second},
		{name: "zero grace", deadline: time.Second, grace: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			defer func() {
				if recover() == nil {
					t.Fatalf("Timeout(%s, %s) did not panic: a zero timeout is no timeout",
						tt.deadline, tt.grace)
				}
			}()
			Timeout(tt.deadline, tt.grace)
		})
	}
}

// --- 以下用真实 TCP listener ---
//
// 计划明确要求：不能只用 httptest.ResponseRecorder。recorder 不实现
// SetWriteDeadline，ResponseController 会返回 errNotSupported——
// 那样的测试等于什么都没验，还会给出"已覆盖"的假象。

// startRealServer 起一个真实的 httptest.Server 并返回其地址。
func startRealServer(t *testing.T, engine *gin.Engine) string {
	testutil.RequireLoopback(t)
	t.Helper()
	srv := httptest.NewServer(engine)
	t.Cleanup(srv.Close)
	return strings.TrimPrefix(srv.URL, "http://")
}

// TestWriteDeadlineIsActuallySetOnRealConnection 证明 socket 写 deadline
// 在真实连接上确实生效。
//
// 判据：handler 内查询 ResponseController.SetWriteDeadline 是否可用。
// 若 writer 链断了（例如某个 wrapper 没实现 Unwrap），这里会拿到
// ErrNotSupported——而那正是"保护配了但静默失效"的形态。
func TestWriteDeadlineIsActuallySetOnRealConnection(t *testing.T) {
	var (
		mu         sync.Mutex
		controlErr error
		checked    bool
	)

	engine := newTimeoutEngine(t, cooperativePolicy(2*time.Second, time.Second),
		func(c *gin.Context) {
			rc := http.NewResponseController(c.Writer)
			err := rc.SetWriteDeadline(time.Now().Add(time.Second))
			mu.Lock()
			controlErr, checked = err, true
			mu.Unlock()
			c.String(http.StatusOK, "ok")
		})

	addr := startRealServer(t, engine)
	resp, err := http.Get("http://" + addr + "/probe")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("resp.Body.Close(): %v", err)
		}
	}()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Errorf("test response I/O: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if !checked {
		t.Fatal("handler did not run")
	}
	if controlErr != nil {
		t.Fatalf("SetWriteDeadline on a real connection = %v, want nil: "+
			"a broken writer chain makes every socket deadline silently ineffective", controlErr)
	}
}

// TestWriteDeadlineSurvivesResponseBodyLogging 是上一条的关键变体：
// **开启响应体日志之后** socket deadline 仍须可设。
//
// responseBodyWriter 只嵌入 gin.ResponseWriter；不实现 Unwrap 的话，
// 一个纯观测开关会静默关掉所有路由的写超时保护。
func TestWriteDeadlineSurvivesResponseBodyLogging(t *testing.T) {
	gin.SetMode(gin.TestMode)

	var (
		mu         sync.Mutex
		controlErr error
		checked    bool
	)

	timeoutMW := cooperativePolicy(2*time.Second, time.Second)

	engine := gin.New()
	// 关键：开启响应体日志，让 responseBodyWriter 进入 writer 链。
	access, _ := newNativeTestLogger(t, "info")
	engine.Use(GinLogger(access, AccessLogOptions{LogResponseBody: true}))
	engine.POST("/probe", timeoutMW, func(c *gin.Context) {
		rc := http.NewResponseController(c.Writer)
		e := rc.SetWriteDeadline(time.Now().Add(time.Second))
		mu.Lock()
		controlErr, checked = e, true
		mu.Unlock()
		c.String(http.StatusOK, "ok")
	})

	addr := startRealServer(t, engine)
	resp, err := http.Post("http://"+addr+"/probe", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("resp.Body.Close(): %v", err)
		}
	}()
	if _, err := io.Copy(io.Discard, resp.Body); err != nil {
		t.Errorf("test response I/O: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if !checked {
		t.Fatal("handler did not run")
	}
	if controlErr != nil {
		t.Fatalf("SetWriteDeadline with response body logging enabled = %v, want nil: "+
			"responseBodyWriter must implement Unwrap so the controller can reach the connection",
			controlErr)
	}
}

// TestKeepAliveConnectionIsNotPollutedByAPreviousDeadline 验证 net/http 的请求边界清理。
// 中间件保留尾部写期限；服务器在响应完成后清理，下一条路由设置自己的期限。
// 因此必须在同一条连接上连续发两个请求。
func TestKeepAliveConnectionIsNotPollutedByAPreviousDeadline(t *testing.T) {
	testutil.RequireLoopback(t)
	gin.SetMode(gin.TestMode)

	shortMW := cooperativePolicy(150*time.Millisecond, 100*time.Millisecond)
	longMW := cooperativePolicy(5*time.Second, 2*time.Second)

	engine := gin.New()
	engine.GET("/short", shortMW, func(c *gin.Context) { c.String(http.StatusOK, "short") })
	engine.GET("/slow", longMW, func(c *gin.Context) {
		// 睡得比第一条路由的 socket deadline（150+100ms）更久。
		// 若 deadline 没被清零，这次写会失败。
		time.Sleep(400 * time.Millisecond)
		c.String(http.StatusOK, "slow")
	})

	addr := startRealServer(t, engine)

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() {
		if err := conn.Close(); err != nil {
			t.Errorf("conn.Close(): %v", err)
		}
	}()
	reader := bufio.NewReader(conn)

	sendAndRead := func(path string) (string, error) {
		req := "GET " + path + " HTTP/1.1\r\nHost: test\r\nConnection: keep-alive\r\n\r\n"
		if _, err := conn.Write([]byte(req)); err != nil {
			return "", fmt.Errorf("write request: %w", err)
		}
		if err := conn.SetReadDeadline(time.Now().Add(5 * time.Second)); err != nil {
			return "", fmt.Errorf("set read deadline: %w", err)
		}

		resp, err := http.ReadResponse(reader, nil)
		if err != nil {
			return "", fmt.Errorf("read response: %w", err)
		}
		defer func() {
			if err := resp.Body.Close(); err != nil {
				t.Errorf("resp.Body.Close(): %v", err)
			}
		}()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", fmt.Errorf("read body: %w", err)
		}
		return string(body), nil
	}

	first, err := sendAndRead("/short")
	if err != nil {
		t.Fatalf("first request: %v", err)
	}
	if first != "short" {
		t.Fatalf("first body = %q, want %q", first, "short")
	}

	second, err := sendAndRead("/slow")
	if err != nil {
		t.Fatalf("second request on the same keep-alive connection: %v "+
			"(the previous route's write deadline was not cleared)", err)
	}
	if second != "slow" {
		t.Fatalf("second body = %q, want %q", second, "slow")
	}
}

// TestStreamingRenewsIdleWriteDeadline 验证流式的可续期空闲超时。
//
// 关键性质：连接可以活得比 IdleWrite 久很多，只要持续有 flush。
// 固定的 WriteTimeout 做不到这一点——它是"从响应开始"的绝对上限。
func TestStreamingRenewsIdleWriteDeadline(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const (
		idleWrite = 200 * time.Millisecond
		heartbeat = 60 * time.Millisecond
		chunks    = 8 // 8 * 60ms = 480ms > idleWrite，不续期必然失败
	)

	streamMW := StreamTimeout(idleWrite)
	_ = heartbeat

	var renewErr error
	engine := gin.New()
	engine.GET("/stream", streamMW, func(c *gin.Context) {
		c.Header("Content-Type", "text/event-stream")
		for i := range chunks {
			if _, err := fmt.Fprintf(c.Writer, "data: %d\n\n", i); err != nil {
				renewErr = fmt.Errorf("write chunk %d: %w", i, err)
				return
			}
			c.Writer.Flush()
			if err := RenewStreamDeadline(c); err != nil {
				renewErr = err
				return
			}
			time.Sleep(heartbeat)
		}
	})

	addr := startRealServer(t, engine)
	resp, err := http.Get("http://" + addr + "/stream")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer func() {
		if err := resp.Body.Close(); err != nil {
			t.Errorf("resp.Body.Close(): %v", err)
		}
	}()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read stream: %v (total duration exceeded the idle write timeout without renewal)", err)
	}
	if renewErr != nil {
		t.Fatalf("streaming handler failed: %v", renewErr)
	}
	for i := range chunks {
		if !strings.Contains(string(body), fmt.Sprintf("data: %d", i)) {
			t.Fatalf("stream body is missing chunk %d: %q", i, body)
		}
	}
}

// TestRenewStreamDeadlineIsNoopOnNonStreamingRoutes 验证非流式路由上调用续期
// 不报错——它没有可续期的空闲预算，那不是错误。
func TestRenewStreamDeadlineIsNoopOnNonStreamingRoutes(t *testing.T) {
	var got error
	engine := newTimeoutEngine(t, cooperativePolicy(time.Second, time.Second),
		func(c *gin.Context) {
			got = RenewStreamDeadline(c)
			c.Status(http.StatusOK)
		})

	w := httptest.NewRecorder()
	engine.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/probe", nil))

	if got != nil {
		t.Fatalf("RenewStreamDeadline on a non-streaming route = %v, want nil", got)
	}
}

func TestTimeoutErrorClassification(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		err         error
		wantTimeout bool
		wantGone    bool
	}{
		{name: "nil", err: nil},
		{name: "deadline exceeded", err: context.DeadlineExceeded, wantTimeout: true},
		{name: "wrapped deadline", err: fmt.Errorf("q: %w", context.DeadlineExceeded), wantTimeout: true},
		{name: "canceled", err: context.Canceled, wantGone: true},
		{name: "wrapped canceled", err: fmt.Errorf("q: %w", context.Canceled), wantGone: true},
		{name: "cancellation and deadline", err: errors.Join(context.Canceled, context.DeadlineExceeded), wantGone: true},
		{name: "wrapped abort tree", err: fmt.Errorf("q: %w", errors.Join(context.DeadlineExceeded, context.Canceled)), wantGone: true},
		{name: "cancellation and failure", err: errors.Join(context.Canceled, errors.New("database unavailable"))},
		{name: "deadline and failure", err: errors.Join(context.DeadlineExceeded, errors.New("database unavailable"))},
		{name: "abort tree and failure", err: errors.Join(context.Canceled, context.DeadlineExceeded, errors.New("database unavailable"))},
		{name: "unrelated", err: errors.New("boom")},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := IsTimeoutError(tc.err); got != tc.wantTimeout {
				t.Errorf("IsTimeoutError(%v) = %v, want %v", tc.err, got, tc.wantTimeout)
			}
			if got := IsClientGone(tc.err); got != tc.wantGone {
				t.Errorf("IsClientGone(%v) = %v, want %v", tc.err, got, tc.wantGone)
			}
		})
	}
}
