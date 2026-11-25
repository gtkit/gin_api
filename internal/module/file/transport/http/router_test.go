package http

import (
	"bytes"
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/gin-api/internal/middleware"
	"github.com/gtkit/gin-api/internal/module/file/application"
	"github.com/gtkit/gin-api/internal/optional/upload"
	"github.com/gtkit/gin-api/internal/pkg/ginc"
	runtimefile "github.com/gtkit/gin-api/internal/runtime/module/file"
	sharedauth "github.com/gtkit/gin-api/internal/shared/auth"
	"github.com/gtkit/gouploader"
)

// memRepo 是内存版归属仓储，复刻唯一键 (owner, storage_key) 幂等语义。
type memRepo struct {
	mu   sync.Mutex
	byID map[string]application.Record
}

func (m *memRepo) Create(_ context.Context, record application.Record) (application.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, existing := range m.byID {
		if existing.OwnerID == record.OwnerID && existing.StorageKey == record.StorageKey {
			return existing, nil
		}
	}
	m.byID[record.ID] = record
	return record, nil
}

func (m *memRepo) Find(_ context.Context, owner int64, id string) (application.Record, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.byID[id]
	if !ok || record.OwnerID != owner {
		return application.Record{}, false, nil
	}
	return record, true, nil
}

func (m *memRepo) List(_ context.Context, owner int64, offset, limit int) ([]application.Record, int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var owned []application.Record
	for _, record := range m.byID {
		if record.OwnerID == owner {
			owned = append(owned, record)
		}
	}
	total := int64(len(owned))
	if offset >= len(owned) {
		return nil, total, nil
	}
	return owned[offset:min(offset+limit, len(owned))], total, nil
}

func (m *memRepo) Delete(_ context.Context, owner int64, id string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	record, ok := m.byID[id]
	if !ok || record.OwnerID != owner {
		return false, nil
	}
	delete(m.byID, id)
	return true, nil
}

func newTestRouter(t *testing.T, allow bool) *gin.Engine {
	return newTestRouterWithHooks(t, allow, nil, nil)
}

func newTestRouterWithHooks(t *testing.T, allow bool, authCalls, authorizeCalls *int, configure ...func(*Deps)) *gin.Engine {
	t.Helper()

	engine := gin.New()
	deps := Deps{
		Service: newTestFileService(t, &memRepo{byID: map[string]application.Record{}}),
		Auth: func(c *gin.Context) {
			if authCalls != nil {
				(*authCalls)++
			}
			owner := int64(7)
			if c.GetHeader("X-Test-Owner") == "other" {
				owner = 8
			}
			ginc.SetUserID(c, owner)
			ginc.SetPrincipal(c, sharedauth.Principal{UserID: owner})
		},
		Authorizer: sharedauth.AuthorizerFunc(func(context.Context, sharedauth.Principal, sharedauth.AuthorizationRequest) (bool, error) {
			if authorizeCalls != nil {
				(*authorizeCalls)++
			}
			return allow, nil
		}),
		Policy:         upload.DefaultPolicy(),
		MaxUploadBytes: 2 << 20,
		MaxChunkBytes:  2 << 20,
		RequestTimeout: 30 * time.Second,
	}
	for _, apply := range configure {
		apply(&deps)
	}
	Register(engine.Group("/api/v1", middleware.ErrorHandler()), deps)
	return engine
}

