package bootstrap

import (
	"go/parser"
	"go/token"
	"os"
	"strconv"
	"strings"
	"testing"
)

// Parse imports instead of matching source text so comments and test fixtures
// cannot hide a production dependency or trigger a false positive. Runtime
// module constructors own repositories; bootstrap only composes their results.
func TestBootstrapDoesNotImportRepositories(t *testing.T) {
	t.Parallel()
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), name, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		checked++
		for _, imported := range file.Imports {
			path, err := strconv.Unquote(imported.Path.Value)
			if err != nil {
				t.Fatal(err)
			}
			if strings.HasPrefix(path, "github.com/gtkit/gin-api/internal/repository/") {
				t.Errorf("%s imports repository %s directly", name, path)
			}
		}
	}
	if checked == 0 {
		t.Fatal("no production bootstrap sources were inspected")
	}
}
