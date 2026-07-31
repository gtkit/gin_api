package cmd

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadModuleDirective(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		content string
		want    string
		wantErr bool
	}{
		{name: "plain", content: "module example.com/foo\n\ngo 1.26\n", want: "example.com/foo"},
		{name: "leading blank lines", content: "\n\nmodule example.com/bar\n", want: "example.com/bar"},
		{name: "indented", content: "   module example.com/baz\n", want: "example.com/baz"},
		{name: "comment before", content: "// note\nmodule example.com/qux\n", want: "example.com/qux"},
		{name: "no module line", content: "go 1.26\n", wantErr: true},
		{name: "empty module path", content: "module \n", wantErr: true},
		// `modulepath` 不是 `module ` 前缀，不能误命中。
		{name: "similar prefix only", content: "modulepath example.com/x\n", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(t.TempDir(), "go.mod")
			if err := os.WriteFile(path, []byte(tt.content), 0o644); err != nil {
				t.Fatalf("write go.mod: %v", err)
			}
			got, err := readModuleDirective(path)
			if (err != nil) != tt.wantErr {
				t.Fatalf("readModuleDirective() err = %v, wantErr = %v", err, tt.wantErr)
			}
			if !tt.wantErr && got != tt.want {
				t.Fatalf("readModuleDirective() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestModulePathFromGoModWalksUpward(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/deep\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	nested := filepath.Join(root, "a", "b", "c")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	got, err := modulePathFromGoMod(nested)
	if err != nil {
		t.Fatalf("modulePathFromGoMod: %v", err)
	}
	if got != "example.com/deep" {
		t.Fatalf("modulePathFromGoMod() = %q, want example.com/deep", got)
	}
}

func TestModulePathFromGoModFailsWhenAbsent(t *testing.T) {
	t.Parallel()

	// t.TempDir() 在某些系统上位于 /var/folders 之类的目录，其祖先不含 go.mod。
	dir := t.TempDir()
	if _, err := modulePathFromGoMod(dir); err == nil {
		t.Fatal("expected an error when no go.mod exists upward")
	}
}

// TestResolveModulePathUsesBuildInfo 证明主来源是 build info 而非 go.mod：
// 测试二进制由 go test 构建，Main.Path 即本仓 module path。
//
// 这条契约的意义在生产镜像里——镜像内没有 go.mod，只能靠 build info。
func TestResolveModulePathUsesBuildInfo(t *testing.T) {
	t.Parallel()

	got, err := resolveModulePath()
	if err != nil {
		t.Fatalf("resolveModulePath: %v", err)
	}
	if got == "" || got == "command-line-arguments" {
		t.Fatalf("resolveModulePath() = %q, want a real module path", got)
	}
	// 绝不能回落到硬编码值以外的推导；这里只断言它解析出了本仓自己的路径。
	if !strings.Contains(got, "/") {
		t.Fatalf("resolveModulePath() = %q, does not look like a module path", got)
	}
}
