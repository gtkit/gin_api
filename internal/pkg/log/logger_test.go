package log

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type captureLogger struct {
	mu      sync.Mutex
	entries []LogEntry
	ids     []string
}

func (c *captureLogger) Log(ctx context.Context, e LogEntry) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries = append(c.entries, e)
	c.ids = append(c.ids, RequestIDFromContext(ctx))
}
func TestSelectedLoggerContract(t *testing.T) {
	sink := &captureLogger{}
	var drained atomic.Int32
	release, err := Install(sink, func(context.Context) error { drained.Add(1); return nil })
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	if _, err := Install(&captureLogger{}, nil); !errors.Is(err, ErrLoggerInstalled) {
		t.Fatalf("duplicate install=%v", err)
	}
	ctx := ContextWithRequestID(t.Context(), "internal-id")
	failure := errors.New("failure")
	for _, tc := range []struct {
		name  string
		write func()
		level Level
		text  string
		alert bool
	}{
		{"debug", func() { Debug("debug", String("key", "value")) }, LevelDebug, "debug", false},
		{"info", func() { Info("info", Int("count", 1)) }, LevelInfo, "info", false},
		{"warn", func() { Warn("warn") }, LevelWarn, "warn", false},
		{"error", func() { Error("error", Err(failure)) }, LevelError, "error", false},
		{"debugf", func() { Debugf("debug %d", 2) }, LevelDebug, "debug 2", false},
		{"infof", func() { Infof("info %d", 2) }, LevelInfo, "info 2", false},
		{"warnf", func() { Warnf("warn %d", 2) }, LevelWarn, "warn 2", false},
		{"errorf", func() { Errorf("error %d", 2) }, LevelError, "error 2", false},
		{"alert", func() { HErrorf("alert %d", 2) }, LevelError, "alert 2", true},
		{"warn context", func() { WarnCtx(ctx, "ctx") }, LevelWarn, "ctx", false},
		{"error context", func() { ErrorCtx(ctx, "ctx error") }, LevelError, "ctx error", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.write()
			got := sink.entries[len(sink.entries)-1]
			if got.Level != tc.level || got.Text() != tc.text || got.Alert != tc.alert {
				t.Fatalf("event=%+v", got)
			}
		})
	}
	if sink.ids[len(sink.ids)-1] != "internal-id" {
		t.Fatal("lost request ID")
	}
	for _, f := range []Field{String("s", "v"), Int("i", 1), Int32("i32", 2), Int64("i64", 3), Duration("duration", time.Second), Any("a", true), Err(failure), NamedError("named", failure), Stack("stack")} {
		if f.Key == "" || f.Value == nil {
			t.Fatalf("empty field=%+v", f)
		}
	}
	if f := NamedError("nil", nil); f.Key != "" {
		t.Fatal("nil error became field")
	}
	SyncAccessLog()
	Sync()
	if drained.Load() != 2 {
		t.Fatal("facade must delegate draining to owner")
	}
}
func TestLoggerBindingOwnershipAndNil(t *testing.T) {
	var typedNil *captureLogger
	for _, logger := range []Logger{nil, typedNil} {
		if _, err := Install(logger, nil); !errors.Is(err, ErrInvalidLogger) {
			t.Fatalf("nil logger=%v", err)
		}
	}
	oldRelease, err := Install(&captureLogger{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	oldRelease()
	next := &captureLogger{}
	release, err := Install(next, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	oldRelease()
	oldRelease()
	Info("survived")
	if len(next.entries) != 1 {
		t.Fatal("stale release removed new binding")
	}
}
func TestLoggerConcurrentForwarding(t *testing.T) {
	sink := &captureLogger{}
	release, err := Install(sink, nil)
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for range 32 {
		group.Go(func() {
			for range 100 {
				Info("parallel")
			}
		})
	}
	group.Wait()
	release()
	if len(sink.entries) != 3200 {
		t.Fatalf("entries=%d", len(sink.entries))
	}
	if err := Drain(t.Context()); err != nil {
		t.Fatal(err)
	}
	if RequestIDFromContext(t.Context()) != "" {
		t.Fatal("unexpected ID")
	}
}
