package metrics

import (
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestRedisPoolCountersWrapAndChangeClient(t *testing.T) {
	r, reader := newTestRecorder(t)
	client1, client2 := new(redis.Client), new(redis.Client)
	state := RedisPoolStats{Client: client1, Main: redis.PoolStats{TotalConns: 5, IdleConns: 2, PendingRequests: 3, WaitCount: math.MaxUint32 - 1, Timeouts: math.MaxUint32 - 1, WaitDurationNs: 100}, Pipeline: &redis.PoolStats{TotalConns: 2, IdleConns: 3, WaitCount: math.MaxUint32 - 2, Timeouts: math.MaxUint32 - 2, WaitDurationNs: 50}}
	var samples atomic.Int64
	stop, err := r.RegisterRedisPoolStats([]int{2}, func(db int) RedisPoolStats {
		samples.Add(1)
		if db != 2 {
			t.Errorf("unexpected DB %d", db)
		}
		return state
	})
	if err != nil {
		t.Fatal(err)
	}
	connections := mustMetricData[metricdata.Sum[int64]](t, collect(t, reader, "db.client.connection.count"))
	for name, unit := range map[string]string{"db.client.connection.count": "{connection}", "db.client.connection.pending_requests": "{request}", "db.client.connection.timeouts": "{timeout}"} {
		m := collect(t, reader, name)
		if m.Unit != unit {
			t.Fatalf("%s unit=%s want %s", name, m.Unit, unit)
		}
	}
	if connections.IsMonotonic || len(connections.DataPoints) != 4 {
		t.Fatalf("connections: %+v", connections)
	}
	for _, dp := range connections.DataPoints {
		role, _ := dp.Attributes.Value("pool.role")
		status, _ := dp.Attributes.Value("db.client.connection.state")
		want := int64(0)
		if role.AsString() == "main" {
			if status.AsString() == "used" {
				want = 3
			} else {
				want = 2
			}
		} else if status.AsString() == "idle" {
			want = 3
		}
		if dp.Value != want {
			t.Fatalf("%v=%d want %d", dp.Attributes, dp.Value, want)
		}
		assertMetricAttrs(t, dp.Attributes, map[string]any{"pool.type": "redis", "pool.role": role.AsString(), "redis.db": int64(2), "db.client.connection.pool.name": "redis/2/" + role.AsString(), "db.client.connection.state": status.AsString()})
	}
	state.Main.WaitCount = 1
	state.Main.Timeouts = 1
	state.Main.WaitDurationNs = 110
	state.Pipeline.WaitCount = 2
	state.Pipeline.Timeouts = 2
	state.Pipeline.WaitDurationNs = 70
	assertRedisTotals(t, collect(t, reader, "pool.wait.count"), int64(math.MaxUint32)+2, int64(math.MaxUint32)+3)
	assertRedisTotals(t, collect(t, reader, "db.client.connection.timeouts"), int64(math.MaxUint32)+2, int64(math.MaxUint32)+3)
	state.Client = client2
	state.Main.WaitCount = 10
	state.Main.Timeouts = 10
	state.Main.WaitDurationNs = 30
	state.Pipeline = nil
	assertRedisTotals(t, collect(t, reader, "pool.wait.count"), int64(math.MaxUint32)+12, -1)
	state.Main.WaitDurationNs = 5
	duration := mustMetricData[metricdata.Sum[float64]](t, collect(t, reader, "pool.wait.duration"))
	if len(duration.DataPoints) != 1 || math.Abs(duration.DataPoints[0].Value-140e-9) > 1e-15 {
		t.Fatalf("duration: %+v", duration)
	}
	assertMetricAttrs(t, duration.DataPoints[0].Attributes, map[string]any{"pool.type": "redis", "pool.role": "main", "redis.db": int64(2), "db.client.connection.pool.name": "redis/2/main"})
	// Also replace the client with smaller counters; neither replacement is a wrap.
	state.Client = new(redis.Client)
	state.Main.WaitCount = 0
	state.Main.Timeouts = 0
	state.Main.WaitDurationNs = 0
	assertRedisTotals(t, collect(t, reader, "pool.wait.count"), int64(math.MaxUint32)+12, -1)
	state.Client = nil
	var collected metricdata.ResourceMetrics
	if err := reader.Collect(t.Context(), &collected); err != nil {
		t.Fatal(err)
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	before := samples.Load()
	if err := reader.Collect(t.Context(), &collected); err != nil {
		t.Fatal(err)
	}
	if samples.Load() != before {
		t.Fatal("callback retained after unregister")
	}
}

func assertRedisTotals(t *testing.T, m metricdata.Metrics, main, pipeline int64) {
	t.Helper()
	sum := mustMetricData[metricdata.Sum[int64]](t, m)
	if !sum.IsMonotonic {
		t.Fatal("non-monotonic counter")
	}
	wantLen := 1
	if pipeline >= 0 {
		wantLen = 2
	}
	if len(sum.DataPoints) != wantLen {
		t.Fatalf("%s series=%d", m.Name, len(sum.DataPoints))
	}
	for _, dp := range sum.DataPoints {
		role, _ := dp.Attributes.Value("pool.role")
		want := main
		if role.AsString() == "pipeline" {
			want = pipeline
		}
		if dp.Value != want || dp.Attributes.Len() != 4 {
			t.Fatalf("%s %v=%d want %d", m.Name, dp.Attributes, dp.Value, want)
		}
	}
}

func TestPoolCollectionAndUnregisterConcurrent(t *testing.T) {
	r, reader := newTestRecorder(t)
	client := new(redis.Client)
	stop, err := r.RegisterRedisPoolStats([]int{0}, func(int) RedisPoolStats { return RedisPoolStats{Client: client} })
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 10 {
				var data metricdata.ResourceMetrics
				if err := reader.Collect(t.Context(), &data); err != nil {
					t.Error(err)
				}
			}
		})
	}
	if err := stop(); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
}

