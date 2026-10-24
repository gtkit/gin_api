package upload

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func serveUploadedFile(t *testing.T, policy Policy, target string) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)

	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)

	FileHeaders(policy)(c)
	c.String(http.StatusOK, "content")
	return rec
}

// TestFileHeadersNosniffAlways 钉住 nosniff 恒定输出：与内联判定无关。
func TestFileHeadersNosniffAlways(t *testing.T) {
	for _, target := range []string{"/uploads/a.png", "/uploads/a.html"} {
		rec := serveUploadedFile(t, DefaultPolicy(), target)
		if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
			t.Fatalf("%s: X-Content-Type-Options = %q, want nosniff", target, got)
		}
	}
}

// TestFileHeadersForcesAttachment 钉住白名单外强制下载。
func TestFileHeadersForcesAttachment(t *testing.T) {
	tests := []struct {
		name           string
		policy         Policy
		target         string
		wantAttachment bool
	}{
		{name: "html forced", policy: DefaultPolicy(), target: "/uploads/page.html", wantAttachment: true},
		{name: "svg forced", policy: DefaultPolicy(), target: "/uploads/logo.svg", wantAttachment: true},
		{name: "no extension forced", policy: DefaultPolicy(), target: "/uploads/blob", wantAttachment: true},
		{name: "png inline", policy: DefaultPolicy(), target: "/uploads/photo.png", wantAttachment: false},
		// 零值策略：图片也强制下载（fail-closed）。
		{name: "zero policy forces png", policy: Policy{}, target: "/uploads/photo.png", wantAttachment: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := serveUploadedFile(t, tt.policy, tt.target)
			disposition := rec.Header().Get("Content-Disposition")
			if got := strings.HasPrefix(disposition, "attachment"); got != tt.wantAttachment {
				t.Fatalf("Content-Disposition = %q, want attachment=%t", disposition, tt.wantAttachment)
			}
		})
	}
}

// TestAttachmentDispositionRejectsHeaderInjection 钉住文件名头注入防线：
// 引号/控制字符/空格等一律退化为不带 filename 的裸 attachment。
func TestAttachmentDispositionRejectsHeaderInjection(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		filename string
		want     string
	}{
		{"safe name kept", "report-v1.2_final.html", `attachment; filename="report-v1.2_final.html"`},
		{"quote dropped", `a".html`, "attachment"},
		{"semicolon dropped", "a;b.html", "attachment"},
		{"space dropped", "a b.html", "attachment"},
		{"control char dropped", "a\r\nSet-Cookie:x.html", "attachment"},
		{"non-ascii dropped", "报表.html", "attachment"},
		{"empty", "", "attachment"},
		{"oversized dropped", strings.Repeat("a", 256) + ".html", "attachment"},
	}
	for _, tt := range tests {
		if got := attachmentDisposition(tt.filename); got != tt.want {
			t.Errorf("%s: attachmentDisposition(%q) = %q, want %q", tt.name, tt.filename, got, tt.want)
		}
	}
}
