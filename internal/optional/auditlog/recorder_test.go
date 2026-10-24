package auditlog

import (
	"context"
	"errors"
	"slices"
	"sync"
	"testing"
	"time"
)

type fakeSink struct {
	mu      sync.Mutex
	err     error
	batches [][]Record
	written chan int // 每写出一批发一次批大小，供测试同步（不靠 sleep 猜时序）
}

func (f *fakeSink) WriteBatch(_ context.Context, records []Record) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return f.err
	}
	f.batches = append(f.batches, slices.Clone(records))
	if f.written != nil {
		select {
		case f.written <- len(records):
		default:
		}
	}
	return nil
}

func (f *fakeSink) calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.batches)
}

func (f *fakeSink) totalRecords() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	total := 0
	for _, b := range f.batches {
		total += len(b)
	}
	return total
}

func record(i int) Record {
	return Record{Method: "POST", Route: "/x", Status: 200, UserID: int64(i)}
}

// startRecorder 启动 Run 循环并返回停止函数（取消 + 等退出）。
func startRecorder(t *testing.T, r *Recorder) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := r.Run(ctx); err != nil {
			t.Errorf("audit Run: %v", err)
		}
	}()
	return func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("Run did not return after cancel")
		}
	}
}

func waitFor(t *testing.T, desc string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", desc)
}

func TestNewValidatesDependenciesAndOptions(t *testing.T) {
	t.Parallel()

	sink := &fakeSink{}
	tests := []struct {
		name string
		sink Sink
		opts Options
	}{
		{name: "nil sink", sink: nil},
		{name: "negative queue", sink: sink, opts: Options{QueueSize: -1}},
		{name: "negative interval", sink: sink, opts: Options{FlushInterval: -time.Second}},
		{name: "batch exceeds queue", sink: sink, opts: Options{QueueSize: 10, BatchSize: 11}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(tt.sink, tt.opts); err == nil {
				t.Fatal("New() = nil error, want validation failure")
			}
		})
	}
}

// TestBatchSizeTriggersImmediateFlush 钉住「攒满批立即写出，不等计时器」。
func TestBatchSizeTriggersImmediateFlush(t *testing.T) {
	t.Parallel()

	sink := &fakeSink{written: make(chan int, 4)}
	// 计时器设到一小时：本用例里任何写出都只能由批大小触发。
	r, err := New(sink, Options{BatchSize: 3, FlushInterval: time.Hour})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	stop := startRecorder(t, r)
	defer stop()

	for i := range 3 {
		if !r.Enqueue(record(i)) {
			t.Fatalf("Enqueue #%d = false, want true", i)
		}
	}
	select {
	case size := <-sink.written:
		if size != 3 {
			t.Fatalf("flushed batch size = %d, want 3", size)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("batch was not flushed on reaching batch size")
	}
}

// TestIntervalFlushesPartialBatch 钉住「到期写出不足一批的记录」。
func TestIntervalFlushesPartialBatch(t *testing.T) {
	t.Parallel()

	sink := &fakeSink{written: make(chan int, 4)}
	r, err := New(sink, Options{BatchSize: 100, FlushInterval: 20 * time.Millisecond})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	stop := startRecorder(t, r)
	defer stop()

	r.Enqueue(record(1))
	r.Enqueue(record(2))
	select {
	case size := <-sink.written:
		if size != 2 {
			t.Fatalf("flushed batch size = %d, want 2", size)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("partial batch was not flushed on interval")
	}
}

// TestEnqueueDropsWhenFull 钉住「满载丢弃并计数，绝不阻塞」。
func TestEnqueueDropsWhenFull(t *testing.T) {
	t.Parallel()

	r, err := New(&fakeSink{}, Options{QueueSize: 1, BatchSize: 1})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	// 不启动 Run：队列无人消费，第二条必然满载。
	if !r.Enqueue(record(1)) {
		t.Fatal("first Enqueue = false, want true")
	}
	if r.Enqueue(record(2)) {
		t.Fatal("second Enqueue = true on full queue, want false")
	}
	if got := r.Dropped(); got != 1 {
		t.Fatalf("Dropped() = %d, want 1", got)
	}
}

// TestSinkFailureDropsBatchWithoutRetry 钉住「落库故障整批丢弃，不做无界重试」。
func TestSinkFailureDropsBatchWithoutRetry(t *testing.T) {
	t.Parallel()

	sink := &fakeSink{err: errors.New("db down")}
	r, err := New(sink, Options{BatchSize: 2, FlushInterval: 10 * time.Millisecond})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	stop := startRecorder(t, r)
	defer stop()

	r.Enqueue(record(1))
	r.Enqueue(record(2))
	waitFor(t, "failed batch counted as dropped", func() bool { return r.Dropped() == 2 })

	// 失败批不重试：等几个 flush 周期，丢弃数不再增长（队列里也没有记录可重写）。
	time.Sleep(50 * time.Millisecond)
	if got := r.Dropped(); got != 2 {
		t.Fatalf("Dropped() = %d after idle periods, want 2 (no retry)", got)
	}
}

// TestDrainFlushesPendingOnCancel 钉住关停排空契约。
// 反证：删掉 Run 的 drain 调用，本测试必须失败。
func TestDrainFlushesPendingOnCancel(t *testing.T) {
	t.Parallel()

	sink := &fakeSink{}
	// 批大 + 计时器一小时：取消前不可能有任何常规写出，
	// sink 收到的每一条都只能来自 drain。
	r, err := New(sink, Options{BatchSize: 100, FlushInterval: time.Hour})
	if err != nil {
		t.Fatalf("New() = %v", err)
	}
	stop := startRecorder(t, r)

	for i := range 5 {
		if !r.Enqueue(record(i)) {
			t.Fatalf("Enqueue #%d = false", i)
		}
	}
	stop() // 取消并等 Run 返回：drain 已完成

	if got := sink.totalRecords(); got != 5 {
		t.Fatalf("sink received %d records after drain, want 5", got)
	}
	if got := r.Dropped(); got != 0 {
		t.Fatalf("Dropped() = %d, want 0", got)
	}
	if sink.calls() == 0 {
		t.Fatal("sink was never called")
	}
}