func TestDatabaseWaitDurationPreservesExistingLabels(t *testing.T) {
	r, reader := newTestRecorder(t)
	stop, err := r.RegisterDBPoolStats(func() DBPoolStats {
		return DBPoolStats{InUse: 2, Idle: 3, MaxOpen: 25, Wait: 4, WaitDuration: 150 * time.Millisecond}
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := stop(); err != nil {
			t.Error(err)
		}
	})
	d := mustMetricData[metricdata.Sum[float64]](t, collect(t, reader, "pool.wait.duration"))
	if !d.IsMonotonic || d.DataPoints[0].Value != .15 {
		t.Fatalf("wait=%+v", d)
	}
	assertMetricAttrs(t, d.DataPoints[0].Attributes, map[string]any{"pool.type": "mysql", "pool.role": "main", "db.client.connection.pool.name": "mysql/main"})
	old := mustMetricData[metricdata.Gauge[int64]](t, collect(t, reader, "db.client.connections.usage"))
	for _, dp := range old.DataPoints {
		state, _ := dp.Attributes.Value("db.connection.state")
		assertMetricAttrs(t, dp.Attributes, map[string]any{"db.connection.state": state.AsString()})
	}
}

func TestRedisAccumulationDoesNotOverflowSignedTotals(t *testing.T) {
	if addPoolCount(math.MaxInt64-1, 3) != math.MaxInt64 {
		t.Fatal("count wrapped negative")
	}
	state := redisPoolCounter{}
	state.add(redis.PoolStats{WaitDurationNs: math.MaxInt64})
	state.initialized = false // replacement client: preserve exporter total
	state.add(redis.PoolStats{WaitDurationNs: math.MaxInt64})
	if state.totalWaitSeconds <= float64(math.MaxInt64)/1e9 || math.IsInf(state.totalWaitSeconds, 0) {
		t.Fatal("wait duration wrapped or disappeared")
	}
}
