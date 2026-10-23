package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestAccessLogQueriesFinalStatusAndPreservesResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name   string
		status int
		level  string
	}{
		{"info", http.StatusOK, "INFO"}, {"warn", http.StatusBadRequest, "WARN"}, {"error", http.StatusInternalServerError, "ERROR"},
	} {
		for _, enabled := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{false: "/filtered", true: "/enabled"}[enabled], func(t *testing.T) {
				access, read := newNativeTestLogger(t, "info")
				engine := gin.New()
				engine.Use(GinLogger(access, AccessLogOptions{LogResponseBody: true}))
				engine.POST("/probe", func(c *gin.Context) {
					level := "fatal"
					if enabled {
						level = "info"
					}
					if err := access.SetLevel(level); err != nil {
						t.Error(err)
					}
					c.Data(tc.status, "application/json", []byte(`{"token":"private-token","name":"worker"}`))
				})
				recorder := httptest.NewRecorder()
				engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/probe?token=private-query", nil))
				if recorder.Code != tc.status || !strings.Contains(recorder.Body.String(), "private-token") {
					t.Fatal("logging changed downstream response")
				}
				entries := read()
				if len(entries) != map[bool]int{false: 0, true: 1}[enabled] {
					t.Fatalf("entries=%v", entries)
				}
				if enabled {
					if entries[0]["level"] != tc.level || entries[0]["status"] != float64(tc.status) {
						t.Fatalf("entry=%v", entries[0])
					}
					for key, field := range entries[0] {
						if value, ok := field.(string); ok && strings.Contains(value, "private-") {
							t.Fatalf("sensitive field %s", key)
						}
					}
				}
			})
		}
	}
}

func TestGinLoggerRejectsMissingAccessInstance(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("missing access logger accepted")
		}
	}()
	GinLogger(nil, AccessLogOptions{})
}
