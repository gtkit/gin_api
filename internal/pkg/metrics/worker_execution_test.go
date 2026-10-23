package metrics

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestWorkerExecutionOutcomesAndDuration(t *testing.T) {
	t.Parallel()
	failure := errors.New("task failure")
	cases := []struct {
		name     string
		err      error
		panicked bool
	}{
		{"success", nil, false},
		{"failure", failure, false},
		{"canceled", context.Canceled, false},
		{"timeout", context.DeadlineExceeded, false},
		{"failure", errors.Join(context.Canceled, failure), false},
		{"panic", nil, true},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			recorder, reader := newTestRecorder(t)
			recorder.RecordWorkerExecution(t.Context(), WorkerQueue, "consumer", 2*time.Second, tc.err, tc.panicked)
			counterMetric := collect(t, reader, "worker.executions")
			counter, ok := counterMetric.Data.(metricdata.Sum[int64])
			if !ok {
				t.Fatalf("metric %q has unexpected type %T", counterMetric.Name, counterMetric.Data)
			}
			histogramMetric := collect(t, reader, "worker.execution.duration")
			histogram, ok := histogramMetric.Data.(metricdata.Histogram[float64])
			if !ok {
				t.Fatalf("metric %q has unexpected type %T", histogramMetric.Name, histogramMetric.Data)
			}
			if len(counter.DataPoints) != 1 || counter.DataPoints[0].Value != 1 || len(histogram.DataPoints) != 1 {
				t.Fatal("expected one counter and histogram series")
			}
			point := counter.DataPoints[0]
			outcome, _ := point.Attributes.Value("worker.outcome")
			kind, _ := point.Attributes.Value("worker.kind")
			if outcome.AsString() != tc.name || kind.AsString() != "queue" || point.Attributes.Len() != 3 {
				t.Fatalf("case %d labels = %v", i, point.Attributes)
			}
			if got := histogram.DataPoints[0]; got.Count != 1 || got.Sum != 2 {
				t.Fatalf("duration = %+v", got)
			}
		})
	}
}

func TestRunWorkerPreservesResultAndPanic(t *testing.T) {
	t.Parallel()
	failure := errors.New("original failure")
	recorder, reader := newTestRecorder(t)
	calls := 0
	if err := recorder.RunWorker(t.Context(), WorkerOneShot, "startup", func(ctx context.Context) error {
		calls++
		if ctx != t.Context() {
			t.Fatal("context changed")
		}
		return failure
	}); !errors.Is(err, failure) {
		t.Fatalf("result = %v", err)
	}
	func() {
		defer func() {
			if got := recover(); got != failure {
				t.Errorf("panic changed: %v", got)
			}
		}()
		err := recorder.RunWorker(t.Context(), WorkerOneShot, "startup", func(context.Context) error {
			calls++
			panic(failure)
		})
		t.Errorf("RunWorker returned instead of panicking: %v", err)
	}()
	if calls != 2 || seriesCount(t, collect(t, reader, "worker.executions")) != 2 {
		t.Fatal("execution count or outcomes lost")
	}
	var disabled *Recorder
	if err := disabled.RunWorker(t.Context(), WorkerOneShot, "startup", func(context.Context) error { return failure }); !errors.Is(err, failure) {
		t.Fatal("nil recorder changed execution")
	}
}

func TestWorkerExecutionConcurrentCounts(t *testing.T) {
	t.Parallel()
	recorder, reader := newTestRecorder(t)
	var calls atomic.Int32
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			if err := recorder.RunWorker(t.Context(), WorkerQueue, "consumer", func(context.Context) error {
				calls.Add(1)
				return nil
			}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	counterMetric := collect(t, reader, "worker.executions")
	counter, ok := counterMetric.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("metric %q has unexpected type %T", counterMetric.Name, counterMetric.Data)
	}
	if calls.Load() != 32 || len(counter.DataPoints) != 1 || counter.DataPoints[0].Value != 32 {
		t.Fatal("concurrent executions were lost or duplicated")
	}
}

func TestRunWorkerGoexitRecordsAbnormalExit(t *testing.T) {
	t.Parallel()
	recorder, reader := newTestRecorder(t)
	done := make(chan struct{})
	go func() {
		defer close(done)
		err := recorder.RunWorker(t.Context(), WorkerOneShot, "abnormal", func(context.Context) error {
			runtime.Goexit()
			return nil
		})
		t.Errorf("RunWorker returned instead of exiting goroutine: %v", err)
	}()
	<-done
	counterMetric := collect(t, reader, "worker.executions")
	counter, ok := counterMetric.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("metric %q has unexpected type %T", counterMetric.Name, counterMetric.Data)
	}
	points := counter.DataPoints
	if len(points) != 1 || points[0].Value != 1 {
		t.Fatalf("abnormal exit was not recorded: %v", points)
	}
	outcome, _ := points[0].Attributes.Value("worker.outcome")
	if outcome.AsString() != "panic" {
		t.Fatalf("outcome = %v", outcome)
	}
}
