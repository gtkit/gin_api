//go:build ignore

// check-package-vars.go —— 包级变量**重新绑定**检查。
//
// 判据准确表述为「禁止包级变量被重新绑定」：即 `pkgVar = …` 形式的赋值出现在
// `init()` 之外即失败。
//
// **必须承认它的能力边界，不得声称它覆盖"所有可变全局状态"**：
// 它检测不到 `pkgMap[k] = v` 的 map 写入、slice 元素修改、经指针修改被指对象、
// 以及在包级对象上调用会改变其内部状态的方法。这三类靠 code review 与
// 「包级变量一律只读」的约定兜底。
//
// 因此本检查命名为「重新绑定检查」而不是「可变全局检查」——
// 后者会给出虚假的完整性保证，让人以为全局状态问题已经被自动化解决了。
//
// 不可变查找表（如正则、常量表）不在治理范围内：判据按"是否被重新赋值"区分，
// 不按 var/const 区分。
package main

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func main() {
	if err := run(); err != nil {
		if _, writeErr := fmt.Fprintln(os.Stderr, err); writeErr != nil {
			os.Exit(2)
		}
		os.Exit(1)
	}
}

func run() error {
	roots := os.Args[1:]
	if len(roots) == 0 {
		roots = []string{"internal", "cmd"}
	}

	var findings []string
	for _, root := range roots {
		err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			f, e := parseFindings(path)
			if e != nil {
				return e
			}
			findings = append(findings, f...)
			return nil
		})
		if err != nil {
			return fmt.Errorf("FAIL: walking %s: %w", root, err)
		}
	}

	allowed, err := loadAllowlist("scripts/package-vars-allowlist.txt")
	if err != nil {
		return fmt.Errorf("FAIL: %w", err)
	}

	var unlisted []string
	for _, f := range findings {
		if !allowed[allowKey(f)] {
			unlisted = append(unlisted, f)
		}
	}

	if len(unlisted) > 0 {
		sort.Strings(unlisted)
		if _, writeErr := fmt.Println("FAIL: package-level variables must not be rebound outside init()"); writeErr != nil {
			return fmt.Errorf("write tool output: %w", writeErr)
		}
		if _, writeErr := fmt.Println("  Either remove the rebinding, or claim it in scripts/package-vars-allowlist.txt"); writeErr != nil {
			return fmt.Errorf("write tool output: %w", writeErr)
		}
		if _, writeErr := fmt.Println("  with an explicit owner and reason."); writeErr != nil {
			return fmt.Errorf("write tool output: %w", writeErr)
		}
		for _, f := range unlisted {
			if _, writeErr := fmt.Println(" ", f); writeErr != nil {
				return fmt.Errorf("write tool output: %w", writeErr)
			}
		}
		return errors.New("package-var check failed")
	}
	if _, writeErr := fmt.Printf("package-var rebinding check OK (%d claimed sites)\n", len(allowed)); writeErr != nil {
		return fmt.Errorf("write tool output: %w", writeErr)
	}
	return nil
}

// allowKey 由 findings 行提取 "文件:变量" 作为登记键。
//
// 刻意不含行号：行号会随无关改动漂移，让登记表变成需要频繁维护的噪声，
// 而一个总在报假警的门禁最终会被整体放宽。
func allowKey(finding string) string {
	// finding 形如 "path:line: name is rebound in func X"
	parts := strings.SplitN(finding, ":", 3)
	if len(parts) < 3 {
		return finding
	}
	rest := strings.TrimSpace(parts[2])
	name, _, _ := strings.Cut(rest, " ")
	return parts[0] + ":" + name
}

func loadAllowlist(path string) (map[string]bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]bool{}, nil
		}
		return nil, fmt.Errorf("read allowlist: %w", err)
	}
	allowed := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		entry, comment, _ := strings.Cut(line, "#")
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		// 每条登记必须写明所有者与理由，否则它只是一个没人负责的豁免。
		if strings.TrimSpace(comment) == "" {
			return nil, fmt.Errorf("allowlist entry %q has no owner/reason comment", entry)
		}
		allowed[entry] = true
	}
	return allowed, nil
}

func parseFindings(path string) ([]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	// 收集本文件声明的包级变量名。
	pkgVars := map[string]bool{}
	for _, decl := range file.Decls {
		gd, ok := decl.(*ast.GenDecl)
		if !ok || gd.Tok != token.VAR {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for _, name := range vs.Names {
				pkgVars[name.Name] = true
			}
		}
	}
	if len(pkgVars) == 0 {
		return nil, nil
	}

	var findings []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}
		// init() 是设置包级状态的唯一合法位置。
		if fn.Recv == nil && fn.Name.Name == "init" {
			continue
		}

		// 函数内被 := 或参数遮蔽的同名局部变量不算重新绑定包级变量。
		shadowed := collectShadowed(fn)

		ast.Inspect(fn.Body, func(n ast.Node) bool {
			assign, ok := n.(*ast.AssignStmt)
			if !ok || assign.Tok == token.DEFINE {
				return true
			}
			for _, lhs := range assign.Lhs {
				ident, ok := lhs.(*ast.Ident)
				if !ok {
					continue
				}
				if pkgVars[ident.Name] && !shadowed[ident.Name] {
					pos := fset.Position(ident.Pos())
					// 路径归一为正斜杠：登记表里写的是可移植形式（cmd/registry.go），
					// 而 filepath.Walk 在 Windows 上给出 cmd\registry.go。不归一的话
					// 每一条登记在该平台都匹配不上，传感器把**已认领**的重绑定报成
					// 未认领——失败信息指向登记表，而登记表看起来完全正确。
					// 输出与匹配用同一份字符串，报出来的行可以直接抄进登记表。
					findings = append(findings, fmt.Sprintf(
						"%s:%d: %s is rebound in %s",
						filepath.ToSlash(path), pos.Line, ident.Name, describeFunc(fn)))
				}
			}
			return true
		})
	}
	return findings, nil
}

// collectShadowed 收集函数内可能遮蔽包级变量的名字（参数、返回值、:= 定义）。
//
// 宁可漏报也不误报：一条永远为真的规则最终只会被人加豁免绕过。
func collectShadowed(fn *ast.FuncDecl) map[string]bool {
	shadowed := map[string]bool{}
	addFields := func(fl *ast.FieldList) {
		if fl == nil {
			return
		}
		for _, f := range fl.List {
			for _, name := range f.Names {
				shadowed[name.Name] = true
			}
		}
	}
	addFields(fn.Type.Params)
	addFields(fn.Type.Results)
	addFields(fn.Recv)

	ast.Inspect(fn.Body, func(n ast.Node) bool {
		if assign, ok := n.(*ast.AssignStmt); ok && assign.Tok == token.DEFINE {
			for _, lhs := range assign.Lhs {
				if ident, ok := lhs.(*ast.Ident); ok {
					shadowed[ident.Name] = true
				}
			}
		}
		if gd, ok := n.(*ast.GenDecl); ok && gd.Tok == token.VAR {
			for _, spec := range gd.Specs {
				if vs, ok := spec.(*ast.ValueSpec); ok {
					for _, name := range vs.Names {
						shadowed[name.Name] = true
					}
				}
			}
		}
		return true
	})
	return shadowed
}

func describeFunc(fn *ast.FuncDecl) string {
	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		return "method " + fn.Name.Name
	}
	return "func " + fn.Name.Name
}
