package httpclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/testutil"

	"github.com/gtkit/httpc"
)

// TestTimeoutIsEnforced 钉住总超时**真的**作用到请求上。
//
// 只断言"配置里写了超时"证明不了任何事：真正要防的是一个卡住的下游把本进程
// 的 goroutine 与连接一起扣住，那只有让请求真的卡住才能验出来。
func TestTimeoutIsEnforced(t *testing.T) {
	testutil.RequireLoopback(t)
	t.Parallel()

	blocked := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 挂住直到测试收尾，模拟一个不回应的下游。
		select {
		case <-blocked:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() {
		close(blocked)
		server.Close()
	})

	factory, err := New(config.OutboundConfig{
		Clients: map[string]config.OutboundClientConfig{
			"slow": {BaseURL: server.URL, Timeout: 150 * time.Millisecond},
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = factory.Close() })

	client, err := factory.Client("slow")
	if err != nil {
		t.Fatalf("Client: %v", err)
	}

	var result map[string]any
	start := time.Now()
	// ctx 不带 deadline：这里要验的是客户端自己的总超时，
	// 若靠 ctx 兜底，客户端漏配超时也照样"通过"。
	_, err = client.GetJSON(context.Background(), "/", nil, &result)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("请求没有超时：卡住的下游会一直扣住本进程的连接")
	}
	if elapsed > 3*time.Second {
		t.Fatalf("超时耗时 %s，远超配置的 150ms：总超时没有生效", elapsed)
	}
}

// TestMaxResponseBytesIsEnforced 钉住响应体上限真的截断。
//
// 没有它，下游返回的异常巨大响应（HTML 错误页、被中间设备劫持的内容）
// 会被整个读进内存，一次异常就够把进程打到 OOM。
func TestMaxResponseBytesIsEnforced(t *testing.T) {
	testutil.RequireLoopback(t)
	t.Parallel()

	const limit = 1024
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// 比上限大一个量级，确保越界发生在读取过程中。
		oversized := make([]byte, limit*10)
		for i := range oversized {
			oversized[i] = 'x'
		}
		_, _ = w.Write(oversized)
	}))
	t.Cleanup(server.Close)

	factory, err := New(config.OutboundConfig{
		Clients: map[string]config.OutboundClientConfig{
			"chatty": {BaseURL: server.URL, Timeout: 5 * time.Second, MaxResponseBytes: limit},
		},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = factory.Close() })

	client, err := factory.Client("chatty")
	if err != nil {
		t.Fatalf("Client: %v", err)
	}

	body, _, err := client.GetRaw(context.Background(), "/", nil)
	if !errors.Is(err, httpc.ErrResponseTooLarge) {
		t.Fatalf("error = %v, want ErrResponseTooLarge（读了 %d 字节）", err, len(body))
	}
}
