package tenant

import (
	"context"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestFrom 覆盖取值的全部输入域：正常标识、空标识、未设置、nil ctx。
//
// 空标识单列一条是因为它是**最危险**的那个取值：把它当身份用会注入
// 判别列等于空串的条件，既不报错也不命中正常数据——看起来隔离在工作，
// 实际每次查询都查了一个不存在的租户。
func TestFrom(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		ctx    func() context.Context
		wantID ID
		wantOK bool
	}{
		{
			name:   "普通标识往返",
			ctx:    func() context.Context { return With(context.Background(), "acme") },
			wantID: "acme",
			wantOK: true,
		},
		{
			name:   "数字形态的标识同样是不透明身份",
			ctx:    func() context.Context { return With(context.Background(), "10086") },
			wantID: "10086",
			wantOK: true,
		},
		{
			name:   "空标识不是身份",
			ctx:    func() context.Context { return With(context.Background(), "") },
			wantID: "",
			wantOK: false,
		},
		{
			name:   "未设置",
			ctx:    context.Background,
			wantID: "",
			wantOK: false,
		},
		{
			name:   "nil ctx 不 panic",
			ctx:    func() context.Context { return nil },
			wantID: "",
			wantOK: false,
		},
		{
			name: "后写覆盖先写",
			ctx: func() context.Context {
				return With(With(context.Background(), "first"), "second")
			},
			wantID: "second",
			wantOK: true,
		},
		{
			name: "覆盖成空标识后不再是身份",
			ctx: func() context.Context {
				return With(With(context.Background(), "acme"), "")
			},
			wantID: "",
			wantOK: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotID, gotOK := From(tc.ctx())
			if gotID != tc.wantID || gotOK != tc.wantOK {
				t.Fatalf("From() = (%q, %v), want (%q, %v)", gotID, gotOK, tc.wantID, tc.wantOK)
			}
		})
	}
}

// TestIsBypass 覆盖绕过标记的输入域。
func TestIsBypass(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		ctx  func() context.Context
		want bool
	}{
		{"未标记", context.Background, false},
		{"已标记", func() context.Context { return WithBypass(context.Background()) }, true},
		{"nil ctx 不 panic", func() context.Context { return nil }, false},
		{
			name: "绕过与身份互不干扰",
			ctx:  func() context.Context { return WithBypass(With(context.Background(), "acme")) },
			want: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := IsBypass(tc.ctx()); got != tc.want {
				t.Fatalf("IsBypass() = %v, want %v", got, tc.want)
			}
		})
	}
}

// TestBypassDoesNotEraseIdentity 钉住绕过标记不会顺手清掉身份。
//
// 两者用不同的 key 是刻意的：dataisolate 需要区分「没有身份」与「要求绕过
// 但绕过被禁用」——后者必须给出点名安全闸的错误，否则写了绕过的人只会
// 收到「没有身份」，无从知道是闸门拦下的。若绕过标记覆盖了身份，
// 这个区分就没了。
func TestBypassDoesNotEraseIdentity(t *testing.T) {
	t.Parallel()

	ctx := WithBypass(With(context.Background(), "acme"))

	id, ok := From(ctx)
	if !ok || id != "acme" {
		t.Fatalf("From() after WithBypass = (%q, %v), want (\"acme\", true)", id, ok)
	}
	if !IsBypass(ctx) {
		t.Fatal("IsBypass() = false after WithBypass")
	}
}

// TestIdentityIsNotReachableByForgedKey 钉住上下文键无法被外部伪造。
//
// 键是私有空结构体类型，因此别的包即使知道名字也造不出同一个键。
// 用字符串键写入同名值不应被 From 采信——否则任何能写 ctx 的代码
// 都能把查询切到任意租户，而 With 的「唯一入口」就不成立了。
func TestIdentityIsNotReachableByForgedKey(t *testing.T) {
	t.Parallel()

	//nolint:staticcheck // 故意用字符串键：本用例证明它取不到值
	ctx := context.WithValue(context.Background(), "tenant.idKey{}", ID("attacker"))

	if id, ok := From(ctx); ok {
		t.Fatalf("From() honored a forged string key: got %q", id)
	}
}

// TestPackageDependsOnStdlibOnly 让「本包是纯基础包」成为判据而非约定。
//
// docs/architecture.md 规定纯基础包任何层都可 import，前提是它不依赖 gin/gorm/redis
// 这类基础设施。而这一条只写在文档里就会被无声违反：某天有人为了省事在这里
// import gorm 取个常量，包的可见范围就变了，而编译、vet 与全部测试都不会有异常。
func TestPackageDependsOnStdlibOnly(t *testing.T) {
	t.Parallel()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("read package dir: %v", err)
	}

	fset := token.NewFileSet()
	scanned := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		file, parseErr := parser.ParseFile(fset, name, nil, parser.ImportsOnly)
		if parseErr != nil {
			t.Fatalf("parse %s: %v", name, parseErr)
		}
		scanned++
		for _, imp := range file.Imports {
			path, unquoteErr := strconv.Unquote(imp.Path.Value)
			if unquoteErr != nil {
				t.Fatalf("unquote import in %s: %v", name, unquoteErr)
			}
			// 标准库路径的第一段不含点号（域名一定含点）。
			first, _, _ := strings.Cut(path, "/")
			if strings.Contains(first, ".") {
				t.Errorf("%s imports %q; this package must depend on stdlib only "+
					"so that every layer may import it", name, path)
			}
		}
	}

	// 扫到 0 个文件同样是失败：那说明枚举坏了，而它的表现与「全部合规」一致。
	if scanned == 0 {
		t.Fatalf("scanned no non-test .go files in %s; the enumeration is broken", mustAbs(t))
	}
}

func mustAbs(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs(".")
	if err != nil {
		return "."
	}
	return abs
}
