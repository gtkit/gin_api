package httpclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestRetryableFailureOnlyRetriesWhatIsWorthRetrying 钉住重试判据。
//
// 4xx 被重试是最常见的错法：它不会因为再试一次就变对，只是把一次客户端
// 错误放大成 N 次——而这 N 次都算在下游的配额里。取消/超时的请求同样
// 不该重试：没人在等它的结果了。
func TestRetryableFailureOnlyRetriesWhatIsWorthRetrying(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		status int
		err    error
		want   bool
	}{
		{name: "传输层错误重试", err: errors.New("connection refused"), want: true},
		{name: "调用方取消不重试", err: fmt.Errorf("do: %w", context.Canceled), want: false},
		{name: "调用方超时不重试", err: fmt.Errorf("do: %w", context.DeadlineExceeded), want: false},
		{name: "429 重试", status: http.StatusTooManyRequests, want: true},
		{name: "500 重试", status: http.StatusInternalServerError, want: true},
		{name: "503 重试", status: http.StatusServiceUnavailable, want: true},
		{name: "501 不重试", status: http.StatusNotImplemented, want: false},
		{name: "400 不重试", status: http.StatusBadRequest, want: false},
		{name: "401 不重试", status: http.StatusUnauthorized, want: false},
		{name: "404 不重试", status: http.StatusNotFound, want: false},
		{name: "200 不重试", status: http.StatusOK, want: false},
		{name: "两者皆空不重试", status: 0, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := RetryableFailure(tt.status, tt.err); got != tt.want {
				t.Fatalf("RetryableFailure(%d, %v) = %v, want %v", tt.status, tt.err, got, tt.want)
			}
		})
	}
}

// TestRetryPolicyRejectsUnsafeParameters 钉住策略校验。
//
// 重试次数没有上限，等于允许把一次下游抖动放大成一场雪崩。
func TestRetryPolicyRejectsUnsafeParameters(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		retry   RetryPolicy
		wantErr string
	}{
		{name: "合法", retry: RetryPolicy{Attempts: 2, WaitTime: time.Millisecond, MaxWaitTime: time.Second}},
		{name: "零次重试", retry: RetryPolicy{Attempts: 0}, wantErr: "out of range"},
		{name: "次数超上限", retry: RetryPolicy{Attempts: 99}, wantErr: "out of range"},
		{
			name:    "退避下界大于上界",
			retry:   RetryPolicy{Attempts: 1, WaitTime: 10 * time.Second, MaxWaitTime: time.Second},
			wantErr: "exceeds max wait",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.retry.validate()
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("validate: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("接受了不安全的参数，want error containing %q", tt.wantErr)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error %q does not contain %q", err, tt.wantErr)
			}
		})
	}
}
