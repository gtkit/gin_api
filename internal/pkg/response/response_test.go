package resp

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gtkit/goerr"
)

func TestNewResponseWithoutDataUsesEmptyArray(t *testing.T) {
	result := newResponse(0, "OK", nil)

	body, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	if !strings.Contains(string(body), "\"data\":[]") {
		t.Fatalf("expected empty response data to marshal as [], got %s", string(body))
	}
}

func TestErrUsesSharedEnvelopeForUnexpectedError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/", nil)

	Err(context, errors.New("boom"))

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
	if strings.Contains(recorder.Body.String(), "boom") {
		t.Fatalf("internal server response should not leak error detail, got %s", recorder.Body.String())
	}
}

func TestErrSanitizesServerErrorMessage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/", nil)

	Err(context, goerr.New(errors.New("sql: connection refused"), goerr.StatusMysqlServer()))

	if recorder.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
	body := recorder.Body.String()
	if strings.Contains(body, "connection refused") {
		t.Fatalf("server error response should not leak raw detail, got %s", body)
	}
}

func TestErrUsesExplicitClientMessage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/", nil)

	Err(context, goerr.New(errors.New("name is required"), goerr.StatusParams(), "name is required"))

	body := recorder.Body.String()
	if !strings.Contains(body, "name is required") {
		t.Fatalf("client error response should preserve detail, got %s", body)
	}
}

func TestBindErrReturnsHTTP422(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/", nil)

	BindErr(context, errors.New("field age is required"))

	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnprocessableEntity)
	}
	if body := recorder.Body.String(); !strings.Contains(body, "field age is required") {
		t.Fatalf("expected binding detail to be preserved, got %s", body)
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		name  string
		bytes int64
		want  string
	}{
		{name: "zero", bytes: 0, want: "0 B"},
		{name: "below unit", bytes: 512, want: "512 B"},
		{name: "exactly 1KB", bytes: 1024, want: "1.0 KB"},
		{name: "1.5KB", bytes: 1536, want: "1.5 KB"},
		{name: "1MB", bytes: 1 << 20, want: "1.0 MB"},
		{name: "10MB", bytes: 10 << 20, want: "10.0 MB"},
		{name: "1GB", bytes: 1 << 30, want: "1.0 GB"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := FormatBytes(tt.bytes); got != tt.want {
				t.Errorf("FormatBytes(%d) = %q, want %q", tt.bytes, got, tt.want)
			}
		})
	}
}

func TestBindErrsMapMaxBytesErrorTo413(t *testing.T) {
	gin.SetMode(gin.TestMode)

	maxBytesErr := &http.MaxBytesError{Limit: 4}
	tests := []struct {
		name     string
		bindFunc func(*gin.Context, error)
		err      error
		wantCode int
		wantBody string
	}{
		{
			name:     "BindErr maps max bytes to 413",
			bindFunc: BindErr,
			err:      maxBytesErr,
			wantCode: http.StatusRequestEntityTooLarge,
			wantBody: "\"max_bytes\":4",
		},
		{
			name:     "BindStructErr maps max bytes to 413",
			bindFunc: BindStructErr,
			err:      maxBytesErr,
			wantCode: http.StatusRequestEntityTooLarge,
			wantBody: "\"code\":10010019",
		},
		{
			name:     "BindStructErr maps wrapped max bytes to 413",
			bindFunc: BindStructErr,
			err:      fmt.Errorf("bind: %w", maxBytesErr),
			wantCode: http.StatusRequestEntityTooLarge,
			wantBody: "\"code\":10010019",
		},
		{
			name:     "BindStructErr keeps 422 for plain errors",
			bindFunc: BindStructErr,
			err:      errors.New("field age is required"),
			wantCode: http.StatusUnprocessableEntity,
			wantBody: "field age is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			ctx, _ := gin.CreateTestContext(recorder)
			ctx.Request = httptest.NewRequest(http.MethodPost, "/", nil)

			tt.bindFunc(ctx, tt.err)

			if recorder.Code != tt.wantCode {
				t.Fatalf("status = %d, want %d", recorder.Code, tt.wantCode)
			}
			if body := recorder.Body.String(); !strings.Contains(body, tt.wantBody) {
				t.Fatalf("body = %s, want containing %q", body, tt.wantBody)
			}
		})
	}
}

func TestBusinessErrorReturnsHTTP200(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/", nil)

	sentinel := errors.New("not found")
	mapper := NewErrMapper(
		ErrMapping{Target: sentinel, Status: goerr.NewStatus(44101, http.StatusOK, "文章不存在")},
	)

	MapErr(ctx, sentinel, mapper)

	if recorder.Code != http.StatusOK {
		t.Fatalf("business error status = %d, want %d", recorder.Code, http.StatusOK)
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "\"code\":44101") {
		t.Fatalf("expected business code 44101, got %s", body)
	}
}

func TestErrPreservesCustomGoerrCode(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodGet, "/", nil)

	sentinel := errors.New("article not found")
	status := goerr.NewStatus(13001, http.StatusOK, "文章不存在")

	Err(context, goerr.New(sentinel, status))

	body := recorder.Body.String()
	if !strings.Contains(body, "\"code\":13001") {
		t.Fatalf("expected mapped business code to be preserved, got %s", body)
	}
	if !strings.Contains(body, "文章不存在") {
		t.Fatalf("expected mapped client message to be preserved, got %s", body)
	}
}

func TestMapErrUsesBusinessCodeForMappedError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/", nil)

	sentinel := errors.New("not found")
	mapper := NewErrMapper(
		ErrMapping{Target: sentinel, Status: goerr.NewStatus(13001, http.StatusOK, "文章不存在")},
	)

	MapErr(ctx, sentinel, mapper)

	body := recorder.Body.String()
	if !strings.Contains(body, "\"code\":13001") {
		t.Fatalf("expected business code 13001, got %s", body)
	}
	if !strings.Contains(body, "文章不存在") {
		t.Fatalf("expected business message, got %s", body)
	}
}

func TestMapErrFallsBackToInternalServerForUnmappedError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/", nil)

	sentinel := errors.New("known")
	mapper := NewErrMapper(
		ErrMapping{Target: sentinel, Status: goerr.NewStatus(13001, http.StatusOK, "已知错误")},
	)

	MapErr(ctx, errors.New("unknown db error"), mapper)

	body := recorder.Body.String()
	if strings.Contains(body, "13001") {
		t.Fatalf("unmapped error should not use business code, got %s", body)
	}
	if strings.Contains(body, "unknown db error") {
		t.Fatalf("should not leak internal error detail, got %s", body)
	}
}