func newTestFileService(t *testing.T, repo application.Repository) *application.Service {
	t.Helper()
	up, err := gouploader.New(
		gouploader.WithLocalStorage(t.TempDir()),
		gouploader.WithChunkTempDir(t.TempDir()),
		gouploader.WithMaxFileSize(1<<20),
		gouploader.WithAllowedTypes(".txt"),
		gouploader.WithChunkSize(5, 1, 1<<20),
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
	service, err := application.New(runtimefile.NewTransfer(up), repo, "local", "")
	if err != nil {
		t.Fatal(err)
	}
	return service
}

type locationTestStorage struct {
	gouploader.Storage
	reads, deletes int
}

func (s *locationTestStorage) Get(context.Context, string) (io.ReadCloser, error) {
	s.reads++
	return io.NopCloser(strings.NewReader("b")), nil
}

func (s *locationTestStorage) Delete(context.Context, string) error {
	s.deletes++
	return nil
}

func TestFileStorageLocationCheckedBeforeObjectIO(t *testing.T) {
	const id = "019abe00-c4eb-7a20-885f-30d224a39e73"
	for _, tc := range []struct {
		name, driver, recordBucket, configuredBucket string
		otherOwner                                   bool
		status                                       int
	}{
		{name: "same object key in other bucket", driver: "s3", recordBucket: "first", configuredBucket: "second", status: http.StatusConflict},
		{name: "legacy empty s3 bucket", driver: "s3", configuredBucket: "second", status: http.StatusConflict},
		{name: "empty s3 location on both sides", driver: "s3", status: http.StatusConflict},
		{name: "invalid local bucket", driver: "local", recordBucket: "first", configuredBucket: "first", status: http.StatusConflict},
		{name: "foreign owner masks location", driver: "s3", recordBucket: "first", configuredBucket: "second", otherOwner: true, status: http.StatusNotFound},
		{name: "valid s3 location", driver: "s3", recordBucket: "first", configuredBucket: "first", status: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			storage := &locationTestStorage{}
			up, err := gouploader.New(gouploader.WithStorage(storage), gouploader.WithChunkTempDir(t.TempDir()))
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(up.Close)
			repo := &memRepo{byID: map[string]application.Record{
				id: {ID: id, OwnerID: 7, Driver: tc.driver, Bucket: tc.recordBucket, StorageKey: "same.txt", FileName: "same.txt", Size: 1},
			}}
			service, err := application.New(runtimefile.NewTransfer(up), repo, tc.driver, tc.configuredBucket)
			if err != nil {
				t.Fatal(err)
			}
			engine := newTestRouterWithHooks(t, true, nil, nil, func(deps *Deps) { deps.Service = service })
			request := func(method, suffix string) {
				t.Helper()
				req := httptest.NewRequest(method, "/api/v1/files/"+id+suffix, nil)
				if tc.otherOwner {
					req.Header.Set("X-Test-Owner", "other")
				}
				rec := httptest.NewRecorder()
				engine.ServeHTTP(rec, req)
				if rec.Code != tc.status {
					t.Fatalf("%s status=%d want=%d", method, rec.Code, tc.status)
				}
			}
			request(http.MethodGet, "/download")
			request(http.MethodDelete, "")
			_, present, err := repo.Find(t.Context(), 7, id)
			wantCalls := 0
			if tc.status == http.StatusOK {
				wantCalls = 1
			}
			if err != nil || present != (wantCalls == 0) || storage.reads != wantCalls || storage.deletes != wantCalls {
				t.Fatalf("object calls: get=%d delete=%d, record present=%t, err=%v", storage.reads, storage.deletes, present, err)
			}
		})
	}
}

