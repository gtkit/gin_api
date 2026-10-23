//go:build !windows

package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestJWTCommandChecksProductionFileSafety(t *testing.T) {
	root := t.TempDir()
	t.Setenv("APP_ROOT", root)
	writeJWTCommandConfig(t, root, "env: prod\njwt:\n  active:\n    kid: test-kid\n    private_key_path: private.pem\n    public_key_path: public.pem\n")
	if err := os.Chmod(filepath.Join(root, "config/env/env.yml"), 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadJWTKeyConfigForCommand(); err == nil {
		t.Fatal("world-writable production config accepted")
	}
}
