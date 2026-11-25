//go:build scaffold

package projectinit

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestRenderModule(t *testing.T) {
	for _, name := range []string{"example", "billing_record", "application"} {
		files, err := RenderModule("example.com/service", name)
		if err != nil || len(files) != 12 {
			t.Fatalf("render %s: %d files, %v", name, len(files), err)
		}
		for path, content := range files {
			if !filepath.IsLocal(path) || bytes.Contains(content, []byte("{{")) {
				t.Fatalf("invalid rendered target %s", path)
			}
			if strings.HasSuffix(path, ".go") {
				parsed, err := parser.ParseFile(token.NewFileSet(), path, content, parser.AllErrors)
				if err != nil {
					t.Fatalf("parse %s: %v", path, err)
				}
				checkModuleImports(t, path, parsed.Imports)
			}
		}
		appPath := "internal/module/" + name + "/application/"
		httpPath := "internal/module/" + name + "/transport/http/"
		commands := files[appPath+"commands.go"]
		handler := files[httpPath+"handler.go"]
		service := files[appPath+"service.go"]
		for _, method := range []string{"Get", "List", "Create", "Update", "Delete", "DeleteMany"} {
			request := method + "Request"
			if !bytes.Contains(commands, []byte("type "+request+" struct")) ||
				!bytes.Contains(service, []byte("request "+request)) ||
				!bytes.Contains(handler, []byte("application."+request+"{OwnerID: ginc.UserIDFromContext(c)")) {
				t.Fatalf("%s: %s request is not wired from trusted identity through the application", name, method)
			}
		}
		router := files[httpPath+"router.go"]
		if !bytes.Contains(router, []byte("func Register(parent *gin.RouterGroup")) ||
			!bytes.Contains(router, []byte("middleware.Authorize(")) ||
			!bytes.Contains(router, []byte(`DELETE("/batch"`)) ||
			bytes.Contains(router, []byte("RegisterCallback")) {
			t.Fatalf("%s: ordinary CRUD registration contract changed", name)
		}
		repo := files["internal/repository/"+name+"/repository.go"]
		for _, contract := range []string{`owner_id = ?`, `clause.Locking{Strength: "UPDATE"}`, `SkipDefaultTransaction: true`} {
			if !bytes.Contains(repo, []byte(contract)) {
				t.Fatalf("%s: missing repository contract %q", name, contract)
			}
		}
	}
	for _, name := range []string{"", "../escape", "type", "Upper", "bad-name"} {
		if _, err := RenderModule("example.com/service", name); err == nil {
			t.Fatalf("accepted invalid name %q", name)
		}
	}
}

func checkModuleImports(t *testing.T, path string, imports []*ast.ImportSpec) {
	t.Helper()
	var application, transport bool
	if relative, ok := strings.CutPrefix(path, "internal/module/"); ok {
		_, layer, _ := strings.Cut(relative, "/")
		application = strings.HasPrefix(layer, "application/")
		transport = strings.HasPrefix(layer, "transport/http/")
	}
	for _, spec := range imports {
		pkg, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			t.Fatal(err)
		}
		if application || transport {
			if strings.Contains(pkg, "/internal/repository/") || strings.HasPrefix(pkg, "gorm.io/") {
				t.Errorf("%s imports persistence dependency %s", path, pkg)
			}
		}
		if application && pkg == "github.com/gin-gonic/gin" {
			t.Errorf("%s imports HTTP framework %s", path, pkg)
		}
		if strings.HasPrefix(path, "internal/repository/") && (pkg == "github.com/gin-gonic/gin" || strings.Contains(pkg, "/internal/module/")) {
			t.Errorf("%s imports module or HTTP dependency %s", path, pkg)
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