func TestFilePilotRateLimitOrder(t *testing.T) {
	for _, tc := range []struct {
		name, method, path string
		deny               string
		wantStatus         int
		want               []string
	}{
		{"list before auth", http.MethodGet, "/api/v1/files", "ip", 429, []string{"ip"}},
		{"failed auth", http.MethodGet, "/api/v1/files", "auth", 401, []string{"ip", "auth"}},
		{"list user quota", http.MethodGet, "/api/v1/files", "user", 429, []string{"ip", "auth", "user"}},
		{"list complete", http.MethodGet, "/api/v1/files", "", 200, []string{"ip", "auth", "user", "authorize"}},
		{"merge admission", http.MethodPost, "/api/v1/upload/merge/some-id", "admit", 503, []string{"ip", "auth", "user", "authorize", "admit"}},
		{"another merge uses same policy", http.MethodPost, "/api/v1/upload/merge/other-id", "ip", 429, []string{"ip"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			stage := func(name string, status int) gin.HandlerFunc {
				return func(c *gin.Context) {
					calls = append(calls, name)
					if tc.deny == name {
						c.AbortWithStatusJSON(status, gin.H{"code": status})
					}
				}
			}
			engine := newTestRouterWithHooks(t, true, nil, nil, func(deps *Deps) {
				deps.RateLimit = func(route string, ids ...string) ([]gin.HandlerFunc, []gin.HandlerFunc) {
					want := []string{"files.list.ip", "files.list.user"}
					if route == "/upload/merge/:upload_id" {
						want = []string{"upload.merge.ip", "upload.merge.user"}
					}
					if !slices.Equal(ids, want) {
						t.Fatalf("route %q registered policies %v, want %v", route, ids, want)
					}
					return []gin.HandlerFunc{stage("ip", 429)}, []gin.HandlerFunc{stage("user", 429)}
				}
				auth := deps.Auth
				deps.Auth = func(c *gin.Context) {
					stage("auth", 401)(c)
					if !c.IsAborted() {
						auth(c)
					}
				}
				deps.Authorizer = sharedauth.AuthorizerFunc(func(context.Context, sharedauth.Principal, sharedauth.AuthorizationRequest) (bool, error) {
					calls = append(calls, "authorize")
					return true, nil
				})
				deps.MergeAdmission = stage("admit", 503)
			})
			status, _ := doJSON(t, engine, tc.method, tc.path, "", nil)
			if status != tc.wantStatus || !slices.Equal(calls, tc.want) {
				t.Fatalf("status=%d, calls=%v, want %d, %v", status, calls, tc.wantStatus, tc.want)
			}
		})
	}
}

type failingListRepository struct {
	memRepo
	err error
}

func (p *failingListRepository) List(context.Context, int64, int, int) ([]application.Record, int64, error) {
	return nil, 0, p.err
}

func TestFileListDependencyErrors(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		want int
	}{
		{"unavailable", errors.Join(application.ErrDependencyUnavailable, errors.New("private cause")), 503},
		{"deadline", context.DeadlineExceeded, 504},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := newTestFileService(t, &failingListRepository{err: tc.err})
			engine := newTestRouterWithHooks(t, true, nil, nil, func(deps *Deps) { deps.Service = service })
			status, body := doJSON(t, engine, http.MethodGet, "/api/v1/files", "", nil)
			if status != tc.want || body.Code == 0 || strings.Contains(body.Msg, "private cause") {
				t.Fatalf("list returned status=%d envelope=%+v", status, body)
			}
		})
	}
}

type blockingMergeRepository struct {
	memRepo
	started chan struct{}
	release chan struct{}
}

func (p *blockingMergeRepository) Create(ctx context.Context, record application.Record) (application.Record, error) {
	p.started <- struct{}{}
	select {
	case <-p.release:
		return p.memRepo.Create(ctx, record)
	case <-ctx.Done():
		return application.Record{}, ctx.Err()
	}
}

