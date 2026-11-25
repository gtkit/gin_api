package application

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gtkit/gouploader"
)

// fakeRepo 是内存版归属仓储，复刻唯一键 (owner, storage_key) 的幂等语义。
type fakeRepo struct {
	mu   sync.Mutex
	byID map[string]Record
}

func newFakeRepo() *fakeRepo { return &fakeRepo{byID: map[string]Record{}} }

func (f *fakeRepo) Create(_ context.Context, record Record) (Record, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, existing := range f.byID {
		if existing.OwnerID == record.OwnerID && existing.StorageKey == record.StorageKey {
			return existing, nil
		}
	}
	f.byID[record.ID] = record
	return record, nil
}

func (f *fakeRepo) Find(_ context.Context, owner int64, id string) (Record, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	record, ok := f.byID[id]
	if !ok || record.OwnerID != owner {
		return Record{}, false, nil
	}
	return record, true, nil
}

func (f *fakeRepo) List(_ context.Context, owner int64, offset, limit int) ([]Record, int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var owned []Record
	for _, record := range f.byID {
		if record.OwnerID == owner {
			owned = append(owned, record)
		}
	}
	slices.SortFunc(owned, func(a, b Record) int { return b.CreatedAt.Compare(a.CreatedAt) })
	total := int64(len(owned))
	if offset >= len(owned) {
		return nil, total, nil
	}
	return owned[offset:min(offset+limit, len(owned))], total, nil
}

func (f *fakeRepo) Delete(_ context.Context, owner int64, id string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	record, ok := f.byID[id]
	if !ok || record.OwnerID != owner {
		return false, nil
	}
	delete(f.byID, id)
	return true, nil
}

func (f *fakeRepo) len() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.byID)
}

