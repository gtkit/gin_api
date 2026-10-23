//go:build ignore

// check-repository-context checks the typed, local origins of classic *gorm.DB
// calls in repositories. It is deliberately conservative: an unrecognised
// origin is a finding, not evidence that a request context reaches the query.
package main

import (
	"errors"
	"fmt"
	"go/ast"
	"go/token"
	"go/types"
	"os"
	"slices"
	"strings"

	"golang.org/x/tools/go/packages"
)

func main() {
	if err := checkRepositoryContext(); err != nil {
		if _, writeErr := fmt.Fprintln(os.Stderr, err); writeErr != nil {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

func checkRepositoryContext() error {
	loaded, err := packages.Load(&packages.Config{
		Mode: packages.NeedName | packages.NeedFiles | packages.NeedCompiledGoFiles |
			packages.NeedImports | packages.NeedTypes | packages.NeedTypesInfo |
			packages.NeedSyntax | packages.NeedModule,
		Tests: false,
	}, "./internal/repository/...")
	if err != nil {
		return fmt.Errorf("load repositories: %w", err)
	}
	findings, err := repositoryContextFindings(loaded)
	if err != nil {
		return err
	}
	if len(findings) != 0 {
		return fmt.Errorf("repository context check failed:\n%s", strings.Join(findings, "\n"))
	}
	_, err = fmt.Println("repository context check OK")
	return err
}

func repositoryContextFindings(loaded []*packages.Package) ([]string, error) {
	if len(loaded) == 0 {
		return nil, errors.New("repository context check: no packages loaded")
	}
	var findings []string
	for _, pkg := range loaded {
		if len(pkg.Errors) != 0 {
			return nil, fmt.Errorf("load %s: %v", pkg.PkgPath, pkg.Errors)
		}
		if pkg.Module == nil || pkg.TypesInfo == nil || len(pkg.Syntax) == 0 {
			return nil, fmt.Errorf("repository context check: incomplete type information for %s", pkg.PkgPath)
		}
		dbtxPath := pkg.Module.Path + "/internal/repository/dbtx"
		if pkg.PkgPath == dbtxPath {
			continue // The context/transaction binding implementation is the sole exemption.
		}
		guard := newContextGuard(pkg, dbtxPath)
		findings = append(findings, guard.check()...)
	}
	slices.Sort(findings)
	return findings, nil
}

type contextGuard struct {
	pkg          *packages.Package
	dbtxPath     string
	helpers      map[types.Object]bool
	assignments  map[types.Object][]ast.Expr
	contexts     map[types.Object]bool
	transactions map[types.Object]ast.Expr
}

func newContextGuard(pkg *packages.Package, dbtxPath string) *contextGuard {
	g := &contextGuard{
		pkg: pkg, dbtxPath: dbtxPath,
		helpers:      make(map[types.Object]bool),
		assignments:  make(map[types.Object][]ast.Expr),
		contexts:     make(map[types.Object]bool),
		transactions: make(map[types.Object]ast.Expr),
	}
	for _, file := range pkg.Syntax {
		ast.Inspect(file, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.FuncDecl:
				g.contextParameters(node.Type)
				if node.Name.Name == "mdbCtx" && node.Type.Results != nil && len(node.Type.Results.List) == 1 &&
					isGormDB(g.pkg.TypesInfo.TypeOf(node.Type.Results.List[0].Type)) {
					g.helpers[g.pkg.TypesInfo.Defs[node.Name]] = g.validHelper(node)
				}
			case *ast.FuncLit:
				g.contextParameters(node.Type)
			case *ast.AssignStmt:
				g.recordAssignments(node.Lhs, node.Rhs)
			case *ast.ValueSpec:
				left := make([]ast.Expr, len(node.Names))
				for i, name := range node.Names {
					left[i] = name
				}
				g.recordAssignments(left, node.Values)
			case *ast.CallExpr:
				selector, ok := unparen(node.Fun).(*ast.SelectorExpr)
				if !ok || selector.Sel.Name != "Transaction" || !g.gormMethod(selector) || len(node.Args) == 0 {
					break
				}
				callback, ok := node.Args[0].(*ast.FuncLit)
				if !ok || callback.Type.Params == nil || len(callback.Type.Params.List) != 1 {
					break
				}
				for _, name := range callback.Type.Params.List[0].Names {
					if isGormDB(g.pkg.TypesInfo.TypeOf(name)) {
						g.transactions[g.pkg.TypesInfo.Defs[name]] = selector.X
					}
				}
			}
			return true
		})
	}
	return g
}

func (g *contextGuard) contextParameters(fn *ast.FuncType) {
	if fn.Params == nil {
		return
	}
	for _, param := range fn.Params.List {
		for _, name := range param.Names {
			if isNamed(g.pkg.TypesInfo.TypeOf(name), "context", "Context") {
				g.contexts[g.pkg.TypesInfo.Defs[name]] = true
			}
		}
	}
}

func (g *contextGuard) recordAssignments(left, right []ast.Expr) {
	for i, expr := range left {
		name, ok := expr.(*ast.Ident)
		if !ok || name.Name == "_" {
			continue
		}
		object := g.pkg.TypesInfo.ObjectOf(name)
		if object == nil {
			continue
		}
		var source ast.Expr
		if len(left) == len(right) {
			source = right[i]
		} else if len(right) == 1 && i == 0 {
			// A derived context is the first result of context.WithCancel/Timeout.
			source = right[0]
		}
		g.assignments[object] = append(g.assignments[object], source)
	}
}

func (g *contextGuard) validHelper(fn *ast.FuncDecl) bool {
	if fn.Recv == nil || len(fn.Recv.List) != 1 || len(fn.Recv.List[0].Names) != 1 ||
		fn.Type.Params == nil || len(fn.Type.Params.List) != 1 || len(fn.Type.Params.List[0].Names) != 1 ||
		fn.Body == nil || len(fn.Body.List) != 1 {
		return false
	}
	ctx := g.pkg.TypesInfo.Defs[fn.Type.Params.List[0].Names[0]]
	if !isNamed(ctx.Type(), "context", "Context") {
		return false
	}
	ret, ok := fn.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(ret.Results) != 1 {
		return false
	}
	call, ok := unparen(ret.Results[0]).(*ast.CallExpr)
	if !ok || !g.isFunction(call.Fun, g.dbtxPath, "DB") || len(call.Args) != 2 {
		return false
	}
	argument, ok := unparen(call.Args[0]).(*ast.Ident)
	if !ok || g.pkg.TypesInfo.ObjectOf(argument) != ctx {
		return false
	}
	db, ok := unparen(call.Args[1]).(*ast.SelectorExpr)
	if !ok || !isGormDB(g.pkg.TypesInfo.TypeOf(db)) {
		return false
	}
	receiver, ok := unparen(db.X).(*ast.Ident)
	return ok && g.pkg.TypesInfo.ObjectOf(receiver) == g.pkg.TypesInfo.Defs[fn.Recv.List[0].Names[0]]
}

func (g *contextGuard) check() []string {
	var findings []string
	for _, file := range g.pkg.Syntax {
		ast.Inspect(file, func(node ast.Node) bool {
			switch node := node.(type) {
			case *ast.FuncDecl:
				if valid, exists := g.helpers[g.pkg.TypesInfo.Defs[node.Name]]; exists && !valid {
					findings = append(findings, g.finding(node.Pos(), "mdbCtx must return dbtx.DB(its context parameter, receiver DB field)"))
				}
			case *ast.SelectorExpr:
				if g.gormMethod(node) && !g.boundDB(node.X) {
					findings = append(findings, g.finding(node.Pos(), "GORM receiver must originate from mdbCtx(ctx) or dbtx.DB(ctx, db)"))
				}
			case *ast.CallExpr:
				selector, ok := unparen(node.Fun).(*ast.SelectorExpr)
				if ok && g.gormMethod(selector) && g.boundDB(selector.X) && isGormDB(g.pkg.TypesInfo.TypeOf(node)) && !g.boundDB(node) {
					findings = append(findings, g.finding(node.Pos(), "GORM call replaces or obscures the bound context"))
				}
			}
			return true
		})
	}
	return findings
}

func (g *contextGuard) finding(pos token.Pos, message string) string {
	return fmt.Sprintf("%s: %s", g.pkg.Fset.Position(pos), message)
}

// safe and anchored are separate so q = q.Where(...) can preserve a known
// root, while a cycle alone can never prove that a context was bound.
type origin struct{ safe, anchored bool }

func (g *contextGuard) boundDB(expr ast.Expr) bool {
	result := g.dbOrigin(expr, make(map[types.Object]bool))
	return result.safe && result.anchored
}

func (g *contextGuard) dbOrigin(expr ast.Expr, visiting map[types.Object]bool) origin {
	switch expr := unparen(expr).(type) {
	case *ast.Ident:
		return g.variableOrigin(g.pkg.TypesInfo.ObjectOf(expr), visiting, false)
	case *ast.CallExpr:
		if g.helpers[g.functionObject(expr.Fun)] && len(expr.Args) == 1 && g.boundContext(expr.Args[0]) {
			return origin{true, true}
		}
		if g.isFunction(expr.Fun, g.dbtxPath, "DB") && len(expr.Args) == 2 && g.boundContext(expr.Args[0]) {
			return origin{true, true}
		}
		selector, ok := unparen(expr.Fun).(*ast.SelectorExpr)
		if !ok || !g.gormMethod(selector) {
			return origin{}
		}
		switch selector.Sel.Name {
		case "WithContext":
			if len(expr.Args) != 1 || !g.boundContext(expr.Args[0]) {
				return origin{}
			}
		case "Session":
			if len(expr.Args) != 1 || !g.safeSession(expr.Args[0]) {
				return origin{}
			}
		case "Scopes":
			return origin{} // Arbitrary callbacks can return another DB; require review.
		}
		return g.dbOrigin(selector.X, visiting)
	}
	return origin{}
}

func (g *contextGuard) safeSession(expr ast.Expr) bool {
	if address, ok := unparen(expr).(*ast.UnaryExpr); ok && address.Op == token.AND {
		expr = address.X
	}
	literal, ok := unparen(expr).(*ast.CompositeLit)
	if !ok {
		return false
	}
	for _, field := range literal.Elts {
		pair, ok := field.(*ast.KeyValueExpr)
		if !ok {
			return false
		}
		key, ok := pair.Key.(*ast.Ident)
		if !ok {
			return false
		}
		if key.Name == "Context" && !g.boundContext(pair.Value) {
			return false
		}
	}
	return true
}

func (g *contextGuard) boundContext(expr ast.Expr) bool {
	result := g.contextOrigin(expr, make(map[types.Object]bool))
	return result.safe && result.anchored
}

func (g *contextGuard) contextOrigin(expr ast.Expr, visiting map[types.Object]bool) origin {
	switch expr := unparen(expr).(type) {
	case *ast.Ident:
		return g.variableOrigin(g.pkg.TypesInfo.ObjectOf(expr), visiting, true)
	case *ast.CallExpr:
		function := g.functionObject(expr.Fun)
		if function == nil || function.Pkg() == nil || function.Pkg().Path() != "context" || len(expr.Args) == 0 {
			return origin{}
		}
		switch function.Name() {
		case "WithCancel", "WithCancelCause", "WithDeadline", "WithDeadlineCause", "WithTimeout", "WithTimeoutCause", "WithValue":
			return g.contextOrigin(expr.Args[0], visiting)
		}
	}
	return origin{}
}

func (g *contextGuard) variableOrigin(object types.Object, visiting map[types.Object]bool, contextValue bool) origin {
	if object == nil {
		return origin{}
	}
	if visiting[object] {
		return origin{safe: true}
	}
	visiting[object] = true
	defer delete(visiting, object)
	result := origin{safe: true, anchored: contextValue && g.contexts[object]}
	if receiver, ok := g.transactions[object]; ok && !contextValue {
		result = g.dbOrigin(receiver, visiting)
	}
	sources := g.assignments[object]
	if len(sources) == 0 && !result.anchored {
		return origin{}
	}
	for _, source := range sources {
		var next origin
		if contextValue {
			next = g.contextOrigin(source, visiting)
		} else {
			next = g.dbOrigin(source, visiting)
		}
		result.safe = result.safe && next.safe
		result.anchored = result.anchored || next.anchored
	}
	return result
}

func (g *contextGuard) gormMethod(selector *ast.SelectorExpr) bool {
	selection := g.pkg.TypesInfo.Selections[selector]
	return selection != nil && (selection.Kind() == types.MethodVal || selection.Kind() == types.MethodExpr) &&
		isGormDB(g.pkg.TypesInfo.TypeOf(selector.X))
}

func (g *contextGuard) isFunction(expr ast.Expr, path, name string) bool {
	object := g.functionObject(expr)
	return object != nil && object.Pkg() != nil && object.Pkg().Path() == path && object.Name() == name
}

func (g *contextGuard) functionObject(expr ast.Expr) types.Object {
	switch expr := unparen(expr).(type) {
	case *ast.Ident:
		return g.pkg.TypesInfo.ObjectOf(expr)
	case *ast.SelectorExpr:
		return g.pkg.TypesInfo.ObjectOf(expr.Sel)
	}
	return nil
}

func isGormDB(value types.Type) bool {
	if value == nil {
		return false
	}
	pointer, ok := types.Unalias(value).(*types.Pointer)
	return ok && isNamed(pointer.Elem(), "gorm.io/gorm", "DB")
}

func isNamed(value types.Type, path, name string) bool {
	if value == nil {
		return false
	}
	named, ok := types.Unalias(value).(*types.Named)
	return ok && named.Obj().Pkg() != nil && named.Obj().Pkg().Path() == path && named.Obj().Name() == name
}

func unparen(expr ast.Expr) ast.Expr {
	for {
		paren, ok := expr.(*ast.ParenExpr)
		if !ok {
			return expr
		}
		expr = paren.X
	}
}
