package sid

import (
	"sync"
	"testing"
)

func TestLegacyIDEncoding(t *testing.T) {
	for n, want := range map[int]string{-1: "", 0: "0", 30: "u", 31: "1v", 62: "20", 961: "1vv"} {
		if got := IntToBase62(n); got != want {
			t.Fatalf("IntToBase62(%d)=%q want %q", n, got, want)
		}
	}
	if len(encodeID(^uint64(0))) != 13 {
		t.Fatal("full-width identifier lost")
	}
}

func TestConcurrentInitializationAndGeneration(t *testing.T) {
	var wg sync.WaitGroup
	ids := make(chan uint64, 64)
	for range cap(ids) {
		wg.Go(func() {
			NewSid()
			id, err := Client().GenUint64()
			if err != nil {
				t.Error(err)
				return
			}
			ids <- id
		})
	}
	wg.Wait()
	close(ids)
	seen := map[uint64]bool{}
	for id := range ids {
		if seen[id] {
			t.Fatal("duplicate identifier")
		}
		seen[id] = true
	}
}
