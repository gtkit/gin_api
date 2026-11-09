package metrics

import (
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

func TestNamedPoolMetricsKeepIndependentSeries(t *testing.T) {
	recorder, reader := newTestRecorder(t)
	for _, name := range []string{"", "reports", "archive"} {
		stopDB, err := recorder.RegisterDBPoolStats(func() DBPoolStats {
			return DBPoolStats{ConnectionName: name, Open: 2, InUse: 1, Idle: 1, WaitDuration: time.Second}
		})
		if err != nil {
			t.Fatal(err)
		}
		client := new(redis.Client)
		stopRedis, err := recorder.RegisterRedisPoolStats([]int{0}, func(int) RedisPoolStats {
			return RedisPoolStats{ConnectionName: name, Client: client, Main: redis.PoolStats{TotalConns: 2, IdleConns: 1, WaitDurationNs: int64(time.Second)}}
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := stopDB(); err != nil {
				t.Error(err)
			}
			if err := stopRedis(); err != nil {
				t.Error(err)
			}
		})
	}
	mysql := mustMetricData[metricdata.Gauge[int64]](t, collect(t, reader, "db.client.connections.usage"))
	redisSeries := mustMetricData[metricdata.Sum[int64]](t, collect(t, reader, "db.client.connection.count"))
	if len(mysql.DataPoints) != 6 || len(redisSeries.DataPoints) != 6 {
		t.Fatalf("pool series merged: mysql=%d redis=%d", len(mysql.DataPoints), len(redisSeries.DataPoints))
	}
	waits := mustMetricData[metricdata.Sum[float64]](t, collect(t, reader, "pool.wait.duration"))
	if len(waits.DataPoints) != 6 {
		t.Fatalf("wait series merged: %d", len(waits.DataPoints))
	}
	seen := make(map[string]bool)
	for _, point := range waits.DataPoints {
		pool, _ := point.Attributes.Value("db.client.connection.pool.name")
		seen[pool.AsString()] = true
		name, named := point.Attributes.Value("connection.name")
		if named && name.AsString() != "reports" && name.AsString() != "archive" {
			t.Fatal("unexpected connection label")
		}
		if point.Value != 1 {
			t.Fatalf("unexpected pool wait: %v", point.Value)
		}
	}
	for _, pool := range []string{"mysql/main", "redis/0/main", "mysql/reports/main", "mysql/archive/main", "redis/reports/0/main", "redis/archive/0/main"} {
		if !seen[pool] {
			t.Errorf("missing pool %s", pool)
		}
	}
}
