//go:build scaffold

package projectinit

import (
	"bytes"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRenderModule(t *testing.T) {
	for _, name := range []string{"example", "billing_record", "application"} {
		files, err := RenderModule("example.com/service", name)
		if err != nil || len(files) != 10 {
			t.Fatalf("render %s: %d files, %v", name, len(files), err)
		}
		for path, content := range files {
			if !filepath.IsLocal(path) || bytes.Contains(content, []byte("{{")) {
				t.Fatalf("invalid rendered target %s", path)
			}
			if strings.HasSuffix(path, ".go") {
				if _, err := parser.ParseFile(token.NewFileSet(), path, content, parser.AllErrors); err != nil {
					t.Fatalf("parse %s: %v", path, err)
				}
			}
		}
	}
	for _, name := range []string{"", "../escape", "type", "Upper", "bad-name"} {
		if _, err := RenderModule("example.com/service", name); err == nil {
			t.Fatalf("accepted invalid name %q", name)
		}
	}
}

func TestGeneratedFilesArePreviewedAndNeverOverwrite(t *testing.T) {
	// This is a file-I/O fixture, not a checkout or a copy of the project.
	root := t.TempDir()
	files := map[string][]byte{"output/value.txt": []byte("generated")}
	if _, err := WriteGenerated(root, files, true); err != nil {
		t.Fatal(err)
	}
	if entries, err := os.ReadDir(root); err != nil || len(entries) != 0 {
		t.Fatalf("preview wrote files: %v %v", entries, err)
	}
	if _, err := WriteGenerated(root, files, false); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteGenerated(root, map[string][]byte{
		"output/value.txt": []byte("overwrite"), "before.txt": []byte("partial"),
	}, false); err == nil {
		t.Fatal("accepted existing output")
	}
	data, err := os.ReadFile(filepath.Join(root, "output/value.txt"))
	if err != nil || string(data) != "generated" {
		t.Fatalf("existing content changed: %q %v", data, err)
	}
	if _, err := os.Stat(filepath.Join(root, "before.txt")); !os.IsNotExist(err) {
		t.Fatalf("validation failure left partial output: %v", err)
	}
	for _, path := range []string{"../outside.txt", "output/../escape.txt", "."} {
		if _, err := WriteGenerated(root, map[string][]byte{path: nil}, false); err == nil {
			t.Fatalf("accepted path %q", path)
		}
	}
	if err := os.Symlink("output", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := WriteGenerated(root, map[string][]byte{"link/value2.txt": nil}, false); err == nil {
		t.Fatal("accepted symlink parent")
	}
}
