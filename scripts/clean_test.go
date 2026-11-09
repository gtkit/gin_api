//go:build ignore

package main

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestCleanHyphenatedNameAndBoundaries(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("go.mod", []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"gin-api", "gin_api", "a", "app-v2"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(root)
			for _, dir := range []string{"bin", ".cache"} {
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
			}
			for _, path := range []string{"go.mod", filepath.Join("bin", name), "bin/keep", ".cache/coverage.out"} {
				if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			if err := cleanOutputs("./bin", name, ".cache/coverage.out", io.Discard); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat("bin/keep"); err != nil {
				t.Fatal("unmanaged output was removed", err)
			}
			for _, path := range []string{filepath.Join("bin", name), ".cache/coverage.out"} {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("managed output %s still exists: %v", path, err)
				}
			}
			if err := cleanOutputs("bin", name, ".cache/coverage.out", io.Discard); err != nil {
				t.Fatal("cleanup is not idempotent", err)
			}
		})
	}
	for _, name := range []string{"", "../outside", "a/b", "a\\b", "-app", "app;echo", "app*", ".", ".."} {
		if err := cleanOutputs("bin", name, ".cache/coverage.out", io.Discard); err == nil {
			t.Errorf("unsafe name accepted: %q", name)
		}
	}
}

func TestCleanRefusesSymlinkBeforeRemovingAnything(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.WriteFile("go.mod", []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir("bin", 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile("bin/gin-api", []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(t.TempDir(), ".cache"); err != nil {
		t.Fatal(err)
	}
	if err := cleanOutputs("bin", "gin-api", ".cache/coverage.out", io.Discard); err == nil {
		t.Fatal("symlink accepted")
	}
	if _, err := os.Stat("bin/gin-api"); err != nil {
		t.Fatal("validation removed a file before rejecting the symlink", err)
	}
}
