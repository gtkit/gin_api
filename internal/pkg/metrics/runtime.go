package metrics

import (
	"context"
	"errors"
	"fmt"
	"math"
	runtimemetrics "runtime/metrics"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// RegisterRuntimeStats samples only on collection, without a sampling goroutine.
// GC quantiles describe lifetime bucket upper bounds, not a recent alert window.
func (r *Recorder) RegisterRuntimeStats() (func() error, error) {
	if r == nil {
		return func() error { return nil }, nil
	}
	m := r.meter
	goroutines, e1 := m.Int64ObservableUpDownCounter("go.goroutine.count", metric.WithUnit("{goroutine}"))
	memory, e2 := m.Int64ObservableUpDownCounter("go.memory.used", metric.WithUnit("By"))
	heap, e3 := m.Int64ObservableGauge("runtime.heap.used", metric.WithUnit("By"))
	allocated, e4 := m.Int64ObservableCounter("go.memory.allocated", metric.WithUnit("By"))
	cpu, e5 := m.Float64ObservableCounter("go.cpu.time", metric.WithUnit("s"))
	pauses, e6 := m.Int64ObservableCounter("runtime.gc.pause.count")
	quantile, e7 := m.Float64ObservableGauge("runtime.gc.pause.quantile", metric.WithUnit("s"), metric.WithDescription("Lifetime GC pause bucket upper bounds; long-term reference, not recent-window alerts"))
	if err := errors.Join(e1, e2, e3, e4, e5, e6, e7); err != nil {
		return nil, fmt.Errorf("metrics: runtime instruments: %w", err)
	}
	reg, err := m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		samples := []runtimemetrics.Sample{
			{Name: "/sched/goroutines:goroutines"}, {Name: "/memory/classes/total:bytes"},
			{Name: "/memory/classes/heap/released:bytes"}, {Name: "/memory/classes/heap/objects:bytes"},
			{Name: "/gc/heap/allocs:bytes"}, {Name: "/cpu/classes/gc/total:cpu-seconds"}, {Name: "/gc/pauses:seconds"},
		}
		runtimemetrics.Read(samples)
		o.ObserveInt64(goroutines, runtimeCount(samples[0].Value.Uint64()))
		total, released := samples[1].Value.Uint64(), samples[2].Value.Uint64()
		used := uint64(0)
		if total > released {
			used = total - released
		}
		o.ObserveInt64(memory, runtimeCount(used))
		o.ObserveInt64(heap, runtimeCount(samples[3].Value.Uint64()))
		o.ObserveInt64(allocated, runtimeCount(samples[4].Value.Uint64()))
		o.ObserveFloat64(cpu, samples[5].Value.Float64(), metric.WithAttributes(attribute.String("go.cpu.state", "gc")))
		h := samples[6].Value.Float64Histogram()
		var count uint64
		for _, n := range h.Counts {
			count += n
		}
		o.ObserveInt64(pauses, runtimeCount(count))
		for _, q := range []struct {
			name  string
			value float64
		}{{"0.50", .5}, {"0.95", .95}, {"0.99", .99}} {
			if bound, ok := pauseQuantile(h, q.value); ok {
				o.ObserveFloat64(quantile, bound, metric.WithAttributes(attribute.String("quantile", q.name)))
			}
		}
		return nil
	}, goroutines, memory, heap, allocated, cpu, pauses, quantile)
	if err != nil {
		return nil, fmt.Errorf("metrics: runtime callback: %w", err)
	}
	return reg.Unregister, nil
}

// OTel's signed counter must not become negative if a runtime total exceeds it.
func runtimeCount(value uint64) int64 {
	if value > math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(value)
}

func pauseQuantile(h *runtimemetrics.Float64Histogram, q float64) (float64, bool) {
	if h == nil || len(h.Buckets) != len(h.Counts)+1 {
		return 0, false
	}
	var total uint64
	for _, count := range h.Counts {
		total += count
	}
	if total == 0 {
		return 0, false
	}
	target := uint64(math.Ceil(float64(total) * q))
	var count uint64
	for i, n := range h.Counts {
		count += n
		if count >= target {
			upper := h.Buckets[i+1]
			return upper, !math.IsInf(upper, 0) && !math.IsNaN(upper)
		}
	}
	return 0, false
}
