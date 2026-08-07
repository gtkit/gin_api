package httpclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/testutil"

	"github.com/go-resty/resty/v2"
)

// TestRetryableFailureOnlyRetriesWhatIsWorthRetrying 钉住重试判据。
//
// 4xx 被重试是最常见的错法：它不会因为再试一次就变对，只是把一次客户端
// 错误放大成 N 次——而这 N 次都算在下游的配额里。
func TestRetryableFailureOnlyRetriesWhatIsWorthRetrying(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status int
		err    error
		want   bool
	}{
		{name: "传输层错误重试", err: context.DeadlineExceeded, want: true},
		{name: "429 重试", status: http.StatusTooManyRequests, want: true},
		{name: "500 重试", status: http.StatusInternalServerError, want: true},
		{name: "503 重试", status: http.StatusServiceUnavailable, want: true},
		{name: "501 不重试", status: http.StatusNotImplemented, want: false},
		{name: "400 不重试", status: http.StatusBadRequest, want: false},
		{name: "401 不重试", status: http.StatusUnauthorized, want: false},
		{name: "404 不重试", status: http.StatusNotFound, want: false},
		{name: "200 不重试", status: http.StatusOK, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if tt.err != nil {
				if got := RetryableFailure(nil, tt.err); got != tt.want {
					t.Fatalf("RetryableFailure(nil, %v) = %v, want %v", tt.err, got, tt.want)
				}
				return
			}
			// resty.Response 的状态码取自内嵌的 *http.Response。
			resp := newRestyResponse(tt.status)
			if got := RetryableFailure(resp, nil); got != tt.want {
				t.Fatalf("RetryableFailure(%d) = %v, want %v", tt.status, got, tt.want)
			}
		})
	}

	// 两者皆空是调用方用错，保守不重试。
	if RetryableFailure(nil, nil) {
		t.Fatal("RetryableFailure(nil, nil) = true, want false")
	}
}

// TestNewRetryingRejectsUnsafePolicies 钉住构造期的参数拒绝。
//
// 重试次数没有上限，等于允许把一次下游抖动放大成一场雪崩。
func TestNewRetryingRejectsUnsafePolicies(t *testing.T) {
	t.Parallel()

	base := config.OutboundClientConfig{BaseURL: "https://x.example.test"}

	tests := []struct {
		name    string
		cfg     config.OutboundClientConfig
		retry   RetryPolicy
		wantErr string
	}{
		{name: "合法", cfg: base, retry: RetryPolicy{Attempts: 2, WaitTime: time.Millisecond, MaxWaitTime: time.Second}},
		{name: "缺 base_url", cfg: config.OutboundClientConfig{}, retry: RetryPolicy{Attempts: 1}, wantErr: "base_url"},
		{name: "零次重试", cfg: base, retry: RetryPolicy{Attempts: 0}, wantErr: "out of range"},
		{name: "次数超上限", cfg: base, retry: RetryPolicy{Attempts: 99}, wantErr: "out of range"},
		{
			name:    "退避下界大于上界",
			cfg:     base,
			retry:   RetryPolicy{Attempts: 1, WaitTime: 10 * time.Second, MaxWaitTime: time.Second},
			wantErr: "exceeds max wait",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			client, err := NewRetrying(tt.cfg, tt.retry)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("NewRetrying: %v", err)
				}
				if client == nil {
					t.Fatal("NewRetrying 返回了 nil 客户端")
				}
				return
			}
			if err == nil {
				t.Fatalf("NewRetrying 接受了不安全的参数，want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tt.wantErr)
			}
		})
	}
}

// TestRetryingClientActuallyRetries 钉住重试**真的**发生，且只发生在该重试的失败上。
//
// 只断言"配置里写了重试次数"证明不了任何事——AddRetryCondition 的判据接错、
// 或次数没被应用，都能让配置看起来对而行为完全不同。
func TestRetryingClientActuallyRetries(t *testing.T) {
	testutil.RequireLoopback(t)
	t.Parallel()

	tests := []struct {
		name string
		// status 是服务端每次都返回的状态码。
		status int
		// wantCalls 是服务端应当收到的总请求数（首次 + 重试）。
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
			}))
			t.Cleanup(server.Close)

			client, err := NewRetrying(
				config.OutboundClientConfig{BaseURL: server.URL, Timeout: 2 * time.Second},
				RetryPolicy{Attempts: 2, WaitTime: time.Millisecond, MaxWaitTime: 10 * time.Millisecond},
			)
			if err != nil {
				t.Fatalf("NewRetrying: %v", err)
			}

			// 错误与否不重要：这里要的是服务端实际被打了几次。
			_, _ = client.Get(context.Background(), "/")

			if got := calls.Load(); got != tt.wantCalls {
				t.Fatalf("服务端收到 %d 次请求，want %d", got, tt.wantCalls)
			}
		})
	}
}

// newRestyResponse 造一个只带状态码的响应。
//
// resty 的 StatusCode() 读的是内嵌的 *http.Response，因此这已经足够
// 驱动重试判据，不必真的发一次请求。
func newRestyResponse(status int) *resty.Response {
	return &resty.Response{RawResponse: &http.Response{StatusCode: status}}
}
