package resource

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolvePathFindsProjectResourceWithoutChdir(t *testing.T) {
	root := t.TempDir()
	t.Setenv("APP_ROOT", root)
	relative := filepath.Join("config", "pem", "jwtpri.pem")
	expected := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(expected), 0o700); err != nil {
		t.Fatal(err)
	}
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	fixture := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
	if err := os.WriteFile(expected, fixture, 0o600); err != nil {
		t.Fatal(err)
	}
	cwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	resolved := ResolvePath(relative)

	expectedSuffix := filepath.Join("config", "pem", "jwtpri.pem")
	if !strings.HasSuffix(filepath.Clean(resolved), expectedSuffix) {
		t.Fatalf("ResolvePath = %q, want suffix %q", resolved, expectedSuffix)
	}

	if !filepath.IsAbs(resolved) {
		t.Fatalf("ResolvePath = %q, want absolute project path", resolved)
	}
	if resolved != expected {
		t.Fatalf("ResolvePath = %q, want fixture path %q", resolved, expected)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() {
		t.Fatal("resolved PEM is not a regular file")
	}
	contents, err := os.ReadFile(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(contents, fixture) {
		t.Fatal("resolved PEM contents differ from the test fixture")
	}
	after, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if after != cwd {
		t.Fatalf("working directory changed from %q to %q", cwd, after)
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
