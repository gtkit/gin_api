//go:build ignore

package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

const fixtureModule = "example.com/service"

const repositoryFixture = `package repository
import (
    "context"
    "gorm.io/gorm"
    "example.com/service/internal/repository/dbtx"
)
type Repository struct { db *gorm.DB }
var _ = dbtx.DB
func (r *Repository) mdbCtx(ctx context.Context) *gorm.DB {
    return dbtx.DB(ctx, r.db)
}
`

func TestRepositoryContextOrigins(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"context entry", `return r.mdbCtx(ctx).Find(nil).Error`, ""},
		{"direct binding", `return dbtx.DB(ctx, raw).Save(nil).Error`, ""},
		{"query chain", `return r.mdbCtx(ctx).Where("id = ?", 1).Find(nil).Error`, ""},
		{"local aliases", `q := r.mdbCtx(ctx); alias := q; return alias.Save(nil).Error`, ""},
		{"query reassignment", `q := r.mdbCtx(ctx); q = q.Where("ok"); return q.Find(nil).Error`, ""},
		{"derived context", `ctx, cancel := context.WithTimeout(ctx, 1); defer cancel(); return r.mdbCtx(ctx).Find(nil).Error`, ""},
		{"context alias", `child := context.WithValue(ctx, 1, 2); return r.mdbCtx(child).Find(nil).Error`, ""},
		{"transaction", `return r.mdbCtx(ctx).Transaction(func(tx *gorm.DB) error { return tx.Save(nil).Error })`, ""},
		{"nested transaction", `return r.mdbCtx(ctx).Transaction(func(tx *gorm.DB) error { return tx.Transaction(func(inner *gorm.DB) error { return inner.Save(nil).Error }) })`, ""},
		{"session options", `return r.mdbCtx(ctx).Session(&gorm.Session{NewDB: true}).Find(nil).Error`, ""},
		{"session context", `return r.mdbCtx(ctx).Session(&gorm.Session{Context: ctx}).Find(nil).Error`, ""},
		{"bound WithContext", `return r.mdbCtx(ctx).WithContext(ctx).Find(nil).Error`, ""},
		{"bound method value", `find := r.mdbCtx(ctx).Find; return find(nil).Error`, ""},
		{"comment and string", `// r.db.Find(nil)
            _ = "raw.Save(nil)"; return nil`, ""},
		{"raw field", `return r.db.Find(nil).Error`, "GORM receiver"},
		{"raw parameter", `return raw.Save(nil).Error`, "GORM receiver"},
		{"raw alias", `q := r.db; return q.Find(nil).Error`, "GORM receiver"},
		{"raw transaction", `return raw.Transaction(func(tx *gorm.DB) error { return tx.Find(nil).Error })`, "GORM receiver"},
		{"raw method value", `find := raw.Find; return find(nil).Error`, "GORM receiver"},
		{"raw method expression", `return (*gorm.DB).Find(raw, nil).Error`, "GORM receiver"},
		{"raw WithContext bypasses transaction binding", `return raw.WithContext(ctx).Find(nil).Error`, "GORM receiver"},
		{"overwrite alias", `q := r.mdbCtx(ctx); if raw != nil { q = raw }; return q.Find(nil).Error`, "GORM receiver"},
		{"background entry", `return r.mdbCtx(context.Background()).Find(nil).Error`, "GORM receiver"},
		{"todo entry", `return dbtx.DB(context.TODO(), raw).Find(nil).Error`, "GORM receiver"},
		{"context overwritten", `ctx = context.Background(); return r.mdbCtx(ctx).Find(nil).Error`, "GORM receiver"},
		{"context detached", `return r.mdbCtx(context.WithoutCancel(ctx)).Find(nil).Error`, "GORM receiver"},
		{"reset WithContext", `return r.mdbCtx(ctx).WithContext(context.Background()).Find(nil).Error`, "obscures"},
		{"reset session context", `return r.mdbCtx(ctx).Session(&gorm.Session{Context: context.Background()}).Find(nil).Error`, "obscures"},
		{"unknown session options", `options := &gorm.Session{}; return r.mdbCtx(ctx).Session(options).Find(nil).Error`, "obscures"},
		{"unknown scopes", `return r.mdbCtx(ctx).Scopes(func(_ *gorm.DB) *gorm.DB { return raw }).Find(nil).Error`, "obscures"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			source := repositoryFixture + "func (r *Repository) query(ctx context.Context, raw *gorm.DB) error {\n" + tc.body + "\n}"
			pkg := parseRepositoryFixture(t, fixtureModule+"/internal/repository/example", source)
			findings, err := repositoryContextFindings([]*packages.Package{pkg})
			if err != nil {
				t.Fatal(err)
			}
			joined := strings.Join(findings, "\n")
			if tc.want == "" && joined != "" {
				t.Fatalf("unexpected findings:\n%s", joined)
			}
			if tc.want != "" && !strings.Contains(joined, tc.want) {
				t.Fatalf("findings = %q, want %q", joined, tc.want)
			}
		})
	}
}

