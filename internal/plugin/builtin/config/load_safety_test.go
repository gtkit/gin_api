package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gtkit/gin-api/internal/testutil"
)

// TestCheckConfigFileSafety 钉住生产配置文件的形态校验（config-validation spec）：
// 组/其他可写与符号链接被拒绝，紧权限的普通文件通过。
//
// 每个 case 声明自己需要的平台能力：被测判据分别建立在 Unix 权限位与符号链接上，
// 两者在 Windows 上都不成立。按 case 声明而不是给整个测试加一条——
// 那会让本可在任何平台执行的 case 一起消失。
func TestCheckConfigFileSafety(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		require func(t *testing.T)
		prepare func(t *testing.T, dir string) string
		wantErr string
	}{
		{
			name: "紧权限普通文件通过",
			// Windows 上普通文件恒报 0666，判据会认定它组可写——
			// 这个 case 在那里不是"失败"，而是无法表达。
			require: testutil.RequireUnixFilePerm,
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
			// Windows 上这个 case 会"通过"，但通过的理由是错的：Chmod 不改变
			// 该平台的权限位，文件恒为 0666，于是判据正好命中"组可写"。
			// 靠一个巧合成立的断言证明不了判据本身，所以同样声明能力。
			require: testutil.RequireUnixFilePerm,
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
			name:    "符号链接被拒绝",
			require: testutil.RequireSymlink,
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

			if tt.require != nil {
				tt.require(t)
			}

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
