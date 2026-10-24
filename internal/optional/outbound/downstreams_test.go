package outbound

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/optional/outbound/breaker"
	"github.com/gtkit/gin-api/internal/testutil"

	"github.com/gtkit/httpc"
)

// newTestDownstreams 构造指向 server 的单下游执行入口。
func newTestDownstreams(t *testing.T, baseURL string, breakerCfg BreakerConfig) *Downstreams {
	t.Helper()
	d, err := NewDownstreams(Config{
		Clients: map[string]ClientConfig{
			"orders": {BaseURL: baseURL, Timeout: 2 * time.Second},
		},
		Breaker: breakerCfg,
	})
	if err != nil {
		t.Fatalf("NewDownstreams: %v", err)
	}
	t.Cleanup(func() {
		if err := d.Close(); err != nil {
			t.Errorf("d.Close(): %v", err)
		}
	})
	return d
}

// getStatus 是最小的真实调用执行体。
func getStatus(path string) Attempt {
	return func(ctx context.Context, client *httpc.Client) (int, error) {
		_, status, err := client.GetRaw(ctx, path, nil)
		return status, err
	}
}

// TestDoRejectsUnknownDownstream 钉住未登记的名字不执行、不放行。
func TestDoRejectsUnknownDownstream(t *testing.T) {
	t.Parallel()

	d := newTestDownstreams(t, "http://unused.example.test", BreakerConfig{})
	_, err := d.Do(context.Background(), "typo", func(context.Context, *httpc.Client) (int, error) {
		t.Fatal("未登记的下游执行了调用")
		return 0, nil
	})
	if !errors.Is(err, ErrUnknownClient) {
		t.Fatalf("err = %v, want ErrUnknownClient", err)
	}
}

// TestDoNormalizesServerErrorsIntoTheBreaker 钉住 5xx 进熔断统计。
//
// httpc 把 5xx 留给调用方判断，于是"下游一直 500"在错误维度上是隐形的；
// 不归一化的话，熔断器对一个持续报错的下游永远不会跳闸。
func TestDoNormalizesServerErrorsIntoTheBreaker(t *testing.T) {
	testutil.RequireLoopback(t)
	t.Parallel()

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
		if _, err := w.Write([]byte("oops")); err != nil {
			t.Errorf("test response I/O: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	d := newTestDownstreams(t, server.URL, BreakerConfig{ConsecutiveFailures: 2})

	for range 2 {
		status, err := d.Do(context.Background(), "orders", getStatus("/"))
		if status != http.StatusInternalServerError {
			t.Fatalf("status = %d, want 500", status)
		}
		if !errors.Is(err, ErrDownstreamServerError) {
			t.Fatalf("err = %v, want ErrDownstreamServerError", err)
		}
	}

	// 连续失败达到阈值：熔断开启，调用不触网、快速失败。
	_, err := d.Do(context.Background(), "orders", getStatus("/"))
	if !breaker.IsOpen(err) {
		t.Fatalf("熔断未开启：err = %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("服务端收到 %d 次请求，want 2（熔断开启后不得触网）", got)
	}
}

// TestDoIdempotentRetriesWithTheSameProtectedClient 钉住重试真的发生、
// 只发生在该重试的失败上，且用的是工厂里那个带全部防护的客户端实例。
//
// "重试路径另建一个裸客户端"正是本次要修的缺陷：那条最容易反复打下游的
// 路径反而丢了响应体上限与连接池上限。
func TestDoIdempotentRetriesWithTheSameProtectedClient(t *testing.T) {
	testutil.RequireLoopback(t)
	t.Parallel()

	tests := []struct {
		name      string
		status    int
		wantCalls int32
	}{
		{name: "503 重试到次数用尽", status: http.StatusServiceUnavailable, wantCalls: 3},
		{name: "400 一次就停", status: http.StatusBadRequest, wantCalls: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				calls.Add(1)
				w.WriteHeader(tt.status)
				if _, err := w.Write([]byte("oops")); err != nil {
					t.Errorf("test response I/O: %v", err)
				}
			}))
			t.Cleanup(server.Close)

			// 熔断阈值放高：本用例只看重试，不让熔断先跳。
			d := newTestDownstreams(t, server.URL, BreakerConfig{ConsecutiveFailures: 100})

			factoryClient, err := d.Client("orders")
			if err != nil {
				t.Fatalf("Client: %v", err)
			}

			retry := RetryPolicy{Attempts: 2, WaitTime: time.Millisecond, MaxWaitTime: 10 * time.Millisecond}
			status, attemptErr := d.DoIdempotent(context.Background(), "orders", retry,
				func(ctx context.Context, client *httpc.Client) (int, error) {
					if client != factoryClient {
						t.Fatal("重试执行体拿到的不是工厂客户端：防护上限没有被继承")
					}
					_, status, getErr := client.GetRaw(ctx, "/", nil)
					return status, getErr
				})
			if status != tt.status || (tt.status >= 500 && !errors.Is(attemptErr, ErrDownstreamServerError)) ||
				(tt.status < 500 && attemptErr != nil) {
				t.Fatalf("retry result status=%d err=%v", status, attemptErr)
			}

			if got := calls.Load(); got != tt.wantCalls {
				t.Fatalf("服务端收到 %d 次请求，want %d", got, tt.wantCalls)
			}
		})
	}
}

