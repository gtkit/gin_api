package dao

import (
	"sync"
	"testing"
)

// Concurrent borrowed view Close must leave its owner's live SQL pool usable.
func TestResourcesCloseIsSafeUnderConcurrentCallers(t *testing.T) {
	t.Parallel()
	client, mock := newSQLMockOrmxClient(t)
	view, err := ResourcesFromClients(client, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	var group sync.WaitGroup
	for range 32 {
		group.Go(func() {
			if err := view.Close(); err != nil {
				t.Error(err)
			}
		})
	}
	group.Wait()
	mock.ExpectPing()
	if err := client.PingContext(t.Context()); err != nil {
		t.Fatal("borrower closed owner:", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// TestResourcesCloseNilSafeUnderConcurrency 覆盖 nil 接收者的并发路径。
//
// 关停链上多处写 `defer res.Close()`，而装配失败时 res 可能是 nil。
// 与上一条同源：nil 判定若放在锁内侧就会先解引用。
func TestResourcesCloseNilSafeUnderConcurrency(t *testing.T) {
	t.Parallel()

	var res *Resources
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			if err := res.Close(); err != nil {
				t.Errorf("nil Resources 的 Close() = %v, want nil", err)
			}
		})
	}
	wg.Wait()
}
