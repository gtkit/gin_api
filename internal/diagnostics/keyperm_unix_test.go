//go:build !windows

package diagnostics

import (
	"os"
	"path/filepath"
	"testing"
)

// TestJWTKeyPermissionsDetectsTooOpen 钉住"权限过宽必须被检出"。
//
// 只在 Unix 上运行：Windows 的访问控制是 ACL，不映射到 os.FileMode 的权限位，
// chmod 0644 在那边不会改变 Go 看到的 mode，这条断言无法表达被测语义。
// 服务的部署目标是 Linux 容器，因此该断言在交付路径上始终被执行。
func TestJWTKeyPermissionsDetectsTooOpen(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	priv := filepath.Join(dir, "priv.pem")
	pub := filepath.Join(dir, "pub.pem")

	if err := os.WriteFile(priv, []byte("x"), 0o600); err != nil {
		t.Fatalf("write private key: %v", err)
	}
	if err := os.WriteFile(pub, []byte("x"), 0o600); err != nil {
		t.Fatalf("write public key: %v", err)
	}

	cfg := validConfig()
	cfg.JWT.Active.PrivateKeyPath = priv
	cfg.JWT.Active.PublicKeyPath = pub

	// 放宽权限后必须被检出：group/other 可读意味着同机器上的任何进程都能签发令牌。
	if err := os.Chmod(priv, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	got := checkJWTKeys(cfg)
	if got.Status != StatusFail || got.ErrorCode != CodeInsecurePerm {
		t.Fatalf("status=%q code=%q, want fail/%s for a group-readable private key",
			got.Status, got.ErrorCode, CodeInsecurePerm)
	}
}
