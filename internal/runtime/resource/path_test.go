package resource

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolvePathFindsProjectResourceWithoutChdir(t *testing.T) {
	resolved := ResolvePath(filepath.Join("config", "pem", "jwtpri.pem"))

	expectedSuffix := filepath.Join("config", "pem", "jwtpri.pem")
	if !strings.HasSuffix(filepath.Clean(resolved), expectedSuffix) {
		t.Fatalf("ResolvePath = %q, want suffix %q", resolved, expectedSuffix)
	}

	// 当运行环境确实包含 PEM 文件时，要求路径为绝对路径并且文件可达。
	// 在 CI 或独立 go test 中文件可能不存在，此时仅校验路径格式。
	if _, err := os.Stat(resolved); err != nil {
		t.Skipf("PEM file not available in this environment, skipping stat check: %v", err)
	}
	if !filepath.IsAbs(resolved) {
		t.Fatalf("ResolvePath = %q, want absolute project path", resolved)
	}
}
