package taskqueue

import (
	"context"
	"errors"
	"sync"
	"testing"
	"testing/synctest"
	"time"
)

// Serialization is inside asynqx's active-enqueue lifetime. Blocking it avoids
// relying on Redis timing while exercising the real underlying producer.
type blockedProducerPayload struct {
	entered chan struct{}
	release chan struct{}
}

func (p blockedProducerPayload) MarshalJSON() ([]byte, error) {
	close(p.entered)
	<-p.release
	return nil, errors.New("serialization stopped by test")
}

func TestProducerShutdownResumesWaiting(t *testing.T) {
	_, client := newTestRedis(t)
	synctest.Test(t, func(t *testing.T) {
		p, err := NewProducer(enabledConfig(), client)
		if err != nil {
			t.Fatal(err)
		}
		payload := blockedProducerPayload{make(chan struct{}), make(chan struct{})}
		var enqueue sync.WaitGroup
		enqueue.Go(func() {
			if err := p.Enqueue(t.Context(), "test:blocked", payload); err == nil {
				t.Error("expected serialization failure")
			}
		})
		<-payload.entered
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if err := p.Shutdown(ctx); !errors.Is(err, context.Canceled) {
			t.Errorf("first Shutdown = %v, want cancellation", err)
		}
		ctx, cancel = context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if err := p.Shutdown(ctx); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("second Shutdown = %v, want continued waiting until deadline", err)
		}
		var closing sync.WaitGroup
		for range 8 {
			closing.Go(func() {
				if err := p.Shutdown(t.Context()); err != nil {
					t.Errorf("concurrent Shutdown = %v", err)
				}
			})
		}
		synctest.Wait()
		close(payload.release)
		enqueue.Wait()
		closing.Wait()
		if err := p.Close(); err != nil {
			t.Fatal(err)
		}
	})
	if err := client.Ping(t.Context()).Err(); err != nil {
		t.Fatalf("borrowed Redis client was closed: %v", err)
	}
}
