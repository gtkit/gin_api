package http

import (
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/gtkit/gin-api/internal/middleware"
	"github.com/gtkit/gin-api/internal/module/example/application"
	"github.com/gtkit/gin-api/internal/pkg/ginc"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type recordStore struct {
	owner  int64
	record application.Record
}

func (s *recordStore) Do(ctx context.Context, fn func(context.Context) error) error { return fn(ctx) }
func (s *recordStore) Find(_ context.Context, owner int64, id string) (application.Record, bool, error) {
	return s.record, owner == s.owner && id == s.record.ID, nil
}
func (s *recordStore) List(_ context.Context, owner int64, _, _ int) ([]application.Record, int64, error) {
	if owner == s.owner && s.record.ID != "" {
		return []application.Record{s.record}, 1, nil
	}
	return nil, 0, nil
}
func (s *recordStore) Create(_ context.Context, owner int64, record application.Record) error {
	s.owner, s.record = owner, record
	return nil
}
func (s *recordStore) Update(_ context.Context, owner int64, record application.Record) (bool, error) {
	if owner != s.owner {
		return false, nil
	}
	s.record = record
	return true, nil
}
func (s *recordStore) Delete(_ context.Context, owner int64, _ string) (bool, error) {
	if owner != s.owner {
		return false, nil
	}
	s.record = application.Record{}
	return true, nil
}

func TestRecordHTTPContract(t *testing.T) {
	store := &recordStore{}
	service, err := application.New(store, store)
	if err != nil {
		t.Fatal(err)
	}
	engine := gin.New()
	group := engine.Group("/api/v1", middleware.ErrorHandler())
	Register(group, Deps{Service: service, Auth: func(c *gin.Context) {
		if c.GetHeader("X-Test-Owner") == "other" {
			ginc.SetUserID(c, 8)
		} else {
			ginc.SetUserID(c, 7)
		}
	}})
	call := func(method, path, body, owner string) (int, map[string]json.RawMessage) {
		t.Helper()
		request := httptest.NewRequest(method, "/api/v1/example"+path, strings.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("X-Test-Owner", owner)
		response := httptest.NewRecorder()
		engine.ServeHTTP(response, request)
		var envelope map[string]json.RawMessage
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatal(err)
		}
		if _, ok := envelope["request_id"]; !ok {
			t.Fatal("missing response envelope")
		}
		return response.Code, envelope
	}
	status, envelope := call(http.MethodPost, "", `{"title":"record"}`, "")
	if status != 200 || string(envelope["code"]) != "0" {
		t.Fatalf("create %d %s", status, envelope)
	}
	var created application.Record
	if err := json.Unmarshal(envelope["data"], &created); err != nil {
		t.Fatal(err)
	}
	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		body := ""
		if method == http.MethodPut {
			body = `{"title":"stolen"}`
		}
		status, _ := call(method, "/"+created.ID, body, "other")
		if status != 404 {
			t.Fatalf("cross-owner %s = %d", method, status)
		}
	}
	for _, input := range []struct {
		method, path, body string
		status             int
	}{
		{http.MethodPost, "", `{"title":""}`, 422},
		{http.MethodPost, "", `{"title":"` + strings.Repeat("x", 8300) + `"}`, 413},
		{http.MethodGet, "?page_size=101", "", 422},
		{http.MethodGet, "/invalid-id", "", 422},
	} {
		status, _ := call(input.method, input.path, input.body, "")
		if status != input.status {
			t.Fatalf("%s %s = %d, want %d", input.method, input.path, status, input.status)
		}
	}
	status, _ = call(http.MethodPut, "/"+created.ID, `{"title":"updated"}`, "")
	if status != 200 {
		t.Fatal(status)
	}
	status, _ = call(http.MethodDelete, "/"+created.ID, "", "")
	if status != 200 {
		t.Fatal(status)
	}
}
