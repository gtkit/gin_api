package application

import (
	"context"
	"errors"
	"maps"
	"sync"
	"testing"
)

type stored struct {
	owner int64
	value Record
}
type memory struct {
	mu      sync.Mutex
	records map[string]stored
	txCalls int
	fail    error
}

type transactionMemoryKey struct{}

func (m *memory) lock(ctx context.Context) func() {
	if ctx.Value(transactionMemoryKey{}) == m {
		return func() {}
	}
	m.mu.Lock()
	return m.mu.Unlock
}

func (m *memory) Do(ctx context.Context, fn func(context.Context) error) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.txCalls++
	snapshot := make(map[string]stored, len(m.records))
	maps.Copy(snapshot, m.records)
	err := fn(context.WithValue(ctx, transactionMemoryKey{}, m))
	if err != nil {
		m.records = snapshot
	}
	return err
}
func (m *memory) Find(ctx context.Context, owner int64, id string) (Record, bool, error) {
	defer m.lock(ctx)()
	value, found := m.find(owner, id)
	return value, found, nil
}
func (m *memory) find(owner int64, id string) (Record, bool) {
	v, ok := m.records[id]
	return v.value, ok && v.owner == owner
}
func (m *memory) FindForUpdate(ctx context.Context, owner int64, id string) (Record, bool, error) {
	return m.Find(ctx, owner, id)
}
func (m *memory) List(ctx context.Context, owner int64, _, _ int) ([]Record, int64, error) {
	defer m.lock(ctx)()
	out := []Record{}
	for _, v := range m.records {
		if v.owner == owner {
			out = append(out, v.value)
		}
	}
	return out, int64(len(out)), nil
}
func (m *memory) Create(ctx context.Context, owner int64, value Record) error {
	defer m.lock(ctx)()
	if m.fail != nil {
		return m.fail
	}
	m.records[value.ID] = stored{owner, value}
	return nil
}
func (m *memory) Update(ctx context.Context, owner int64, value Record) (bool, error) {
	defer m.lock(ctx)()
	_, ok := m.find(owner, value.ID)
	if ok {
		m.records[value.ID] = stored{owner, value}
	}
	return ok, m.fail
}
func (m *memory) Delete(ctx context.Context, owner int64, id string) (bool, error) {
	defer m.lock(ctx)()
	_, ok := m.find(owner, id)
	if ok {
		delete(m.records, id)
	}
	return ok, m.fail
}
func (m *memory) DeleteMany(ctx context.Context, owner int64, ids []string) (int64, error) {
	defer m.lock(ctx)()
	var count int64
	for _, id := range ids {
		_, ok := m.find(owner, id)
		if ok {
			delete(m.records, id)
			count++
		}
	}
	return count, m.fail
}

func TestCompetingWritesPreserveDeletionAndOwnership(t *testing.T) {
	for _, batch := range []bool{false, true} {
		name := "update and delete"
		if batch {
			name = "batch and single delete"
		}
		t.Run(name, func(t *testing.T) {
			for range 32 {
				ctx := t.Context()
				store := &memory{records: map[string]stored{}}
				service, err := New(store, store)
				if err != nil {
					t.Fatal(err)
				}
				create := func(owner int64, title string) Record {
					t.Helper()
					record, err := callCreate(service, ctx, owner, title)
					if err != nil {
						t.Fatal(err)
					}
					return record
				}
				first, second, foreign := create(7, "first"), create(7, "second"), create(8, "other")
				start := make(chan struct{})
				var wg sync.WaitGroup
				var edit, deleteErr error
				wg.Go(func() {
					<-start
					if batch {
						edit = callDeleteMany(service, ctx, 7, []string{first.ID, second.ID})
						return
					}
					_, edit = callUpdate(service, ctx, 7, first.ID, "updated")
				})
				wg.Go(func() {
					<-start
					deleteErr = callDelete(service, ctx, 7, first.ID)
				})
				close(start)
				wg.Wait()
				if edit != nil && !errors.Is(edit, ErrNotFound) {
					t.Fatalf("competing write: %v", edit)
				}
				if batch && edit == nil {
					if !errors.Is(deleteErr, ErrNotFound) {
						t.Fatalf("batch and single delete both succeeded: %v", deleteErr)
					}
				} else if deleteErr != nil {
					t.Fatalf("single delete: %v", deleteErr)
				}
				if _, err := callGet(service, ctx, 7, first.ID); !errors.Is(err, ErrNotFound) {
					t.Fatalf("deleted row became visible: %v", err)
				}
				if kept, err := callGet(service, ctx, 8, foreign.ID); err != nil || kept != foreign {
					t.Fatalf("another owner's row changed: %+v %v", kept, err)
				}
				_, secondErr := callGet(service, ctx, 7, second.ID)
				if batch && edit == nil {
					if !errors.Is(secondErr, ErrNotFound) {
						t.Fatalf("successful batch kept its second row: %v", secondErr)
					}
				} else if secondErr != nil {
					t.Fatalf("second row changed during rejected batch or update: %v", secondErr)
				}
			}
		})
	}
}

