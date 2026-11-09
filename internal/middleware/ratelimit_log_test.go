package middleware

import (
	"context"
	"errors"
	"net/http"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/pkg/metrics"
	"github.com/gtkit/gin-api/internal/pkg/ratelimit"
	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestRateLimitLogWindowAndRecovery(t *testing.T) {
	now := time.Unix(1, 0)
	state := rateLimitLogState{now: func() time.Time { return now }}
	boom := errors.New("backend unavailable")
	if action, _ := state.observe(boom); action != rateLimitLogFailure {
		t.Fatal("first failure was not logged")
	}
	if action, _ := state.observe(context.Canceled); action != rateLimitLogNone {
		t.Fatal("client cancellation generated a dependency event")
	}
	for range 10 {
		if action, _ := state.observe(errors.New("changed error text")); action != rateLimitLogNone {
			t.Fatal("error text bypassed the window")
		}
	}
	now = now.Add(rateLimitLogInterval)
	if action, suppressed := state.observe(boom); action != rateLimitLogFailure || suppressed != 10 {
		t.Fatalf("boundary action=%d suppressed=%d", action, suppressed)
	}
	state.observe(boom)
	if action, _ := state.observe(context.Canceled); action != rateLimitLogNone {
		t.Fatal("cancel generated a false recovery")
	}
	if action, suppressed := state.observe(nil); action != rateLimitLogRecovery || suppressed != 1 {
		t.Fatalf("recovery action=%d suppressed=%d", action, suppressed)
	}
	if action, _ := state.observe(nil); action != rateLimitLogNone {
		t.Fatal("recovery repeated")
	}
	if action, _ := state.observe(boom); action != rateLimitLogFailure {
		t.Fatal("failure after recovery did not log immediately")
	}
}

func TestRateLimitLogConcurrentWindowIsInstanceLocal(t *testing.T) {
	state := rateLimitLogState{now: func() time.Time { return time.Unix(1, 0) }}
	var emitted atomic.Int32
	var group sync.WaitGroup
	for range 100 {
		group.Go(func() {
			if action, _ := state.observe(errors.New("down")); action == rateLimitLogFailure {
				emitted.Add(1)
			}
		})
	}
	group.Wait()
	if emitted.Load() != 1 {
		t.Fatalf("emitted %d failures, want 1", emitted.Load())
	}
	if action, suppressed := state.observe(nil); action != rateLimitLogRecovery || suppressed != 99 {
		t.Fatalf("recovery action=%d suppressed=%d", action, suppressed)
	}
	other := rateLimitLogState{now: time.Now}
	if action, _ := other.observe(errors.New("down")); action != rateLimitLogFailure {
		t.Fatal("another instance shared the suppression state")
	}
}

func TestRateLimitLogSuppressionPreservesHTTPDecision(t *testing.T) {
	_, read := newNativeTestLogger(t, "info")
	limiter := &fakeLimiter{policy: testPolicy("p.log", ratelimit.ScopeGlobal),
		decision: ratelimit.Decision{Outcome: ratelimit.OutcomeAllowed, Limit: 10, Remaining: 9}, err: errors.New("down")}
	mw := RateLimit(limiter, testKeyBuilder(t), nil)
	for range 3 {
		if rec := serveRateLimited(t, mw); rec.Code != http.StatusOK || rec.Header().Get(headerRateRemaining) != "9" {
			t.Fatalf("suppression changed allowed decision: %d %v", rec.Code, rec.Header())
		}
	}
	limiter.err = nil
	serveRateLimited(t, mw)
	entries := read()
	if len(entries) != 2 || entries[0]["level"] != "WARN" || entries[1]["level"] != "INFO" || entries[1]["suppressed"] != float64(2) {
		t.Fatalf("failure/recovery logs = %v", entries)
	}
}

func TestRateLimitLogKeepsDecisionMetrics(t *testing.T) {
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		if err := provider.Shutdown(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	})
	recorder, err := metrics.New(provider.Meter("rate-limit-log-test"))
	if err != nil {
		t.Fatal(err)
	}
	limiter := &fakeLimiter{policy: testPolicy("p.metrics", ratelimit.ScopeGlobal),
		decision: ratelimit.Decision{Outcome: ratelimit.OutcomeAllowed, Limit: 10}, err: errors.New("down")}
	mw := RateLimit(limiter, testKeyBuilder(t), recorder)
	for range 3 {
		serveRateLimited(t, mw)
	}
	limiter.decision.Outcome = ratelimit.OutcomeKeyBudgetExhausted
	limiter.err = nil
	if rec := serveRateLimited(t, mw); rec.Code != http.StatusTooManyRequests {
		t.Fatalf("key limit returned %d", rec.Code)
	}
	var data metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &data); err != nil {
		t.Fatal(err)
	}
	var total, keyLimit int64
	for _, scope := range data.ScopeMetrics {
		for _, metric := range scope.Metrics {
			if metric.Name != "ratelimit.decisions" {
				continue
			}
			sum, ok := metric.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("unexpected metric data %T", metric.Data)
			}
			for _, point := range sum.DataPoints {
				total += point.Value
				if outcome, _ := point.Attributes.Value(attribute.Key("ratelimit.outcome")); outcome.AsString() == "key_limit" {
					keyLimit += point.Value
				}
			}
		}
	}
	if total != 4 || keyLimit != 1 {
		t.Fatalf("decisions=%d key_limit=%d, want 4/1", total, keyLimit)
	}
}
