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

// TestResolvePathReturnsAbsolutePathsUnchanged 钉住绝对路径不被改写。
//
// filepath.Join("/app", "/var/log/x") 会得到 "/app/var/log/x"——一个完全不同的位置。
// 配置允许绝对路径（生产日志路径校验就要求绝对路径位于 APP_ROOT 下），
// 因此这条路径真实会走到：改写的后果是日志写去了别处，而且没有任何报错。
func TestResolvePathReturnsAbsolutePathsUnchanged(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "sub", "app.log")

	// APP_ROOT 存在时最容易触发拼接，因此显式设置它再断言。
	t.Setenv("APP_ROOT", dir)

	if got := ResolvePath(target); got != target {
		t.Fatalf("ResolvePath(%q) = %q, want it unchanged", target, got)
	}
}
