//go:build !windows

package jwtauth

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReencodePermissions(t *testing.T) {
	t.Parallel()
	privatePath, publicPath := reencodingFixture(t, true)
	out := filepath.Join(t.TempDir(), "out")
	if err := ReencodeEd25519Keys(privatePath, publicPath, out); err != nil {
		t.Fatal(err)
	}
	for _, file := range []struct {
		path string
		mode os.FileMode
	}{{out, 0o700}, {filepath.Join(out, "jwtpri.pem"), 0o600}, {filepath.Join(out, "jwtpub.pem"), 0o644}} {
		info, err := os.Stat(file.path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != file.mode {
			t.Fatalf("%s permissions = %o, want %o", file.path, info.Mode().Perm(), file.mode)
		}
	}
}
