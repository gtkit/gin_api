package bootstrap

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/gtkit/gin-api/internal/platform/config"
)

// TestInstallTenantIsolationIsANoOpWhenDisabled 钉住关闭时零动作。
//
// 传 nil 连接：关闭态若真的走到安装，就会因为连接未初始化而报错——
// 因此"返回 nil"同时证明了它没有触碰连接。
func TestInstallTenantIsolationIsANoOpWhenDisabled(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{}
	if err := installTenantIsolation(cfg, nil); err != nil {
		t.Fatalf("installTenantIsolation with isolation disabled = %v, want nil", err)
	}
}

func TestInstallTenantIsolationToleratesNilConfig(t *testing.T) {
	t.Parallel()

	if err := installTenantIsolation(nil, nil); err != nil {
		t.Fatalf("installTenantIsolation(nil, nil) = %v, want nil", err)
	}
}

// TestOnlyTheServingAssemblyInstallsIsolation 钉住装配面这条边界。
//
// 迁移运行时**不得**安装本插件：migratex 读版本表时用 `.Table(...)` 不带 Model，
// 那类语句没有模型元数据，会撞上插件的 fail-closed，而失败信息指向租户身份，
// 与真实原因（跑的是迁移路径）毫无关系——`make migrate-status` 会以一条
// 完全误导的错误失败。既有的 otelgorm 插件同样只装在服务运行时。
//
// 这条边界只写在注释里必然被违反：把它补齐看起来像是修了一个遗漏。
// 因此判据钉住"调用点集合"——多一处就失败，并指出是哪个函数。
//
// 能力边界：它只看本包的调用点。若将来迁移用的连接改到别处构造，
// 判据不会跟着走——那种改动必须同时更新这里。
func TestOnlyTheServingAssemblyInstallsIsolation(t *testing.T) {
	t.Parallel()

	const target = "installTenantIsolation"
	// 允许的调用者：服务装配阶段，以及本判据之外的测试不计入。
	allowed := []string{"newProviders"}

	callers, scanned := callersOf(t, target)
	if scanned == 0 {
		t.Fatal("scanned no non-test .go files; the enumeration is broken")
	}
	if len(callers) == 0 {
		t.Fatalf("%s has no caller at all; the isolation plugin would never be installed", target)
	}

	for _, c := range callers {
		if !slices.Contains(allowed, c) {
			t.Errorf("%s is called from %q, which is not part of the serving assembly (%v). "+
				"Installing it on the migration handle makes `make migrate-status` fail with a "+
				"tenant-identity error that has nothing to do with the real cause.",
				target, c, allowed)
		}
	}
}

// callersOf 返回本包内调用了 name 的函数名集合，以及扫描到的文件数。
func callersOf(t *testing.T, name string) ([]string, int) {
	t.Helper()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	fset := token.NewFileSet()
	var callers []string
	scanned := 0

	for _, e := range entries {
		file := e.Name()
		if e.IsDir() || !strings.HasSuffix(file, ".go") || strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, parseErr := parser.ParseFile(fset, file, nil, 0)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", file, parseErr)
		}
		scanned++

		for _, decl := range parsed.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil || fn.Name.Name == name {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, isCall := n.(*ast.CallExpr)
				if !isCall {
					return true
				}
				if ident, isIdent := call.Fun.(*ast.Ident); isIdent && ident.Name == name {
					callers = append(callers, fn.Name.Name)
				}
				return true
			})
		}
	}

	slices.Sort(callers)
	return slices.Compact(callers), scanned
}
