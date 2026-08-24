package models

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// TestManagedCoversEveryTableModel 是清单完整性的枚举判据。
//
// 漏项是清单类缺陷里最安静的一种：没有任何机制要求新表被登记，于是
// "又加了一张表但忘了登记"不会有任何症状。而对租户隔离来说，漏登记的表
// 会让装配期校验少看一张表——判别列的核对与豁免清单的核对都因此变弱。
//
// 判据方向是「代码里有的都必须在清单里」，反向多余项由 TestManagedHasNoStaleEntry 覆盖。
func TestManagedCoversEveryTableModel(t *testing.T) {
	t.Parallel()

	declared := tableModelNames(t)
	if len(declared) == 0 {
		t.Fatal("scanned no types with TableName(); the enumeration is broken")
	}

	listed := managedTypeNames(t)
	for _, name := range declared {
		if !slices.Contains(listed, name) {
			t.Errorf("%s declares TableName() but is not in Managed(); "+
				"tenant isolation would validate its column and exemptions against an incomplete model set", name)
		}
	}
}

// TestManagedHasNoStaleEntry 反向判据：清单里不得有已经不存在的模型。
func TestManagedHasNoStaleEntry(t *testing.T) {
	t.Parallel()

	declared := tableModelNames(t)
	for _, name := range managedTypeNames(t) {
		if !slices.Contains(declared, name) {
			t.Errorf("Managed() lists %s, which no longer declares TableName(); remove the entry", name)
		}
	}
}

// TestManagedReturnsPointers 钉住清单里是指针。
//
// GORM 的 schema 解析对值与指针都工作，但 SetColumn 这类写入路径需要可寻址的值。
// 清单里混入值类型不会立刻报错，只会在某个写入路径上表现为"盖章没生效"。
func TestManagedReturnsPointers(t *testing.T) {
	t.Parallel()

	for _, m := range Managed() {
		if reflect.TypeOf(m).Kind() != reflect.Pointer {
			t.Errorf("Managed() contains a non-pointer %T", m)
		}
	}
}

// managedTypeNames 返回 Managed() 里各元素的具体类型名。
func managedTypeNames(t *testing.T) []string {
	t.Helper()

	names := make([]string, 0, len(Managed()))
	for _, m := range Managed() {
		rt := reflect.TypeOf(m)
		for rt.Kind() == reflect.Pointer {
			rt = rt.Elem()
		}
		names = append(names, rt.Name())
	}
	return names
}

// tableModelNames 扫本包源码，返回全部声明了 TableName() string 的类型名。
func tableModelNames(t *testing.T) []string {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	fset := token.NewFileSet()
	var names []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(fset, name, nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", name, parseErr)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name != "TableName" || fn.Recv == nil || len(fn.Recv.List) == 0 {
				continue
			}
			if recv := receiverTypeName(fn.Recv.List[0].Type); recv != "" {
				names = append(names, recv)
			}
		}
	}
	slices.Sort(names)
	return slices.Compact(names)
}

// receiverTypeName 取接收者的类型名，指针与值两种写法都认。
func receiverTypeName(expr ast.Expr) string {
	switch v := expr.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.StarExpr:
		return receiverTypeName(v.X)
	default:
		return ""
	}
}
