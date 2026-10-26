package metrics

import (
	"math"
	runtimemetrics "runtime/metrics"
	"testing"

	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestRuntimeMetricTypesAndAttributes(t *testing.T) {
	r, reader := newTestRecorder(t)
	stop, err := r.RegisterRuntimeStats()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := stop(); err != nil {
			t.Error(err)
		}
	}()
	for _, name := range []string{"go.goroutine.count", "go.memory.used", "go.memory.allocated", "runtime.gc.pause.count"} {
		m := collect(t, reader, name)
		sum, ok := m.Data.(metricdata.Sum[int64])
		if !ok || len(sum.DataPoints) != 1 {
			t.Fatalf("%s: %T", name, m.Data)
		}
		wantMonotonic := name == "go.memory.allocated" || name == "runtime.gc.pause.count"
		if sum.IsMonotonic != wantMonotonic || sum.DataPoints[0].Attributes.Len() != 0 || sum.DataPoints[0].Value < 0 {
			t.Fatalf("%s: %+v", name, sum)
		}
	}
	heap := mustMetricData[metricdata.Gauge[int64]](t, collect(t, reader, "runtime.heap.used"))
	if heap.DataPoints[0].Attributes.Len() != 0 {
		t.Fatal("heap attributes")
	}
	cpu := mustMetricData[metricdata.Sum[float64]](t, collect(t, reader, "go.cpu.time"))
	if !cpu.IsMonotonic {
		t.Fatal("CPU must be cumulative")
	}
	assertMetricAttrs(t, cpu.DataPoints[0].Attributes, map[string]any{"go.cpu.state": "gc"})
}

func TestPauseQuantileUsesFiniteLifetimeBucketUpperBound(t *testing.T) {
	for _, tc := range []struct {
		name    string
		h       runtimemetrics.Float64Histogram
		q, want float64
		valid   bool
	}{
		{"empty", runtimemetrics.Float64Histogram{Buckets: []float64{0, 1}, Counts: []uint64{0}}, .99, 0, false},
		{"median", runtimemetrics.Float64Histogram{Buckets: []float64{0, .01, .1, math.Inf(1)}, Counts: []uint64{6, 3, 1}}, .5, .01, true},
		{"tail infinity", runtimemetrics.Float64Histogram{Buckets: []float64{0, .01, .1, math.Inf(1)}, Counts: []uint64{6, 3, 1}}, .99, 0, false},
		{"p95", runtimemetrics.Float64Histogram{Buckets: []float64{0, .01, .1, 1}, Counts: []uint64{6, 3, 1}}, .95, 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := pauseQuantile(&tc.h, tc.q)
			if ok != tc.valid || (ok && got != tc.want) {
				t.Fatalf("got %v/%v", got, ok)
			}
		})
	}
}

func TestRuntimeCounterDoesNotTurnNegative(t *testing.T) {
	if runtimeCount(math.MaxUint64) != math.MaxInt64 || runtimeCount(0) != 0 {
		t.Fatal("runtime counter overflow")
	}
}
