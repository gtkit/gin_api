package middleware

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	resp "github.com/gtkit/gin-api/internal/pkg/response"
)

// TestRequestBodyLoggingKeepsFullBodyForHandler 验证前缀预读拼回后，
// 开启请求体日志时 handler 仍能读到 >4KB 的完整请求体。
func TestRequestBodyLoggingKeepsFullBodyForHandler(t *testing.T) {
	largeValue := strings.Repeat("a", 8*1024)
	payload := fmt.Sprintf(`{"name":%q}`, largeValue)

	engine := newMaxBytesTestEngine(t, 1<<20, AccessLogOptions{LogRequestBody: true}, func(c *gin.Context) {
		var req struct {
			Name string `json:"name"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			resp.BindStructErr(c, err)
			return
		}
		if len(req.Name) != len(largeValue) {
			t.Fatalf("handler got name len = %d, want %d (body truncated?)", len(req.Name), len(largeValue))
		}
		c.Status(http.StatusNoContent)
	})

	req := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusNoContent {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusNoContent)
	}
}

// TestRedactBodyFallsBackToPlaceholderForTruncatedJSON 锁定脱敏安全性：
// 超过捕获上限被截断的 JSON 前缀解析失败时，只输出占位符，不回传原始字节。
func TestRedactBodyFallsBackToPlaceholderForTruncatedJSON(t *testing.T) {
	largeValue := strings.Repeat("b", 8*1024)
	full := fmt.Appendf(nil, `{"phone":%q}`, largeValue)
	prefix := full[:bodyLogCaptureLimit]

	got := redactBody("application/json", prefix)
	if strings.Contains(got, "bbbb") {
		t.Fatalf("redactBody leaked raw truncated content: %q", got[:80])
	}
}

// TestResponseBodyWriterCapturesBoundedPrefix 验证响应捕获有界：
// 客户端字节流完整，捕获缓冲不超过 bodyLogCaptureLimit。
func TestResponseBodyWriterCapturesBoundedPrefix(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)

	w := &responseBodyWriter{body: &bytes.Buffer{}, ResponseWriter: ctx.Writer}

	chunk := bytes.Repeat([]byte("x"), 2048)
	total := 0
	for range 8 { // 8 × 2KB = 16KB
		n, err := w.Write(chunk)
		if err != nil {
			t.Fatalf("Write returned error: %v", err)
		}
		total += n
	}

	if total != 16*1024 {
		t.Fatalf("written = %d, want %d (client stream must be complete)", total, 16*1024)
	}
	if recorder.Body.Len() != 16*1024 {
		t.Fatalf("client received = %d bytes, want %d", recorder.Body.Len(), 16*1024)
	}
	if w.body.Len() != bodyLogCaptureLimit {
		t.Fatalf("capture buffer = %d bytes, want capped at %d", w.body.Len(), bodyLogCaptureLimit)
	}
	if got := truncate(w.body.String(), maxBodyLogSize); !strings.HasSuffix(got, "...(truncated)") {
		t.Fatalf("expected truncated marker on capped capture, got tail %q", got[len(got)-20:])
	}
}

// TestResponseBodyWriterSmallResponseUnchanged 验证 4KB 内响应捕获行为与全量时代一致。
func TestResponseBodyWriterSmallResponseUnchanged(t *testing.T) {
	gin.SetMode(gin.TestMode)
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)

	w := &responseBodyWriter{body: &bytes.Buffer{}, ResponseWriter: ctx.Writer}
	payload := `{"ok":true}`
	if _, err := w.Write([]byte(payload)); err != nil {
		t.Fatalf("Write returned error: %v", err)
	}

	if w.body.String() != payload {
		t.Fatalf("capture = %q, want %q", w.body.String(), payload)
	}
	if recorder.Body.String() != payload {
		t.Fatalf("client = %q, want %q", recorder.Body.String(), payload)
	}
}