func callGet(service *Service, ctx context.Context, owner int64, id string) (Record, error) {
	return service.Get(ctx, GetRequest{OwnerID: owner, ID: id})
}

func callList(service *Service, ctx context.Context, owner int64, page, size int) (Page, error) {
	return service.List(ctx, ListRequest{OwnerID: owner, Page: page, Size: size})
}

func callCreate(service *Service, ctx context.Context, owner int64, title string) (Record, error) {
	return service.Create(ctx, CreateRequest{OwnerID: owner, Title: title})
}

func callUpdate(service *Service, ctx context.Context, owner int64, id, title string) (Record, error) {
	return service.Update(ctx, UpdateRequest{OwnerID: owner, ID: id, Title: title})
}

func callDelete(service *Service, ctx context.Context, owner int64, id string) error {
	return service.Delete(ctx, DeleteRequest{OwnerID: owner, ID: id})
}

func callDeleteMany(service *Service, ctx context.Context, owner int64, ids []string) error {
	return service.DeleteMany(ctx, DeleteManyRequest{OwnerID: owner, IDs: ids})
}

func TestMixedOwnerBatchRollsBack(t *testing.T) {
	store := &memory{records: map[string]stored{}}
	service, err := New(store, store)
	if err != nil {
		t.Fatal(err)
	}
	own, err := callCreate(service, t.Context(), 7, "own")
	if err != nil {
		t.Fatal(err)
	}
	other, err := callCreate(service, t.Context(), 8, "other")
	if err != nil {
		t.Fatal(err)
	}
	second, err := callCreate(service, t.Context(), 7, "second")
	if err != nil {
		t.Fatal(err)
	}
	for _, ids := range [][]string{
		{other.ID, own.ID, second.ID},
		{own.ID, other.ID, second.ID},
		{own.ID, second.ID, other.ID},
	} {
		if err := callDeleteMany(service, t.Context(), 7, ids); !errors.Is(err, ErrNotFound) {
			t.Fatalf("mixed batch: %v", err)
		}
		if len(store.records) != 3 {
			t.Fatal("rejected batch partially deleted records")
		}
	}
	for _, ids := range [][]string{nil, {own.ID, own.ID}, {"bad"}} {
		if err := callDeleteMany(service, t.Context(), 7, ids); !errors.Is(err, ErrInvalid) {
			t.Fatalf("invalid batch accepted: %v", err)
		}
	}
	if err := callDeleteMany(service, t.Context(), 7, []string{own.ID}); err != nil {
		t.Fatal(err)
	}
	if len(store.records) != 2 {
		t.Fatal("own batch did not delete exactly its owner record")
	}
}

func TestOwnerScopedRecordLifecycle(t *testing.T) {
	store := &memory{records: map[string]stored{}}
	service, err := New(store, store)
	if err != nil {
		t.Fatal(err)
	}
	record, err := callCreate(service, t.Context(), 7, " first ")
	if err != nil {
		t.Fatal(err)
	}
	if record.Title != "first" || !validID(record.ID) {
		t.Fatalf("invalid created record: %+v", record)
	}
	if repeated, err := callUpdate(service, t.Context(), 7, record.ID, "first"); err != nil || repeated != record {
		t.Fatalf("same title changed persisted record: %+v %v", repeated, err)
	}
	if _, err := callGet(service, t.Context(), 8, record.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner get: %v", err)
	}
	if _, err := callUpdate(service, t.Context(), 8, record.ID, "stolen"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner update: %v", err)
	}
	if err := callDelete(service, t.Context(), 8, record.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner delete: %v", err)
	}
	page, err := callList(service, t.Context(), 8, 1, 20)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("cross-owner list: %+v %v", page, err)
	}
	updated, err := callUpdate(service, t.Context(), 7, record.ID, "second")
	if err != nil || updated.Title != "second" {
		t.Fatalf("update: %+v %v", updated, err)
	}
	if err := callDelete(service, t.Context(), 7, record.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := callGet(service, t.Context(), 7, record.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("deleted record: %v", err)
	}
}

func TestValidationAndTransactionRollback(t *testing.T) {
	failure := errors.New("storage unavailable")
	store := &memory{records: map[string]stored{}, fail: failure}
	service, err := New(store, store)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := callCreate(service, t.Context(), 0, "title"); !errors.Is(err, ErrForbidden) {
		t.Fatal(err)
	}
	if _, err := callCreate(service, t.Context(), 7, " "); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := callList(service, t.Context(), 7, 1, 101); !errors.Is(err, ErrInvalid) {
		t.Fatal(err)
	}
	if _, err := callCreate(service, t.Context(), 7, "title"); !errors.Is(err, failure) || len(store.records) != 0 || store.txCalls != 0 {
		t.Fatalf("failed single write changed state or began a transaction: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := callCreate(service, ctx, 7, "title"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
