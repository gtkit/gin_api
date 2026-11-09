package bootstrap

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/gtkit/gin-api/internal/pkg/metrics"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func newWorkerRecorderWithReader(t *testing.T) (*metrics.Recorder, *sdkmetric.ManualReader) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		if err := provider.Shutdown(context.Background()); err != nil {
			t.Error(err)
		}
	})
	recorder, err := metrics.New(provider.Meter("github.com/gtkit/gin-api"))
	if err != nil {
		t.Fatal(err)
	}
	return recorder, reader
}

func TestWorkerMetricsExcludeLongRunningLifetime(t *testing.T) {
	t.Parallel()
	recorder, reader := newWorkerRecorderWithReader(t)
	manager := &WorkerManager{recorder: recorder}
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
	if counts["one_shot"] != 2 || len(counts) != 1 {
		t.Fatalf("execution counts = %v", counts)
	}
}

func TestWorkerManagerStartRecordsBothOneShotPaths(t *testing.T) {
	t.Parallel()
	recorder, reader := newWorkerRecorderWithReader(t)
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
	manager.recorder = recorder
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
