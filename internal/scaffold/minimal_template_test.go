//go:build scaffold

package scaffold

import (
	"go/ast"
	"go/parser"
	"go/token"
	"testing"
)

func TestGeneratedModuleOmitsUnusedStorageAPI(t *testing.T) {
	t.Parallel()
	data, err := newModuleTemplateData("notes", []string{"title:string"}, "example.com/notes")
	if err != nil {
		t.Fatal(err)
	}
	for _, source := range []string{modelTemplate, repositoryUtilTemplate, applicationPortsTemplate} {
		generated, err := renderTemplate(source, data, true)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(token.NewFileSet(), "generated.go", generated, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.FuncDecl:
				switch node.Name.Name {
				case "ExistsByID", "MarshalBinary", "UnmarshalBinary":
					t.Errorf("generated unused method %s", node.Name.Name)
				}
			case *ast.FuncType:
				for _, param := range node.Params.List {
					if _, ok := param.Type.(*ast.Ellipsis); ok {
						t.Error("generated an unused variadic storage projection")
					}
				}
			}
			return true
		})
	}
}