func TestRepositoryContextHelperAndTypes(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name   string
		source string
		want   string
	}{
		{"raw helper", strings.Replace(repositoryFixture, "return dbtx.DB(ctx, r.db)", "return r.db", 1), "mdbCtx must return"},
		{"background helper", strings.Replace(repositoryFixture, "dbtx.DB(ctx, r.db)", "dbtx.DB(context.Background(), r.db)", 1), "mdbCtx must return"},
		{"wrong fallback helper", strings.Replace(repositoryFixture, "dbtx.DB(ctx, r.db)", "dbtx.DB(ctx, nil)", 1), "mdbCtx must return"},
		{"unknown origin", repositoryFixture + `
            func unknown(db *gorm.DB) *gorm.DB { return db }
            func (r *Repository) query(ctx context.Context) error { return unknown(r.db).Find(nil).Error }`, "GORM receiver"},
		{"same names different types", repositoryFixture + `
            type Other struct{}
            func (Other) Find(any) error { return nil }
            func (Other) mdbCtx() {}
            func query() error { return Other{}.Find(nil) }`, ""},
		{"import aliases", strings.NewReplacer(
			`"context"`, `requestctx "context"`, `context.`, `requestctx.`,
			`"gorm.io/gorm"`, `orm "gorm.io/gorm"`, `gorm.`, `orm.`,
			`"example.com/service/internal/repository/dbtx"`, `transaction "example.com/service/internal/repository/dbtx"`, `dbtx.`, `transaction.`,
		).Replace(repositoryFixture + `func (r *Repository) query(ctx context.Context) error { return r.mdbCtx(ctx).Find(nil).Error }`), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pkg := parseRepositoryFixture(t, fixtureModule+"/internal/repository/example", tc.source)
			findings, err := repositoryContextFindings([]*packages.Package{pkg})
			if err != nil {
				t.Fatal(err)
			}
			joined := strings.Join(findings, "\n")
			if tc.want == "" && joined != "" || tc.want != "" && !strings.Contains(joined, tc.want) {
				t.Fatalf("findings = %q, want %q", joined, tc.want)
			}
		})
	}
}

func TestRepositoryContextExemptionAndLoadFailure(t *testing.T) {
	t.Parallel()
	pkg := parseRepositoryFixture(t, fixtureModule+"/internal/repository/dbtx", repositoryFixture+`
        func query(raw *gorm.DB) error { return raw.Find(nil).Error }
    `)
	if findings, err := repositoryContextFindings([]*packages.Package{pkg}); err != nil || len(findings) != 0 {
		t.Fatalf("dbtx exemption: findings=%v err=%v", findings, err)
	}
	for _, loaded := range [][]*packages.Package{
		nil,
		{{PkgPath: "broken", Errors: []packages.Error{{Msg: "type check failed"}}}},
		{{PkgPath: "missing type information"}},
	} {
		if _, err := repositoryContextFindings(loaded); err == nil {
			t.Fatal("incomplete package load must fail")
		}
	}
}

// These type fixtures model only the signatures exercised by the guard. They
// perform no I/O and do not stand in for a real database integration test.
func parseRepositoryFixture(t *testing.T, path, source string) *packages.Package {
	t.Helper()
	imports := fixtureImporter{}
	for _, dependency := range []struct{ path, source string }{
		{"context", `package context
            type Context interface { Done() <-chan struct{} }
            func Background() Context { return nil }
            func TODO() Context { return nil }
            func WithoutCancel(Context) Context { return nil }
            func WithValue(Context, any, any) Context { return nil }
            func WithTimeout(Context, int) (Context, func()) { return nil, nil }
        `},
		{"gorm.io/gorm", `package gorm
            import "context"
            type DB struct { Error error }
            type Session struct { Context context.Context; NewDB bool }
            func (db *DB) Find(...any) *DB { return db }
            func (db *DB) Save(any) *DB { return db }
            func (db *DB) Where(any, ...any) *DB { return db }
            func (db *DB) WithContext(context.Context) *DB { return db }
            func (db *DB) Session(*Session) *DB { return db }
            func (db *DB) Scopes(...func(*DB) *DB) *DB { return db }
            func (db *DB) Transaction(func(*DB) error, ...any) error { return nil }
        `},
		{fixtureModule + "/internal/repository/dbtx", `package dbtx
            import ("context"; "gorm.io/gorm")
            func DB(context.Context, *gorm.DB) *gorm.DB { return nil }
        `},
	} {
		pkg := typeFixture(t, imports, dependency.path, dependency.source)
		imports[dependency.path] = pkg.Types
	}
	return typeFixture(t, imports, path, source)
}

type fixtureImporter map[string]*types.Package

func (f fixtureImporter) Import(path string) (*types.Package, error) {
	if pkg, ok := f[path]; ok {
		return pkg, nil
	}
	return nil, fmt.Errorf("unexpected fixture import %s", path)
}

func typeFixture(t *testing.T, imports fixtureImporter, path, source string) *packages.Package {
	t.Helper()
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "fixture.go", source, 0)
	if err != nil {
		t.Fatal(err)
	}
	info := &types.Info{
		Types: make(map[ast.Expr]types.TypeAndValue), Defs: make(map[*ast.Ident]types.Object),
		Uses: make(map[*ast.Ident]types.Object), Selections: make(map[*ast.SelectorExpr]*types.Selection),
	}
	config := types.Config{Importer: imports}
	checked, err := config.Check(path, fset, []*ast.File{file}, info)
	if err != nil {
		t.Fatal(err)
	}
	return &packages.Package{
		PkgPath: path, Fset: fset, Syntax: []*ast.File{file}, Types: checked, TypesInfo: info,
		Module: &packages.Module{Path: fixtureModule},
	}
}
