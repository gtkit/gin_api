package bootstrap

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestWorkerMetricsExcludeLongRunningLifetime(t *testing.T) {
	t.Parallel()
	observer, reader := newObserverWithReader(t)
	manager := &WorkerManager{recorder: observer.recorder}
	failure := errors.New("one-shot panic")
	for _, tc := range []struct {
		lifecycle Lifecycle
		panicked  bool
	}{
		{LifecycleOneShot, false},
		{LifecycleOneShot, true},
		{LifecycleLongRunning, false},
	} {
		worker := &funcWorker{name: "worker", lifecycle: tc.lifecycle, run: func(context.Context) error {
			if tc.panicked {
				panic(failure)
			}
			return nil
		}}
		err := manager.invokeRun(t.Context(), worker)
		_, isPanic := errors.AsType[*WorkerPanicError](err)
		if isPanic != tc.panicked {
			t.Fatalf("panic boundary changed: %v", err)
		}
	}
	observer.TaskFinished(t.Context(), "singleton", time.Second, nil, false)
	var data metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &data); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int64{}
	for _, scope := range data.ScopeMetrics {
		for _, metric := range scope.Metrics {
			if metric.Name == "worker.executions" {
				counter, ok := metric.Data.(metricdata.Sum[int64])
				if !ok {
					t.Fatalf("metric %q has unexpected type %T", metric.Name, metric.Data)
				}
				for _, point := range counter.DataPoints {
					kind, _ := point.Attributes.Value("worker.kind")
					counts[kind.AsString()] += point.Value
				}
			}
		}
	}
	if counts["one_shot"] != 2 || counts["singleton"] != 1 || len(counts) != 2 {
		t.Fatalf("execution counts = %v", counts)
	}
}

func TestWorkerManagerStartRecordsBothOneShotPaths(t *testing.T) {
	t.Parallel()
	observer, reader := newObserverWithReader(t)
	var calls atomic.Int32
	workers := []*funcWorker{
		{name: "required", kind: KindBootstrap, lifecycle: LifecycleOneShot, required: true},
		{name: "optional", kind: KindBootstrap, lifecycle: LifecycleOneShot},
	}
	for _, worker := range workers {
		worker.run = func(context.Context) error { calls.Add(1); return nil }
	}
	manager, err := NewWorkerManager(DefaultWorkerBudget(), workers[0], workers[1])
	if err != nil {
		t.Fatal(err)
	}
	manager.recorder = observer.recorder
	t.Cleanup(func() {
		if err := manager.StopAll(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	})
	if err := manager.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := manager.StopAll(t.Context()); err != nil {
		t.Fatal(err)
	}
	var data metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &data); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int64{}
	for _, scope := range data.ScopeMetrics {
		for _, metric := range scope.Metrics {
			if metric.Name == "worker.executions" {
				counter, ok := metric.Data.(metricdata.Sum[int64])
				if !ok {
					t.Fatalf("metric %q has unexpected type %T", metric.Name, metric.Data)
				}
				for _, point := range counter.DataPoints {
					name, _ := point.Attributes.Value("worker.name")
					counts[name.AsString()] += point.Value
				}
			}
		}
	}
	if calls.Load() != 2 || counts["required"] != 1 || counts["optional"] != 1 {
		t.Fatalf("calls=%d counts=%v", calls.Load(), counts)
	}
	for _, worker := range workers {
		if worker.cleanupCalls.Load() != 1 {
			t.Errorf("%s cleanup calls = %d", worker.name, worker.cleanupCalls.Load())
		}
	}
}
