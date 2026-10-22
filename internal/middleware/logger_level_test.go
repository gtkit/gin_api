package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	applog "github.com/gtkit/gin-api/internal/pkg/log"
)

type accessLevelCapture struct {
	handled bool
	level   applog.Level
	stream  applog.Stream
	queried int
	entries []applog.LogEntry
	enabled bool
}

func (s *accessLevelCapture) Enabled(level applog.Level, stream applog.Stream) bool {
	s.level, s.stream = level, stream
	s.queried++
	return s.handled && s.enabled
}

func (s *accessLevelCapture) Log(_ context.Context, entry applog.LogEntry) {
	s.entries = append(s.entries, entry)
}

func TestAccessLogQueriesFinalStatusAndPreservesResponse(t *testing.T) {
	gin.SetMode(gin.TestMode)
	for _, tc := range []struct {
		name   string
		status int
		level  applog.Level
	}{
		{"info", http.StatusOK, applog.LevelInfo},
		{"warn", http.StatusBadRequest, applog.LevelWarn},
		{"error", http.StatusInternalServerError, applog.LevelError},
	} {
		for _, enabled := range []bool{false, true} {
			t.Run(tc.name+map[bool]string{false: "/filtered", true: "/enabled"}[enabled], func(t *testing.T) {
				sink := &accessLevelCapture{enabled: enabled}
				release, err := applog.Install(sink, nil)
				if err != nil {
					t.Fatal(err)
				}
				defer release()
				engine := gin.New()
				engine.Use(GinLogger(AccessLogOptions{LogResponseBody: true}))
				engine.POST("/probe", func(c *gin.Context) {
					sink.handled = true
					c.Data(tc.status, "application/json", []byte(`{"token":"private-token","name":"worker"}`))
				})
				recorder := httptest.NewRecorder()
				engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/probe?token=private-query", nil))
				if !sink.handled || recorder.Code != tc.status || !strings.Contains(recorder.Body.String(), "private-token") {
					t.Fatal("logging changed downstream response")
				}
				if sink.queried == 0 || sink.level != tc.level || sink.stream != applog.AccessStream || len(sink.entries) != map[bool]int{false: 0, true: 1}[enabled] {
					t.Fatalf("queries=%d level=%v stream=%v entries=%d", sink.queried, sink.level, sink.stream, len(sink.entries))
				}
				if enabled {
					for _, field := range sink.entries[0].Fields {
						if value, ok := field.Value.(string); ok && strings.Contains(value, "private-") {
							t.Fatalf("sensitive value in access field %s", field.Key)
						}
					}
				}
			})
		}
	}
}
