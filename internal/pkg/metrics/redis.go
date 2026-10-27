package metrics

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"sync"

	"github.com/redis/go-redis/v9"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// RedisPoolStats carries one captured client's snapshots. Client is used only
// as an identity, never as a metric attribute. Nil Pipeline means a shared pool.
type RedisPoolStats struct {
	Client   *redis.Client
	Main     redis.PoolStats
	Pipeline *redis.PoolStats
}

type redisPoolCounter struct {
	initialized               bool
	waits, timeouts           uint32
	waitNanos                 int64
	totalWaits, totalTimeouts int64
	totalWaitSeconds          float64
}

func (c *redisPoolCounter) add(s redis.PoolStats) {
	if c.initialized {
		// Both operands are uint32: subtraction itself has modulo-2^32 semantics.
		c.totalWaits = addPoolCount(c.totalWaits, s.WaitCount-c.waits)
		c.totalTimeouts = addPoolCount(c.totalTimeouts, s.Timeouts-c.timeouts)
		// An unexpected same-client decrease establishes a new baseline rather
		// than inventing a negative duration or treating it as uint32 wrapping.
		if s.WaitDurationNs >= c.waitNanos {
			c.totalWaitSeconds += float64(s.WaitDurationNs-c.waitNanos) / 1e9
		}
	} else {
		c.totalWaits = addPoolCount(c.totalWaits, s.WaitCount)
		c.totalTimeouts = addPoolCount(c.totalTimeouts, s.Timeouts)
		c.totalWaitSeconds += float64(max(s.WaitDurationNs, 0)) / 1e9
	}
	c.initialized = true
	c.waits = s.WaitCount
	c.timeouts = s.Timeouts
	c.waitNanos = max(s.WaitDurationNs, 0)
}

func addPoolCount(total int64, increment uint32) int64 {
	return total + min(int64(increment), math.MaxInt64-total)
}

type redisPoolGeneration struct {
	client         *redis.Client
	generation     uint64
	main, pipeline redisPoolCounter
}

// RegisterRedisPoolStats samples only the finite configured DB set. Snapshots
// must be memory-only and must capture client identity and stats from the same
// client. At most one current generation and two pool roles are retained per DB.
func (r *Recorder) RegisterRedisPoolStats(dbs []int, snapshot func(int) RedisPoolStats) (func() error, error) {
	if r == nil || snapshot == nil || len(dbs) == 0 {
		return func() error { return nil }, nil
	}
	dbs = slices.Clone(dbs)
	slices.Sort(dbs)
	dbs = slices.Compact(dbs)
	for _, db := range dbs {
		if db < 0 {
			return nil, fmt.Errorf("metrics: negative Redis DB")
		}
	}
	m := r.meter
	connections, e1 := m.Int64ObservableUpDownCounter("db.client.connection.count", metric.WithUnit("{connection}"))
	pending, e2 := m.Int64ObservableUpDownCounter("db.client.connection.pending_requests", metric.WithUnit("{request}"))
	timeouts, e3 := m.Int64ObservableCounter("db.client.connection.timeouts", metric.WithUnit("{timeout}"))
	waits, e4 := m.Int64ObservableCounter("pool.wait.count")
	duration, e5 := m.Float64ObservableCounter("pool.wait.duration", metric.WithUnit("s"), metric.WithDescription("Cumulative connection pool wait time"))
	if err := errors.Join(e1, e2, e3, e4, e5); err != nil {
		return nil, fmt.Errorf("metrics: Redis instruments: %w", err)
	}
	states := make(map[int]*redisPoolGeneration, len(dbs))
	for _, db := range dbs {
		states[db] = new(redisPoolGeneration)
	}
	var mu sync.Mutex
	reg, err := m.RegisterCallback(func(_ context.Context, o metric.Observer) error {
		mu.Lock()
		defer mu.Unlock()
		for _, db := range dbs {
			s := snapshot(db)
			if s.Client == nil {
				continue
			}
			state := states[db]
			if state.client != s.Client {
				state.client = s.Client
				state.generation++
				state.main.initialized = false
				state.pipeline.initialized = false
			}
			observe := func(role string, stats redis.PoolStats, counter *redisPoolCounter) {
				counter.add(stats)
				attrs := []attribute.KeyValue{attribute.String("db.client.connection.pool.name", "redis/"+strconv.Itoa(db)+"/"+role), attribute.String("pool.type", "redis"), attribute.Int("redis.db", db), attribute.String("pool.role", role)}
				base := metric.WithAttributes(attrs...)
				used := max(int64(stats.TotalConns)-int64(stats.IdleConns), 0)
				o.ObserveInt64(connections, used, metric.WithAttributes(append(slices.Clone(attrs), attribute.String("db.client.connection.state", "used"))...))
				o.ObserveInt64(connections, int64(stats.IdleConns), metric.WithAttributes(append(slices.Clone(attrs), attribute.String("db.client.connection.state", "idle"))...))
				o.ObserveInt64(pending, int64(stats.PendingRequests), base)
				o.ObserveInt64(timeouts, counter.totalTimeouts, base)
				o.ObserveInt64(waits, counter.totalWaits, base)
				o.ObserveFloat64(duration, counter.totalWaitSeconds, base)
			}
			observe("main", s.Main, &state.main)
			if s.Pipeline != nil {
				observe("pipeline", *s.Pipeline, &state.pipeline)
			}
		}
		return nil
	}, connections, pending, timeouts, waits, duration)
	if err != nil {
		return nil, fmt.Errorf("metrics: Redis callback: %w", err)
	}
	return reg.Unregister, nil
}
