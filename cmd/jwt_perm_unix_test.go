//go:build !windows

package cmd

import (
	"os"
	"testing"

	"github.com/gtkit/gin-api/internal/pkg/jwtauth"
)

// TestJWTGeneratePermissions 钉住产物的文件权限。
//
// 只在 Unix 上运行：Windows 的访问控制是 ACL，不映射到 os.FileMode 的权限位，
// Go 在该平台对普通文件恒报 0666，这里的断言在那边只能表达"这是 Windows"。
// 服务的部署目标是 Linux 容器，因此该断言在交付路径上始终被执行。
func TestJWTGeneratePermissions(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	result, err := generateKeyPair("k2026a", dir)
	if err != nil {
		t.Fatalf("generateKeyPair: %v", err)
	}

	privInfo, err := os.Stat(result.privatePath)
	if err != nil {
		t.Fatalf("stat private key: %v", err)
	}
	// 私钥必须是 0600：group/other 可读意味着同机器上的任何进程都能签发令牌。
	if perm := privInfo.Mode().Perm(); perm != 0o600 {
		t.Fatalf("private key mode = %04o, want 0600", perm)
	}

	pubInfo, err := os.Stat(result.publicPath)
	if err != nil {
		t.Fatalf("stat public key: %v", err)
	}
	if perm := pubInfo.Mode().Perm(); perm != 0o644 {
		t.Fatalf("public key mode = %04o, want 0644", perm)
	}
	if _, err := jwtauth.LoadEd25519PublicKey(result.publicPath); err != nil {
		t.Fatalf("generated public key unusable: %v", err)
	}
	if err := os.Chmod(result.publicPath, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := jwtauth.LoadEd25519PublicKey(result.publicPath); err == nil {
		t.Fatal("runtime accepted group/other writable public key")
	}
}
