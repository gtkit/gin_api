package http

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/gin-api/internal/middleware"
	"github.com/gtkit/gin-api/internal/module/file/application"
	"github.com/gtkit/gin-api/internal/optional/upload"
	"github.com/gtkit/gin-api/internal/pkg/ginc"
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

func newTestRouterWithHooks(t *testing.T, allow bool, authCalls, authorizeCalls *int) *gin.Engine {
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
	service, err := application.New(up, &memRepo{byID: map[string]application.Record{}}, "local", "")
	if err != nil {
		t.Fatal(err)
	}
	engine := gin.New()
	Register(engine.Group("/api/v1", middleware.ErrorHandler()), Deps{
		Service: service,
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
	})
	return engine
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