// TestDoIdempotentRejectsUnsafePolicies 钉住策略校验发生在任何调用之前。
func TestDoIdempotentRejectsUnsafePolicies(t *testing.T) {
	t.Parallel()

	d := newTestDownstreams(t, "http://unused.example.test", BreakerConfig{})
	_, err := d.DoIdempotent(context.Background(), "orders", RetryPolicy{Attempts: 99},
		func(context.Context, *httpc.Client) (int, error) {
			t.Fatal("非法策略仍执行了调用")
			return 0, nil
		})
	if err == nil {
		t.Fatal("非法重试策略被接受")
	}
}

// TestRetryBackoffIsCancelable 钉住退避等待可被调用方取消。
//
// 退避里睡死等于把调用方的取消预算吃掉：请求已被放弃，等待只是让 goroutine
// 多挂一个退避周期。
func TestRetryBackoffIsCancelable(t *testing.T) {
	testutil.RequireLoopback(t)
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		if _, err := w.Write([]byte("oops")); err != nil {
			t.Errorf("test response I/O: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	d := newTestDownstreams(t, server.URL, BreakerConfig{ConsecutiveFailures: 100})

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := d.DoIdempotent(ctx, "orders",
		RetryPolicy{Attempts: 3, WaitTime: 5 * time.Second, MaxWaitTime: 10 * time.Second},
		getStatus("/"))
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("退避等待耗时 %s：取消没有生效", elapsed)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled in the chain", err)
	}
	// 真实的下游失败不能被"调用方放弃"抹掉：熔断判据依赖它。
	if !errors.Is(err, ErrDownstreamServerError) {
		t.Fatalf("err = %v, want the last downstream failure preserved", err)
	}
}

// TestZeroBackoffStillChecksCancellation 钉住零退避下的取消检查。
//
// 退避为 0 时循环里没有等待点，若不在每轮之前无条件检查 ctx，
// 一个已被放弃的调用会把重试次数全部打完——白打下游，还计进熔断。
func TestZeroBackoffStillChecksCancellation(t *testing.T) {
	testutil.RequireLoopback(t)
	t.Parallel()

	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		if _, err := w.Write([]byte("oops")); err != nil {
			t.Errorf("test response I/O: %v", err)
		}
	}))
	t.Cleanup(server.Close)

	d := newTestDownstreams(t, server.URL, BreakerConfig{ConsecutiveFailures: 100})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	_, err := d.DoIdempotent(ctx, "orders",
		RetryPolicy{Attempts: 3, WaitTime: 0, MaxWaitTime: 0}, // 零退避：循环里没有等待点
		func(attemptCtx context.Context, client *httpc.Client) (int, error) {
			_, status, getErr := client.GetRaw(attemptCtx, "/", nil)
			cancel() // 本次尝试已完成，调用方随即放弃
			return status, getErr
		})

	if got := calls.Load(); got != 1 {
		t.Fatalf("服务端收到 %d 次请求，want 1：取消之后不得继续重试", got)
	}
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled in the chain", err)
	}
	// 真实的下游失败仍需保留：熔断判据依赖它。
	if !errors.Is(err, ErrDownstreamServerError) {
		t.Fatalf("err = %v, want the last downstream failure preserved", err)
	}
}
