//go:build ignore

// check-loopback-tests.go —— 绑定回环端口的测试必须自行声明该依赖。
//
// 判据：任何**直接**调用 `net.Listen` / `net.Dial` / `httptest.NewServer` /
// `httptest.NewUnstartedServer` 的测试函数或测试 helper，函数体内必须出现
// `testutil.RequireLoopback` 调用。
//
// 为什么规则定在"直接包含该调用的函数"这一层：这样无需跨函数分析——
// helper 自己声明了，所有用它的测试就都被覆盖。跨调用链推导会把检查器
// 变成一个不完整的静态分析器，而不完整的分析给出的是虚假保证。
//
// **必须承认能力边界**：它检测不到经由变量间接调用（`f := net.Listen; f(...)`）、
// 反射调用、或第三方库内部的端口绑定。这些靠 code review 与"新增端口依赖时
// 照着旁边写"的约定兜底。命名为「直接绑定检查」而不是「端口依赖检查」，
// 后者会让人以为所有端口依赖都已被自动发现。
//
// 存在的理由：此前受限环境靠 `scripts/test-coverage.sh` 维护测试名黑名单来猜
// 哪些测试绑端口。新增测试时脚本无从知晓，受限环境因此静默失效——
// 只修当前这些点而不装防线，下一个新测试就会重新打破它。
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// bindingCalls 是会占用本地端口的调用。
//
// 只列**直接形成端口依赖**的那些：`httptest.NewRecorder` 不在其中，
// 它完全在内存里完成，把它列进来会让大量无关测试被迫声明依赖，
// 而那种噪音最终会让整条规则被绕过。
var bindingCalls = map[string]string{
	"net.Listen":                  "binds a local port",
	"net.ListenPacket":            "binds a local port",
	"net.Dial":                    "connects to a local port",
	"httptest.NewServer":          "starts a listening server",
	"httptest.NewUnstartedServer": "prepares a listening server",
	"httptest.NewTLSServer":       "starts a listening TLS server",
}

// requireCall 是声明该依赖的方式。
const requireCall = "testutil.RequireLoopback"

func main() {
	roots := os.Args[1:]
	if len(roots) == 1 && roots[0] == "--self-test" {
		runSelfTest()
		return
	}
	if len(roots) == 0 {
		roots = []string{"internal", "cmd"}
	}

	var findings []string
	for _, root := range roots {
		if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
			if err != nil {
				return err
			}
			if info.IsDir() || !strings.HasSuffix(path, "_test.go") {
				return nil
			}
			found, err := inspectFile(path)
			if err != nil {
				return err
			}
			findings = append(findings, found...)
			return nil
		}); err != nil {
			fmt.Fprintf(os.Stderr, "walk %s: %v\n", root, err)
			os.Exit(2)
		}
	}

	if len(findings) == 0 {
		fmt.Println("loopback test check OK")
		return
	}

	sort.Strings(findings)
	fmt.Fprintln(os.Stderr, "FAIL: these test functions take a local port without declaring it:")
	for _, f := range findings {
		fmt.Fprintln(os.Stderr, "  "+f)
	}
	fmt.Fprintf(os.Stderr, "\nAdd %s(t) as the first statement, so the restricted sandbox skips it\n",
		requireCall)
	fmt.Fprintln(os.Stderr, "instead of failing. A build-script name list cannot stay in sync with new tests.")
	os.Exit(1)
}

// inspectFile 检查单个测试文件。
func inspectFile(path string) ([]string, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}

	var findings []string
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Body == nil {
			continue
		}

		binding, at := findBindingCall(fn.Body)
		if binding == "" || declaresRequirement(fn.Body) {
			continue
		}
		findings = append(findings, fmt.Sprintf("%s:%d %s calls %s, which %s",
			path, fset.Position(at).Line, fn.Name.Name, binding, bindingCalls[binding]))
	}
	return findings, nil
}

// findBindingCall 返回函数体内第一个端口绑定调用及其位置。
func findBindingCall(body *ast.BlockStmt) (string, token.Pos) {
	var name string
	var pos token.Pos

	ast.Inspect(body, func(n ast.Node) bool {
		if name != "" {
			return false
		}
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		if qualified := qualifiedName(call.Fun); bindingCalls[qualified] != "" {
			name, pos = qualified, call.Pos()
			return false
		}
		return true
	})
	return name, pos
}

