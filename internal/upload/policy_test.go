package upload

import "testing"

// TestZeroValuePolicyDeniesEverything 钉住 fail-closed 契约：零值策略拒绝一切内联。
func TestZeroValuePolicyDeniesEverything(t *testing.T) {
	t.Parallel()

	var p Policy
	for _, mt := range []string{"image/png", "text/plain", "application/pdf", ""} {
		if p.AllowsInline(mt) {
			t.Fatalf("zero-value Policy allowed %q inline, want deny-all", mt)
		}
	}
	if p.AllowsInlinePath("/uploads/a.png") {
		t.Fatal("zero-value Policy allowed path inline, want deny-all")
	}
}

func TestDefaultPolicyWhitelist(t *testing.T) {
	t.Parallel()

	p := DefaultPolicy()
	tests := []struct {
		mime string
		want bool
	}{
		{"image/png", true},
		{"image/jpeg", true},
		{"application/pdf", true},
		{"text/plain", true},
		// 规范化：参数与大小写不影响判定。
		{"text/plain; charset=utf-8", true},
		{"IMAGE/PNG", true},
		// 有脚本执行面的类型必须在白名单之外。
		{"text/html", false},
		{"image/svg+xml", false},
		{"application/javascript", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := p.AllowsInline(tt.mime); got != tt.want {
			t.Errorf("AllowsInline(%q) = %t, want %t", tt.mime, got, tt.want)
		}
	}
}

func TestAllowsInlinePath(t *testing.T) {
	t.Parallel()

	p := DefaultPolicy()
	tests := []struct {
		path string
		want bool
	}{
		{"/uploads/photo.png", true},
		{"/uploads/photo.JPG", true}, // 扩展名大小写不敏感（mime.TypeByExtension 语义）
		{"/uploads/report.pdf", true},
		{"/uploads/page.html", false},
		{"/uploads/logo.svg", false},
		{"/uploads/noext", false},           // 无扩展名 → 不可内联
		{"/uploads/file.unknownext", false}, // 未知扩展名 → 不可内联
	}
	for _, tt := range tests {
		if got := p.AllowsInlinePath(tt.path); got != tt.want {
			t.Errorf("AllowsInlinePath(%q) = %t, want %t", tt.path, got, tt.want)
		}
	}
}

// TestNewPolicySkipsBlankEntries 钉住构造边界：空串/纯参数项不产生可命中的白名单键。
func TestNewPolicySkipsBlankEntries(t *testing.T) {
	t.Parallel()

	p := NewPolicy("", "  ", "; charset=utf-8")
	if p.AllowsInline("") || p.AllowsInline("; charset=utf-8") {
		t.Fatal("blank whitelist entries must not admit blank MIME types")
	}
}
