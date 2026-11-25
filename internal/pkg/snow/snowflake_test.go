package snow

import (
	"sync"
	"testing"
)

func TestWorkerIDsConcurrent(t *testing.T) {
	w := NewWorkers(2, 3)
	var mu sync.Mutex
	seen := map[uint64]struct{}{}
	var wg sync.WaitGroup
	for range 16 {
		wg.Go(func() {
			for range 1024 {
				id, err := w.NextID()
				if err != nil {
					t.Error(err)
					return
				}
				mu.Lock()
				if _, ok := seen[id]; ok {
					t.Error("duplicate id")
				}
				seen[id] = struct{}{}
				mu.Unlock()
			}
		})
	}
	wg.Wait()
	if _, err := NewWorkers(32, 0).SonwID(); err == nil {
		t.Fatal("invalid worker accepted")
	}
}
