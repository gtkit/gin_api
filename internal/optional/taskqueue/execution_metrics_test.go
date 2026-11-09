package taskqueue

import (
	"context"
	"errors"
	"testing"

	"github.com/gtkit/gin-api/internal/pkg/metrics"
	"github.com/hibiken/asynq"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestQueueExecutionMetricsPreserveAttempts(t *testing.T) {
	t.Parallel()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() {
		if err := provider.Shutdown(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	})
	recorder, err := metrics.New(provider.Meter("queue-test"))
	if err != nil {
		t.Fatal(err)
	}
	failure := errors.New("retry this task")
	calls := 0
	mux := asynq.NewServeMux()
	mux.Use(executionMetrics(recorder, "consumer"))
	mux.HandleFunc("mail:", func(context.Context, *asynq.Task) error {
		calls++
		return failure
	})
	for _, taskType := range []string{"mail:first-user", "mail:another-user"} {
		if err := mux.ProcessTask(t.Context(), asynq.NewTask(taskType, []byte("private payload"))); !errors.Is(err, failure) {
			t.Fatalf("task error changed: %v", err)
		}
	}
	var data metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &data); err != nil {
		t.Fatal(err)
	}
	var count int64
	var durationCount uint64
	for _, scope := range data.ScopeMetrics {
		for _, metric := range scope.Metrics {
			switch metric.Name {
			case "worker.executions":
				counter, ok := metric.Data.(metricdata.Sum[int64])
				if !ok {
					t.Fatalf("metric %q has unexpected type %T", metric.Name, metric.Data)
				}
				points := counter.DataPoints
				if len(points) != 1 || points[0].Attributes.Len() != 3 {
					t.Fatal("task-specific labels leaked into metrics")
				}
				name, _ := points[0].Attributes.Value("worker.name")
				outcome, _ := points[0].Attributes.Value("worker.outcome")
				if name.AsString() != "consumer" || outcome.AsString() != "failure" {
					t.Fatal("wrong execution labels")
				}
				count = points[0].Value
			case "worker.execution.duration":
				histogram, ok := metric.Data.(metricdata.Histogram[float64])
				if !ok {
					t.Fatalf("metric %q has unexpected type %T", metric.Name, metric.Data)
				}
				for _, point := range histogram.DataPoints {
					durationCount += point.Count
				}
			}
		}
	}
	if calls != 2 || count != 2 || durationCount != 2 {
		t.Fatalf("calls=%d count=%d durations=%d", calls, count, durationCount)
	}
}

func TestQueueExecutionDoesNotRecoverPanic(t *testing.T) {
	t.Parallel()
	failure := errors.New("handler panic")
	wrapped := executionMetrics(nil, "consumer")(asynq.HandlerFunc(func(context.Context, *asynq.Task) error {
		panic(failure)
	}))
	defer func() {
		if got := recover(); got != failure {
			t.Errorf("panic changed: %v", got)
		}
	}()
	err := wrapped.ProcessTask(t.Context(), asynq.NewTask("task", nil))
	t.Errorf("ProcessTask returned instead of panicking: %v", err)
}