func newTestService(t *testing.T, repo Repository, driver string) (*Service, *gouploader.Uploader) {
	t.Helper()

	up, err := gouploader.New(
		gouploader.WithLocalStorage(t.TempDir()),
		gouploader.WithChunkTempDir(t.TempDir()),
		gouploader.WithMaxFileSize(1<<20),
		gouploader.WithAllowedTypes(".txt"),
		gouploader.WithChunkSize(5, 1, 1<<20),
		// 与 NewUploader 装配同款身份策略：会话归属校验依赖 UserIDFn。
		gouploader.WithUserIDFn(func(ctx context.Context) string {
			if info, ok := gouploader.UploadInfoFromContext(ctx); ok {
				return info.UserID
			}
			return ""
		}),
		gouploader.WithRequireUserID(),
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(up.Close)
	svc, err := New(up, repo, driver, "")
	if err != nil {
		t.Fatal(err)
	}
	return svc, up
}

func readBack(t *testing.T, up *gouploader.Uploader, key string) []byte {
	t.Helper()

	rc, err := up.Storage().Get(t.Context(), key)
	if err != nil {
		t.Fatalf("Get(%q) = %v", key, err)
	}
	defer func() {
		if err := rc.Close(); err != nil {
			t.Errorf("rc.Close(): %v", err)
		}
	}()
	data, err := io.ReadAll(rc)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestUploadRecordsAndReadsBack(t *testing.T) {
	t.Parallel()

	repo := newFakeRepo()
	svc, up := newTestService(t, repo, "local")
	const payload = "hello upload"
	record, err := svc.Upload(t.Context(), 7, "a.txt", strings.NewReader(payload), int64(len(payload)))
	if err != nil {
		t.Fatalf("Upload() = %v", err)
	}
	if record.OwnerID != 7 || record.Driver != "local" || record.Size != int64(len(payload)) || record.Checksum == "" {
		t.Fatalf("record = %+v", record)
	}
	if record.MIMEType != "text/plain; charset=utf-8" {
		t.Fatalf("MIMEType = %q", record.MIMEType)
	}
	if got := readBack(t, up, record.StorageKey); !bytes.Equal(got, []byte(payload)) {
		t.Fatalf("read back = %q, want %q", got, payload)
	}
}

func TestUploadRejectsAnonymousAndDisallowedType(t *testing.T) {
	t.Parallel()

	repo := newFakeRepo()
	svc, _ := newTestService(t, repo, "local")
	if _, err := svc.Upload(t.Context(), 0, "a.txt", strings.NewReader("x"), 1); !errors.Is(err, ErrForbidden) {
		t.Fatalf("anonymous Upload() = %v, want ErrForbidden", err)
	}
	if _, err := svc.Upload(t.Context(), 7, "a.exe", strings.NewReader("x"), 1); !errors.Is(err, gouploader.ErrFileTypeNotAllow) {
		t.Fatalf("disallowed Upload() = %v, want ErrFileTypeNotAllow", err)
	}
	if repo.len() != 0 {
		t.Fatalf("rejected uploads left %d records", repo.len())
	}
}

func TestChunkFlowResumeAndMergeIdempotent(t *testing.T) {
	t.Parallel()

	repo := newFakeRepo()
	svc, up := newTestService(t, repo, "local")
	const payload = "hello upload"
	sum := sha256.Sum256([]byte(payload))
	fileHash := hex.EncodeToString(sum[:])

	init, err := svc.ChunkInit(t.Context(), 7, &gouploader.ChunkInitRequest{
		FileName: "a.txt", FileSize: int64(len(payload)), ChunkSize: 5, FileHash: fileHash,
	})
	if err != nil {
		t.Fatalf("ChunkInit() = %v", err)
	}
	for i := range 2 {
		part := payload[i*5 : (i+1)*5]
		if _, err := svc.UploadChunk(t.Context(), 7, init.UploadID, i, strings.NewReader(part), int64(len(part)), ""); err != nil {
			t.Fatalf("UploadChunk(%d) = %v", i, err)
		}
	}
	resume, err := svc.Resume(t.Context(), 7, &gouploader.ResumeRequest{FileHash: fileHash})
	if err != nil {
		t.Fatalf("Resume() = %v", err)
	}
	if resume.UploadID != init.UploadID || len(resume.MissingParts) != 1 {
		t.Fatalf("Resume() = %+v", resume)
	}
	if _, err := svc.UploadChunk(t.Context(), 7, init.UploadID, 2, strings.NewReader(payload[10:]), 2, ""); err != nil {
		t.Fatalf("UploadChunk(2) = %v", err)
	}
	record, err := svc.Merge(t.Context(), 7, init.UploadID)
	if err != nil {
		t.Fatalf("Merge() = %v", err)
	}
	if got := readBack(t, up, record.StorageKey); !bytes.Equal(got, []byte(payload)) {
		t.Fatalf("merged = %q, want %q", got, payload)
	}
	again, err := svc.Merge(t.Context(), 7, init.UploadID)
	if err != nil {
		t.Fatalf("repeat Merge() = %v", err)
	}
	if again.ID != record.ID || repo.len() != 1 {
		t.Fatalf("repeat merge created duplicate: %q vs %q, records=%d", again.ID, record.ID, repo.len())
	}
}

func TestSessionOwnershipEnforced(t *testing.T) {
	t.Parallel()

	svc, _ := newTestService(t, newFakeRepo(), "local")
	init, err := svc.ChunkInit(t.Context(), 7, &gouploader.ChunkInitRequest{FileName: "a.txt", FileSize: 12, ChunkSize: 5})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.UploadChunk(t.Context(), 8, init.UploadID, 0, strings.NewReader("hello"), 5, ""); !errors.Is(err, gouploader.ErrUnauthorized) {
		t.Fatalf("cross-owner UploadChunk() = %v, want ErrUnauthorized", err)
	}
	if _, err := svc.Status(t.Context(), 8, init.UploadID); !errors.Is(err, gouploader.ErrUnauthorized) {
		t.Fatalf("cross-owner Status() = %v, want ErrUnauthorized", err)
	}
	if _, err := svc.Merge(t.Context(), 8, init.UploadID); !errors.Is(err, gouploader.ErrUnauthorized) {
		t.Fatalf("cross-owner Merge() = %v, want ErrUnauthorized", err)
	}
}

func TestFileOwnershipEnforced(t *testing.T) {
	t.Parallel()

	repo := newFakeRepo()
	svc, _ := newTestService(t, repo, "local")
	record, err := svc.Upload(t.Context(), 7, "a.txt", strings.NewReader("secret"), 6)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Get(t.Context(), 8, record.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner Get() = %v, want ErrNotFound", err)
	}
	if _, _, err := svc.Download(t.Context(), 8, record.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner Download() = %v, want ErrNotFound", err)
	}
	if err := svc.Delete(t.Context(), 8, record.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("cross-owner Delete() = %v, want ErrNotFound", err)
	}
	page, err := svc.List(t.Context(), 8, 1, 20)
	if err != nil || len(page.Items) != 0 {
		t.Fatalf("cross-owner List() = (%d items, %v)", len(page.Items), err)
	}
}

func TestDownloadAndDelete(t *testing.T) {
	t.Parallel()

	repo := newFakeRepo()
	svc, up := newTestService(t, repo, "local")
	const payload = "download me"
	record, err := svc.Upload(t.Context(), 7, "a.txt", strings.NewReader(payload), int64(len(payload)))
	if err != nil {
		t.Fatal(err)
	}
	got, rc, err := svc.Download(t.Context(), 7, record.ID)
	if err != nil {
		t.Fatalf("Download() = %v", err)
	}
	data, err := io.ReadAll(rc)
	if err := rc.Close(); err != nil {
		t.Errorf("rc.Close(): %v", err)
	}
	if err != nil || !bytes.Equal(data, []byte(payload)) || got.ID != record.ID {
		t.Fatalf("Download() = (%q, %v)", data, err)
	}
	if err := svc.Delete(t.Context(), 7, record.ID); err != nil {
		t.Fatalf("Delete() = %v", err)
	}
	if exists, err := up.Storage().Exists(t.Context(), record.StorageKey); err != nil || exists {
		t.Fatalf("object still exists after delete: (%v, %v)", exists, err)
	}
	if _, err := svc.Get(t.Context(), 7, record.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Get after delete = %v, want ErrNotFound", err)
	}
}

func TestDriverMismatchRejected(t *testing.T) {
	t.Parallel()

	repo := newFakeRepo()
	svc, up := newTestService(t, repo, "local")
	record, err := svc.Upload(t.Context(), 7, "a.txt", strings.NewReader("x"), 1)
	if err != nil {
		t.Fatal(err)
	}
	// 同一仓储换成另一驱动标签的部署：历史记录不得跨驱动读写。
	switched, err := New(up, repo, "aliyun", "other-bucket")
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := switched.Download(t.Context(), 7, record.ID); !errors.Is(err, ErrDriverMismatch) {
		t.Fatalf("Download() = %v, want ErrDriverMismatch", err)
	}
	if err := switched.Delete(t.Context(), 7, record.ID); !errors.Is(err, ErrDriverMismatch) {
		t.Fatalf("Delete() = %v, want ErrDriverMismatch", err)
	}
}

func TestListPagination(t *testing.T) {
	t.Parallel()

	svc, _ := newTestService(t, newFakeRepo(), "local")
	for i := range 3 {
		if _, err := svc.Upload(t.Context(), 7, "a.txt", strings.NewReader("x"), 1); err != nil {
			t.Fatalf("Upload(%d) = %v", i, err)
		}
	}
	page, err := svc.List(t.Context(), 7, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 2 || page.Paging.TotalCount != 3 || page.Paging.TotalPage != 2 {
		t.Fatalf("List() = %+v", page.Paging)
	}
	if _, err := svc.List(t.Context(), 7, 1, 101); !errors.Is(err, ErrInvalid) {
		t.Fatalf("oversized List() = %v, want ErrInvalid", err)
	}
}

func (m *fakeRepo) ListAfter(_ context.Context, owner int64, at time.Time, id string, size int) ([]Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var rows []Record
	for _, r := range m.byID {
		if r.OwnerID == owner && (id == "" || r.CreatedAt.Before(at) || (r.CreatedAt.Equal(at) && r.ID < id)) {
			rows = append(rows, r)
		}
	}
	slices.SortFunc(rows, func(a, b Record) int { return cmp.Or(b.CreatedAt.Compare(a.CreatedAt), cmp.Compare(b.ID, a.ID)) })
	return rows[:min(len(rows), size+1)], nil
}
