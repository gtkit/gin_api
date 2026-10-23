package middleware

import (
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	resp "github.com/gtkit/gin-api/internal/pkg/response"
)

type failingBodyCloser struct {
	cause error
	calls int
}

func (c *failingBodyCloser) Read([]byte) (int, error) { return 0, io.EOF }
func (c *failingBodyCloser) Close() error             { c.calls++; return c.cause }

func TestMaxBytesCloseClosesUnderlyingBodyOnce(t *testing.T) {
	closeErr := errors.New("underlying close failed")
	body := &failingBodyCloser{cause: closeErr}
	engine := newMaxBytesTestEngine(1024, AccessLogOptions{}, func(c *gin.Context) {
		if err := c.Request.Body.Close(); !errors.Is(err, closeErr) {
			t.Errorf("lost close error: %v", err)
		}
		c.Status(http.StatusNoContent)
	})
	req := httptest.NewRequest(http.MethodPost, "/upload", nil)
	req.Body, req.ContentLength = body, -1
	engine.ServeHTTP(httptest.NewRecorder(), req)
	if body.calls != 1 {
		t.Fatalf("underlying body closed %d times, want 1", body.calls)
	}
}

// newMaxBytesTestEngine 组装与生产一致的相关中间件链（ErrorHandler → MaxBytesLimit → GinLogger）。
func newMaxBytesTestEngine(maxBytes int64, accessLog AccessLogOptions, handler gin.HandlerFunc) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(ErrorHandler())
	engine.Use(MaxBytesLimit(MaxBytesLimitConfig{MaxBytes: maxBytes, ErrorMessage: "请求体过大"}))
	engine.Use(GinLogger(accessLog))
	engine.POST("/upload", handler)
	return engine
}

func TestMaxBytesLimitRejectsDeclaredLengthBeforeHandler(t *testing.T) {
	handlerCalled := false
	engine := newMaxBytesTestEngine(4, AccessLogOptions{}, func(c *gin.Context) {
		handlerCalled = true
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader("12345"))
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusRequestEntityTooLarge)
	}
	if handlerCalled {
		t.Fatal("handler executed, want rejected before handler")
	}

	body := recorder.Body.String()
	if !strings.Contains(body, "\"code\":10010019") {
		t.Fatalf("expected max body error code, got %s", body)
	}
	if !strings.Contains(body, "\"msg\":\"文件太大，最大支持 4 B\"") {
		t.Fatalf("expected max body message, got %s", body)
	}
	if !strings.Contains(body, "\"max_bytes\":4") {
		t.Fatalf("expected limit metadata in response data, got %s", body)
	}
}

func TestMaxBytesLimitReturns413ForChunkedBodyViaBind(t *testing.T) {
	engine := newMaxBytesTestEngine(4, AccessLogOptions{}, func(c *gin.Context) {
		var payload struct {
			Name string `json:"name"`
		}
		if err := c.ShouldBindJSON(&payload); err != nil {
			resp.BindStructErr(c, err)
			return
		}
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader(`{"name":"exceeds-limit"}`))
	// 模拟 chunked 传输：无声明长度，前置预检不命中，读取时由 MaxBytesReader 兜底。
	req.ContentLength = -1
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusRequestEntityTooLarge)
	}
	if body := recorder.Body.String(); !strings.Contains(body, "\"code\":10010019") {
		t.Fatalf("expected max body error code, got %s", body)
	}
}

// TestMaxBytesLimitReturns413WhenRequestBodyLoggingEnabled 验证开启请求体日志时
// 字节上限依然生效。
//
// handler **必须真的读 body**：日志中间件不再主动预读，而是在 handler 读取时
// 旁路抄走前缀。这正是要的语义——不读 body 的 handler 不该因为日志被拒。
//
// 旧版本测的是"日志预读撞限"，而那次预读发生在字节上限接管之前，读的是
// 未受约束的流；换句话说，它测的是一条本身就有缺陷的路径。
func TestMaxBytesLimitReturns413WhenRequestBodyLoggingEnabled(t *testing.T) {
	handlerCalled := false
	engine := newMaxBytesTestEngine(4, AccessLogOptions{LogRequestBody: true}, func(c *gin.Context) {
		handlerCalled = true
		if _, err := io.ReadAll(c.Request.Body); err != nil {
			// 与生产 handler 同一口径：绑定路径识别 MaxBytesError 并映射 413。
			resp.BindStructErr(c, err)
			return
		}
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader("12345678"))
	req.ContentLength = -1 // 绕过声明长度预检，让 GinLogger 的 ReadAll 撞限
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusRequestEntityTooLarge)
	}
	if !handlerCalled {
		t.Fatal("handler was skipped: the limit must surface when the handler reads, not before")
	}
}

func TestMaxBytesLimitAllowsBodyWithinLimit(t *testing.T) {
	engine := newMaxBytesTestEngine(1024, AccessLogOptions{}, func(c *gin.Context) {
		var payload struct {
			Name string `json:"name"`
		}
		if err := c.ShouldBindJSON(&payload); err != nil {
			resp.BindStructErr(c, err)
			return
		}
		c.JSON(http.StatusOK, gin.H{"name": payload.Name})
	})

	req := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader(`{"name":"ok"}`))
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
}

func TestMaxBytesLimitKeeps422ForInvalidJSONWithinLimit(t *testing.T) {
	engine := newMaxBytesTestEngine(1024, AccessLogOptions{}, func(c *gin.Context) {
		var payload struct {
			Name string `json:"name"`
		}
		if err := c.ShouldBindJSON(&payload); err != nil {
			resp.BindStructErr(c, err)
			return
		}
		c.Status(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodPost, "/upload", strings.NewReader(`{invalid`))
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusUnprocessableEntity {
		t.Fatalf("status = %d, want %d", recorder.Code, http.StatusUnprocessableEntity)
	}
}
