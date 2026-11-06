package outbound

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strconv"
	"strings"
	"time"
)

type retryResult struct {
	status     int
	err        error
	retryAfter string
}
type retryTiming struct {
	now    func() time.Time
	wait   func(context.Context, time.Duration) error
	jitter func(time.Duration) time.Duration
}

func defaultRetryTiming() retryTiming {
	return retryTiming{now: time.Now, jitter: func(waitCap time.Duration) time.Duration {
		if waitCap <= 0 {
			return 0
		}
		return time.Duration(rand.Int64N(int64(waitCap) + 1)) //nolint:gosec // Backoff jitter does not generate security tokens.
	}, wait: func(ctx context.Context, d time.Duration) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if d == 0 {
			return nil
		}
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-timer.C:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}}
}

func runRetries(ctx context.Context, policy RetryPolicy, attempt func(context.Context) retryResult, timing retryTiming) retryResult {
	if err := policy.validate(); err != nil {
		return retryResult{err: err}
	}
	waitCap := policy.MaxWaitTime
	if waitCap == 0 {
		waitCap = maxRetryWait
	}
	backoff, remaining := policy.WaitTime, maxRetryWait
	var last retryResult
	for tries := 0; ; tries++ {
		if err := ctx.Err(); err != nil {
			if tries == 0 {
				return retryResult{err: fmt.Errorf("httpclient: call abandoned before the first attempt: %w", err)}
			}
			last.err = retryAbandoned(last.err, err)
			return last
		}
		last = attempt(ctx)
		if !RetryableFailure(last.status, last.err) || tries >= policy.Attempts {
			return last
		}
		serverWait, acceptable := retryAfterWait(last.retryAfter, timing.now())
		if !acceptable {
			return last
		}
		delay := max(timing.jitter(backoff), serverWait)
		if delay > remaining || delay > maxRetryWait {
			return last
		}
		if deadline, ok := ctx.Deadline(); ok && delay >= deadline.Sub(timing.now()) {
			return last
		}
		if err := timing.wait(ctx, delay); err != nil {
			last.err = retryAbandoned(last.err, err)
			return last
		}
		remaining -= delay
		// Saturate before multiplication; this also preserves explicit zero.
		if backoff > waitCap-backoff {
			backoff = waitCap
		} else {
			backoff *= 2
		}
	}
}

// Invalid syntax falls back to local backoff. A valid but excessive delay must
// stop retrying, never be shortened into an early request.
func retryAfterWait(raw string, now time.Time) (time.Duration, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, true
	}
	digits := true
	for _, c := range raw {
		if c < '0' || c > '9' {
			digits = false
			break
		}
	}
	if digits {
		seconds, err := strconv.ParseUint(raw, 10, 64)
		if err != nil || seconds > uint64(maxRetryWait/time.Second) {
			return 0, false
		}
		return time.Duration(seconds) * time.Second, true
	}
	date, err := http.ParseTime(raw)
	if err != nil {
		return 0, true
	}
	delay := max(date.Sub(now), 0)
	return delay, delay <= maxRetryWait
}
