package middleware

import (
	"bytes"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestFileTypeMiddlewareRejectsUnsupportedExtensionWithUnifiedEnvelope(t *testing.T) {
	gin.SetMode(gin.TestMode)

	engine := gin.New()
	engine.Use(FileTypeMiddleware())
	engine.POST("/upload", func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	body, contentType := buildMultipartBody(t, "file", "malware.exe", []byte("hello"))
	req := httptest.NewRequest(http.MethodPost, "/upload", body)
	req.Header.Set("Content-Type", contentType)

	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusBadRequest)
	}
	responseBody := recorder.Body.String()
	if !strings.Contains(responseBody, "\"code\":10010002") {
		t.Fatalf("expected params error code, got %s", responseBody)
	}
	if !strings.Contains(responseBody, "不支持的文件类型: .exe") {
		t.Fatalf("expected unsupported extension message, got %s", responseBody)
	}
	if !strings.Contains(responseBody, "\"allowed\"") {
		t.Fatalf("expected allowed list in response data, got %s", responseBody)
	}
}

func buildMultipartBody(t *testing.T, fieldName, fileName string, content []byte) (*bytes.Buffer, string) {
	t.Helper()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)

	part, err := writer.CreateFormFile(fieldName, fileName)
	if err != nil {
		t.Fatalf("CreateFormFile error = %v", err)
	}
	if _, err := io.Copy(part, bytes.NewReader(content)); err != nil {
		t.Fatalf("copy multipart content error = %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("Close multipart writer error = %v", err)
	}

	return body, writer.FormDataContentType()
}