func TestMergeCapacityDoesNotBlockFileList(t *testing.T) {
	const limit = 4
	repo := &blockingMergeRepository{
		memRepo: memRepo{byID: map[string]application.Record{}},
		started: make(chan struct{}, limit+1),
		release: make(chan struct{}),
	}
	service := newTestFileService(t, repo)
	engine := newTestRouterWithHooks(t, true, nil, nil, func(deps *Deps) {
		deps.Service = service
		deps.MergeAdmission = middleware.Admission(limit, nil)
	})

	uploads := make([]string, limit)
	for i := range uploads {
		payload := fmt.Sprintf("row-%d", i)
		status, body := doJSON(t, engine, http.MethodPost, "/api/v1/upload/init",
			fmt.Sprintf(`{"file_name":"file-%d.txt","file_size":5,"chunk_size":5}`, i), nil)
		var session struct {
			UploadID string `json:"upload_id"`
		}
		if err := json.Unmarshal(body.Data, &session); status != http.StatusOK || err != nil || session.UploadID == "" {
			t.Fatalf("init status=%d data=%s err=%v", status, body.Data, err)
		}
		uploads[i] = session.UploadID
		status, _ = doMultipart(t, engine, "/api/v1/upload/chunk", "part", []byte(payload),
			map[string]string{"upload_id": session.UploadID, "chunk_index": "0"}, nil)
		if status != http.StatusOK {
			t.Fatalf("chunk status=%d", status)
		}
	}

	var merging sync.WaitGroup
	defer func() { close(repo.release); merging.Wait() }()
	for _, id := range uploads {
		merging.Go(func() {
			rec := httptest.NewRecorder()
			req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/upload/merge/"+id, nil)
			engine.ServeHTTP(rec, req)
			if rec.Code != http.StatusOK {
				t.Errorf("accepted merge status=%d body=%s", rec.Code, rec.Body.String())
			}
		})
	}
	timeout := time.NewTimer(3 * time.Second)
	defer timeout.Stop()
	for range limit {
		select {
		case <-repo.started:
		case <-timeout.C:
			t.Fatal("all four merge requests did not reach persistence")
		}
	}
	if status, _ := doJSON(t, engine, http.MethodPost, "/api/v1/upload/merge/another-session", "", nil); status != http.StatusServiceUnavailable {
		t.Fatalf("merge with full quota status=%d", status)
	}
	if status, _ := doJSON(t, engine, http.MethodGet, "/api/v1/files", "", nil); status != http.StatusOK {
		t.Fatalf("list with full merge quota status=%d", status)
	}
}

func TestUploadRejectsWrongContentTypeBeforeAuthentication(t *testing.T) {
	authCalls, authorizeCalls := 0, 0
	engine := newTestRouterWithHooks(t, true, &authCalls, &authorizeCalls)
	status, _ := doJSON(t, engine, http.MethodPost, "/api/v1/upload", `{}`, nil)
	if status != http.StatusUnsupportedMediaType || authCalls != 0 || authorizeCalls != 0 {
		t.Fatalf("wrong content type status=%d auth=%d authorize=%d", status, authCalls, authorizeCalls)
	}
}

type envelope struct {
	Code int             `json:"code"`
	Msg  string          `json:"msg"`
	Data json.RawMessage `json:"data"`
}

func doJSON(t *testing.T, engine *gin.Engine, method, path, body string, headers map[string]string) (int, envelope) {
	t.Helper()

	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		request.Header.Set(k, v)
	}
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	var env envelope
	if err := json.Unmarshal(response.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v (body=%s)", err, response.Body.String())
	}
	return response.Code, env
}

func doMultipart(t *testing.T, engine *gin.Engine, path, fileName string, content []byte, fields map[string]string, headers map[string]string) (int, envelope) {
	t.Helper()

	var buf bytes.Buffer
	writer := multipart.NewWriter(&buf)
	for k, v := range fields {
		if err := writer.WriteField(k, v); err != nil {
			t.Fatal(err)
		}
	}
	part, err := writer.CreateFormFile("file", fileName)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(content); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, path, &buf)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	for k, v := range headers {
		request.Header.Set(k, v)
	}
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	var env envelope
	if err := json.Unmarshal(response.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode envelope: %v (body=%s)", err, response.Body.String())
	}
	return response.Code, env
}

