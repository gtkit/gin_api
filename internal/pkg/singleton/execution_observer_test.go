package singleton

import (
	"context"
	"errors"
	"testing"
	"time"
)

type executionObservation struct {
	*recordingObserver
	calls    int
	err      error
	panicked bool
	elapsed  time.Duration
}

func (o *executionObservation) TaskFinished(_ context.Context, _ string, elapsed time.Duration, err error, panicked bool) {
	o.calls++
	o.err, o.panicked, o.elapsed = err, panicked, elapsed
}

func TestExecutionObserverCoversOnlyActualTasks(t *testing.T) {
	t.Parallel()
	failure := errors.New("task error")
	for _, tc := range []struct {
		name     string
		acquired bool
		err      error
		panicked bool
	}{
		{name: "no lease"},
		{name: "success", acquired: true},
		{name: "error", acquired: true, err: failure},
		{name: "canceled", acquired: true, err: context.Canceled},
		{name: "panic", acquired: true, panicked: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			acquired := int64(0)
			if tc.acquired {
				acquired = 1
			}
			client := &fakeLeaseClient{evalResults: []leaseEvalResult{{val: []any{acquired, int64(9)}}}}
			observer := &executionObservation{recordingObserver: &recordingObserver{}}
			calls := 0
			runner, err := NewRunner(newFakeLease(t, client), func(context.Context, Held) error {
				calls++
				if tc.panicked {
					panic(failure)
				}
				return tc.err
			}, Config{Interval: time.Hour, Guarantee: GuaranteeIdempotent, Observer: observer})
			if err != nil {
				t.Fatal(err)
			}
			func() {
				defer func() {
					got := recover()
					if tc.panicked && got != failure || !tc.panicked && got != nil {
						t.Errorf("panic changed: %v", got)
					}
				}()
				runner.runOnce(t.Context())
			}()
			want := 0
			if tc.acquired {
				want = 1
			}
			if calls != want || observer.calls != want || observer.panicked != tc.panicked || !errors.Is(observer.err, tc.err) || observer.elapsed < 0 {
				t.Fatalf("calls=%d observation=%+v", calls, observer)
			}
		})
	}
}
