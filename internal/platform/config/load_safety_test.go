package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCheckConfigFileSafety 钉住生产配置文件的形态校验（config-validation spec）：
// 组/其他可写与符号链接被拒绝，紧权限的普通文件通过。
func TestCheckConfigFileSafety(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		prepare func(t *testing.T, dir string) string
		wantErr string
	}{
		{
			name: "紧权限普通文件通过",
			prepare: func(t *testing.T, dir string) string {
				t.Helper()
				p := filepath.Join(dir, "env.yml")
				if err := os.WriteFile(p, []byte("env: prod\n"), 0o600); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
				return p
			},
		},
		{
			name: "组可写被拒绝",
			prepare: func(t *testing.T, dir string) string {
				t.Helper()
				p := filepath.Join(dir, "env.yml")
				if err := os.WriteFile(p, []byte("env: prod\n"), 0o600); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
				// 显式 Chmod：WriteFile 的 perm 受 umask 影响，直接给足权限位。
				if err := os.Chmod(p, 0o620); err != nil {
					t.Fatalf("Chmod: %v", err)
				}
				return p
			},
			wantErr: "writable by group or others",
		},
		{
			name: "符号链接被拒绝",
			prepare: func(t *testing.T, dir string) string {
				t.Helper()
				target := filepath.Join(dir, "real.yml")
				if err := os.WriteFile(target, []byte("env: prod\n"), 0o600); err != nil {
					t.Fatalf("WriteFile: %v", err)
				}
				link := filepath.Join(dir, "env.yml")
				if err := os.Symlink(target, link); err != nil {
					t.Fatalf("Symlink: %v", err)
				}
				return link
			},
			wantErr: "symlink",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			path := tt.prepare(t, t.TempDir())
			err := checkConfigFileSafety(path)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatalf("checkConfigFileSafety: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("err = %v, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}