// declaresRequirement 报告函数体内是否声明了回环端口依赖。
func declaresRequirement(body *ast.BlockStmt) bool {
	var declared bool
	ast.Inspect(body, func(n ast.Node) bool {
		if declared {
			return false
		}
		if call, ok := n.(*ast.CallExpr); ok && qualifiedName(call.Fun) == requireCall {
			declared = true
			return false
		}
		return true
	})
	return declared
}

// qualifiedName 把 `pkg.Fn` 形式的调用还原成字符串。
//
// 只认包限定调用：本地同名函数（`func Listen()`）不该被当成 net.Listen，
// 把它算进来会产生误报，而误报会让整条规则被加豁免绕过。
func qualifiedName(fun ast.Expr) string {
	sel, ok := fun.(*ast.SelectorExpr)
	if !ok {
		return ""
	}
	ident, ok := sel.X.(*ast.Ident)
	if !ok {
		return ""
	}
	return ident.Name + "." + sel.Sel.Name
}

// runSelfTest 证明这条规则**真的会触发**，也**真的不会误报**。
//
// 没有负向证明的检查器等于没有检查器：一条永远不报警的规则与删掉它没有区别，
// 而它的存在会让人以为这一面已经被守住了。只有负向证明而没有反向证明则会
// 漏掉误报，而误报的规则最终会被加豁免绕过。
//
// 在一次性临时目录里造 fixture，因此完全不触碰真实仓库。
func runSelfTest() {
	dir, err := os.MkdirTemp("", "loopback-selftest")
	if err != nil {
		fmt.Fprintf(os.Stderr, "self-test: %v\n", err)
		os.Exit(2)
	}
	defer os.RemoveAll(dir)

	cases := []struct {
		name        string
		source      string
		wantFinding bool
	}{
		{
			name: "an undeclared binding test is caught",
			source: `package x
func TestBinds(t *testing.T) {
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	_ = l
}`,
			wantFinding: true,
		},
		{
			name: "an undeclared helper is caught",
			source: `package x
func startServer(t *testing.T) {
	srv := httptest.NewServer(nil)
	_ = srv
}`,
			wantFinding: true,
		},
		{
			name: "a declared test passes",
			source: `package x
func TestBinds(t *testing.T) {
	testutil.RequireLoopback(t)
	l, _ := net.Listen("tcp", "127.0.0.1:0")
	_ = l
}`,
		},
		{
			// 反向证明：内存 recorder 不该被要求声明端口依赖。
			// 误报会让大量无关测试被迫加一行，而那种噪音最终会让规则被绕过。
			name: "an in-memory recorder is not flagged",
			source: `package x
func TestRecorder(t *testing.T) {
	rec := httptest.NewRecorder()
	_ = rec
}`,
		},
		{
			// 本地同名函数不该被当成 net.Listen。
			name: "a local function with the same name is not flagged",
			source: `package x
func TestLocal(t *testing.T) {
	Listen("tcp")
}`,
		},
	}

	failed := false
	for i, tc := range cases {
		path := filepath.Join(dir, fmt.Sprintf("case%d_test.go", i))
		if err := os.WriteFile(path, []byte(tc.source), 0o600); err != nil {
			fmt.Fprintf(os.Stderr, "self-test write: %v\n", err)
			os.Exit(2)
		}
		findings, err := inspectFile(path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[self-test] FAIL: %s => parse error: %v\n", tc.name, err)
			failed = true
			continue
		}
		if got := len(findings) > 0; got != tc.wantFinding {
			fmt.Fprintf(os.Stderr, "[self-test] FAIL: %s => finding=%v, want %v\n",
				tc.name, got, tc.wantFinding)
			failed = true
			continue
		}
		fmt.Printf("[self-test] OK: %s\n", tc.name)
	}

	if failed {
		os.Exit(1)
	}
	fmt.Println("[self-test] OK: the rule fires on real gaps and stays quiet on safe code")
}
