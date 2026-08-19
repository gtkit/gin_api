package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/gtkit/gin-api/internal/models"
	"github.com/gtkit/gin-api/internal/pkg/ginc"
)

// auditEngine 组一个带路由的引擎：FullPath 只有真实路由匹配才有值，
// CreateTestContext 不走路由树，测不了模板回退。
func auditEngine(capture *[]models.OperationAudit, accept bool) *gin.Engine {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(func(c *gin.Context) { ginc.SetUserID(c, 42) })
	engine.Use(OperationAudit(func(rec models.OperationAudit) bool {
		*capture = append(*capture, rec)
		return accept
	}))
	engine.POST("/api/v1/things/:id", func(c *gin.Context) { c.Status(http.StatusCreated) })
	return engine
}

func TestOperationAuditRecordsCompletedRequest(t *testing.T) {
	var records []models.OperationAudit
	engine := auditEngine(&records, true)

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/things/7?secret=1", nil))

	if len(records) != 1 {
		t.Fatalf("captured %d records, want 1", len(records))
	}
	got := records[0]
	if got.Method != http.MethodPost || got.Route != "/api/v1/things/:id" {
		t.Fatalf("record = %s %s, want POST /api/v1/things/:id (route template, not raw path)", got.Method, got.Route)
	}
	if got.Status != http.StatusCreated {
		t.Fatalf("status = %d, want 201", got.Status)
	}
	if got.UserID != 42 {
		t.Fatalf("user id = %d, want 42", got.UserID)
	}
	if got.ClientIP == "" || got.OccurredAt.IsZero() || got.LatencyMs < 0 {
		t.Fatalf("incomplete record: %+v", got)
	}
	// query string 不得进入任何字段（脱敏契约）。
	if strings.Contains(got.Route, "secret") {
		t.Fatalf("route %q leaks query string", got.Route)
	}
}

// TestOperationAuditFallsBackToRawPath 钉住未匹配路由的回退：404 也要留下审计线索。
func TestOperationAuditFallsBackToRawPath(t *testing.T) {
	var records []models.OperationAudit
	engine := auditEngine(&records, true)

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/nowhere", nil))

	if len(records) != 1 {
		t.Fatalf("captured %d records, want 1", len(records))
	}
	if records[0].Route != "/nowhere" || records[0].Status != http.StatusNotFound {
		t.Fatalf("record = %+v, want raw path /nowhere with 404", records[0])
	}
	// 未认证请求（本用例的 SetUserID 中间件仍在——用另一个引擎验证 0 值反而引入噪音，
	// 这里直接验证：user id 来自上下文而不是记录器编造）。
	if records[0].UserID != 42 {
		t.Fatalf("user id = %d, want context value 42", records[0].UserID)
	}
}

// TestOperationAuditTruncatesOversizedPath 钉住构造点截断：超长路径不毒化批次。
func TestOperationAuditTruncatesOversizedPath(t *testing.T) {
	var records []models.OperationAudit
	engine := auditEngine(&records, true)

	long := "/" + strings.Repeat("a", 2*models.OperationAuditRouteLen)
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, long, nil))

	if len(records) != 1 {
		t.Fatalf("captured %d records, want 1", len(records))
	}
	if got := len([]rune(records[0].Route)); got != models.OperationAuditRouteLen {
		t.Fatalf("route length = %d runes, want truncated to %d", got, models.OperationAuditRouteLen)
	}
}

// TestOperationAuditNilEnqueuePassesThrough 钉住 nil 契约：没接审计是正常路径。
func TestOperationAuditNilEnqueuePassesThrough(t *testing.T) {
	gin.SetMode(gin.TestMode)
	engine := gin.New()
	engine.Use(OperationAudit(nil))
	engine.GET("/ok", func(c *gin.Context) { c.Status(http.StatusOK) })

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/ok", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
}

// TestOperationAuditDropDoesNotAffectResponse 钉住「审计故障不放大」：
// enqueue 返回 false（队列满载）时响应不受影响。
func TestOperationAuditDropDoesNotAffectResponse(t *testing.T) {
	var records []models.OperationAudit
	engine := auditEngine(&records, false)

	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/things/7", nil))
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d, want 201 despite audit drop", rec.Code)
	}
}

func TestTruncateRunes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in    string
		limit int
		want  string
	}{
		{"abc", 5, "abc"},
		{"abcdef", 3, "abc"},
		{"中文路径测试", 3, "中文路"}, // 按字符截断，不产生半个 UTF-8 序列
		{"", 3, ""},
	}
	for _, tt := range tests {
		if got := truncateRunes(tt.in, tt.limit); got != tt.want {
			t.Errorf("truncateRunes(%q, %d) = %q, want %q", tt.in, tt.limit, got, tt.want)
		}
	}
}
