package outbound

import (
	"context"
	"errors"
	"math"
	"net/http"
	"testing"
	"testing/synctest"
	"time"
)

func TestRetryTimingValidation(t *testing.T) {
	for _, p := range []RetryPolicy{
		{Attempts: 1, WaitTime: -1}, {Attempts: 1, MaxWaitTime: -1},
		{Attempts: 1, WaitTime: 31 * time.Second}, {Attempts: 1, MaxWaitTime: 31 * time.Second},
		{Attempts: 1, WaitTime: time.Duration(math.MaxInt64)},
	} {
		if err := p.validate(); err == nil {
			t.Fatalf("accepted invalid policy %+v", p)
		}
	}
	if err := (RetryPolicy{Attempts: 3, WaitTime: 20 * time.Second, MaxWaitTime: 20 * time.Second}).validate(); err != nil {
		t.Fatal("maximum count does not promise all waits will fit", err)
	}
}

func TestRetryAfterParsing(t *testing.T) {
	now := time.Date(2026, 10, 26, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		raw  string
		want time.Duration
		ok   bool
	}{
		{"", 0, true}, {"2", 2 * time.Second, true}, {"30", 30 * time.Second, true}, {"86400", 0, false},
		{"184467440737095516160000", 0, false}, {"malformed", 0, true}, {"-1", 0, true},
		{now.Add(3 * time.Second).Format(http.TimeFormat), 3 * time.Second, true},
		{now.Add(-time.Second).Format(http.TimeFormat), 0, true},
		{now.Add(time.Hour).Format(http.TimeFormat), time.Hour, false},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			got, ok := retryAfterWait(tc.raw, now)
			if ok != tc.ok || got != tc.want {
				t.Fatalf("got %s/%v", got, ok)
			}
		})
	}
}

func TestRetryBudgetAndAtMostAttempts(t *testing.T) {
	for _, jitter := range []time.Duration{0, 20 * time.Second} {
		t.Run(jitter.String(), func(t *testing.T) {
			calls := 0
			cause := errors.New("downstream")
			now := time.Now()
			waited := time.Duration(0)
			timing := retryTiming{now: func() time.Time { return now }, jitter: func(time.Duration) time.Duration { return jitter }, wait: func(_ context.Context, d time.Duration) error { waited += d; now = now.Add(d); return nil }}
			result := runRetries(t.Context(), RetryPolicy{Attempts: 3, WaitTime: 20 * time.Second, MaxWaitTime: 20 * time.Second}, func(context.Context) retryResult { calls++; return retryResult{status: 503, err: cause} }, timing)
			want := 4
			if jitter > 0 {
				want = 2
			}
			if calls != want || !errors.Is(result.err, cause) || waited > maxRetryWait {
				t.Fatalf("calls=%d waited=%s err=%v", calls, waited, result.err)
			}
		})
	}
}

func TestRetryAfterRespectsServerAndCallerBudgets(t *testing.T) {
	for _, tc := range []struct {
		after     string
		deadline  time.Duration
		wantCalls int
		wantWait  time.Duration
	}{{"2", 0, 2, 2 * time.Second}, {"86400", 0, 1, 0}, {"184467440737095516160000", 0, 1, 0}, {"2", time.Second, 1, 0}} {
		calls := 0
		waited := time.Duration(0)
		now := time.Now()
		cause := errors.New("last failure")
		ctx := t.Context()
		if tc.deadline > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithDeadline(ctx, now.Add(tc.deadline))
			defer cancel()
		}
		timing := retryTiming{now: func() time.Time { return now }, jitter: func(time.Duration) time.Duration { return 0 }, wait: func(_ context.Context, d time.Duration) error { waited += d; now = now.Add(d); return nil }}
		result := runRetries(ctx, RetryPolicy{Attempts: 1}, func(context.Context) retryResult {
			calls++
			return retryResult{status: 503, err: cause, retryAfter: tc.after}
		}, timing)
		if calls != tc.wantCalls || waited != tc.wantWait || !errors.Is(result.err, cause) || errors.Is(result.err, context.DeadlineExceeded) {
			t.Fatalf("%s: calls=%d waited=%s err=%v", tc.after, calls, waited, result.err)
		}
	}
}

func TestRetryWaitCancellationKeepsLastFailure(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		cause := errors.New("last failure")
		calls := 0
		timing := defaultRetryTiming()
		timing.jitter = func(waitCap time.Duration) time.Duration { return waitCap }
		go func() { synctest.Sleep(time.Second); cancel() }()
		result := runRetries(ctx, RetryPolicy{Attempts: 3, WaitTime: 5 * time.Second}, func(context.Context) retryResult { calls++; return retryResult{status: 503, err: cause} }, timing)
		if calls != 1 || !errors.Is(result.err, cause) || !errors.Is(result.err, context.Canceled) {
			t.Fatalf("calls=%d err=%v", calls, result.err)
		}
	})
}

func TestFullJitterBounds(t *testing.T) {
	jitter := defaultRetryTiming().jitter
	if jitter(0) != 0 {
		t.Fatal("zero delay changed")
	}
	for range 100 {
		if d := jitter(maxRetryWait); d < 0 || d > maxRetryWait {
			t.Fatalf("jitter out of bounds %s", d)
		}
	}
}