func TestUploadDownloadHTTPContract(t *testing.T) {
	t.Parallel()

	engine := newTestRouter(t, true)
	const payload = "hello upload"
	status, env := doMultipart(t, engine, "/api/v1/upload", "a.txt", []byte(payload), nil, nil)
	if status != http.StatusOK || env.Code != 0 {
		t.Fatalf("upload: status=%d env=%+v", status, env)
	}
	var record struct {
		ID       string `json:"id"`
		FileName string `json:"file_name"`
		Size     int64  `json:"size"`
	}
	if err := json.Unmarshal(env.Data, &record); err != nil || record.ID == "" || record.Size != int64(len(payload)) {
		t.Fatalf("upload data = %s (%v)", env.Data, err)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/files/"+record.ID+"/download", nil)
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK || !bytes.Equal(body, []byte(payload)) {
		t.Fatalf("download: status=%d body=%q", response.Code, body)
	}
	if response.Header().Get("X-Content-Type-Options") != "nosniff" ||
		!strings.HasPrefix(response.Header().Get("Content-Disposition"), "attachment") {
		t.Fatalf("download headers = %v", response.Header())
	}

	status, env = doJSON(t, engine, http.MethodGet, "/api/v1/files?page=1&page_size=10", "", nil)
	if status != http.StatusOK || !strings.Contains(string(env.Data), record.ID) {
		t.Fatalf("list: status=%d data=%s", status, env.Data)
	}
}

func TestChunkProtocolHTTPContract(t *testing.T) {
	t.Parallel()

	engine := newTestRouter(t, true)
	const payload = "hello upload"
	sum := sha256.Sum256([]byte(payload))
	fileHash := hex.EncodeToString(sum[:])

	status, env := doJSON(t, engine, http.MethodPost, "/api/v1/upload/init",
		`{"file_name":"a.txt","file_size":12,"chunk_size":5,"file_hash":"`+fileHash+`"}`, nil)
	if status != http.StatusOK {
		t.Fatalf("init: status=%d env=%+v", status, env)
	}
	var init struct {
		UploadID    string `json:"upload_id"`
		TotalChunks int    `json:"total_chunks"`
	}
	if err := json.Unmarshal(env.Data, &init); err != nil || init.UploadID == "" || init.TotalChunks != 3 {
		t.Fatalf("init data = %s (%v)", env.Data, err)
	}

	for i := range 2 {
		part := payload[i*5 : (i+1)*5]
		status, env = doMultipart(t, engine, "/api/v1/upload/chunk", "chunk", []byte(part),
			map[string]string{"upload_id": init.UploadID, "chunk_index": string(rune('0' + i))}, nil)
		if status != http.StatusOK {
			t.Fatalf("chunk %d: status=%d env=%+v", i, status, env)
		}
	}

	status, env = doJSON(t, engine, http.MethodPost, "/api/v1/upload/resume", `{"file_hash":"`+fileHash+`"}`, nil)
	if status != http.StatusOK || !strings.Contains(string(env.Data), init.UploadID) {
		t.Fatalf("resume: status=%d data=%s", status, env.Data)
	}

	status, env = doMultipart(t, engine, "/api/v1/upload/chunk", "chunk", []byte(payload[10:]),
		map[string]string{"upload_id": init.UploadID, "chunk_index": "2"}, nil)
	if status != http.StatusOK {
		t.Fatalf("chunk 2: status=%d env=%+v", status, env)
	}

	status, env = doJSON(t, engine, http.MethodPost, "/api/v1/upload/merge/"+init.UploadID, "", nil)
	if status != http.StatusOK {
		t.Fatalf("merge: status=%d env=%+v", status, env)
	}
	var record struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(env.Data, &record); err != nil || record.ID == "" {
		t.Fatalf("merge data = %s (%v)", env.Data, err)
	}

	request := httptest.NewRequest(http.MethodGet, "/api/v1/files/"+record.ID+"/download", nil)
	response := httptest.NewRecorder()
	engine.ServeHTTP(response, request)
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(body, []byte(payload)) {
		t.Fatalf("merged download = %q", body)
	}
}

func TestCrossOwnerRejected(t *testing.T) {
	t.Parallel()

	engine := newTestRouter(t, true)
	_, env := doMultipart(t, engine, "/api/v1/upload", "a.txt", []byte("secret"), nil, nil)
	var record struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(env.Data, &record); err != nil {
		t.Fatal(err)
	}
	other := map[string]string{"X-Test-Owner": "other"}

	status, _ := doJSON(t, engine, http.MethodGet, "/api/v1/files/"+record.ID, "", other)
	if status != http.StatusNotFound {
		t.Fatalf("cross-owner get: status=%d, want 404", status)
	}

	_, env = doJSON(t, engine, http.MethodPost, "/api/v1/upload/init", `{"file_name":"a.txt","file_size":12,"chunk_size":5}`, nil)
	var init struct {
		UploadID string `json:"upload_id"`
	}
	if err := json.Unmarshal(env.Data, &init); err != nil {
		t.Fatal(err)
	}
	status, _ = doJSON(t, engine, http.MethodPost, "/api/v1/upload/merge/"+init.UploadID, "", other)
	if status != http.StatusForbidden {
		t.Fatalf("cross-owner merge: status=%d, want 403", status)
	}
}

func TestAuthorizerDeniesAndTypeRejected(t *testing.T) {
	t.Parallel()

	denied := newTestRouter(t, false)
	status, _ := doMultipart(t, denied, "/api/v1/upload", "a.txt", []byte("x"), nil, nil)
	if status != http.StatusForbidden {
		t.Fatalf("denied upload: status=%d, want 403", status)
	}

	engine := newTestRouter(t, true)
	status, env := doMultipart(t, engine, "/api/v1/upload", "a.exe", []byte("x"), nil, nil)
	if status == http.StatusOK || env.Code == 0 {
		t.Fatalf("disallowed type accepted: status=%d env=%+v", status, env)
	}
}

func (m *memRepo) ListAfter(_ context.Context, owner int64, at time.Time, id string, size int) ([]application.Record, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var rows []application.Record
	for _, r := range m.byID {
		if r.OwnerID == owner && (id == "" || r.CreatedAt.Before(at) || (r.CreatedAt.Equal(at) && r.ID < id)) {
			rows = append(rows, r)
		}
	}
	slices.SortFunc(rows, func(a, b application.Record) int {
		return cmp.Or(b.CreatedAt.Compare(a.CreatedAt), cmp.Compare(b.ID, a.ID))
	})
	return rows[:min(len(rows), size+1)], nil
}

func TestFileCursorPaging(t *testing.T) {
	when := time.Now()
	repo := &memRepo{byID: map[string]application.Record{}}
	for i, owner := range []int64{7, 8, 7} {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", i+1)
		repo.byID[id] = application.Record{ID: id, OwnerID: owner, CreatedAt: when}
	}
	engine := newTestRouterWithHooks(t, true, nil, nil, func(d *Deps) { d.Service = newTestFileService(t, repo) })
	status, body := doJSON(t, engine, http.MethodGet, "/api/v1/files?cursor=&page_size=1", "", nil)
	var first, last application.CursorPage
	if status != 200 || json.Unmarshal(body.Data, &first) != nil || len(first.Items) != 1 || first.NextCursor == "" || !strings.HasSuffix(first.Items[0].ID, "3") {
		t.Fatalf("first cursor page: %+v", body)
	}
	path := "/api/v1/files?page_size=1&cursor=" + first.NextCursor
	status, body = doJSON(t, engine, http.MethodGet, path, "", nil)
	if status != 200 || json.Unmarshal(body.Data, &last) != nil || len(last.Items) != 1 || last.NextCursor != "" || !strings.HasSuffix(last.Items[0].ID, "1") {
		t.Fatalf("last cursor page: %+v", body)
	}
	status, _ = doJSON(t, engine, http.MethodGet, path, "", map[string]string{"X-Test-Owner": "other"})
	if status != http.StatusUnprocessableEntity {
		t.Fatal("cross-owner cursor accepted")
	}
	for _, path := range []string{"/api/v1/files?cursor=bad", "/api/v1/files?cursor=&page=1", "/api/v1/files?cursor=&cursor="} {
		status, _ = doJSON(t, engine, http.MethodGet, path, "", nil)
		if status != http.StatusUnprocessableEntity {
			t.Fatalf("ambiguous cursor query returned %d", status)
		}
	}
}
