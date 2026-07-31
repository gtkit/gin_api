package projectinit

import (
	"maps"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRewriteIdentityConsts(t *testing.T) {
	t.Parallel()

	const src = `package appmeta

const (
	CommandName     = "app_client"
	RuntimeName     = " Client"
	ProjectName     = "go-gin-api"
	MigrateLockName = "app_client_migrate"
)
`
	got := rewriteIdentityConsts(src, "my-service")

	tests := []struct {
		name string
		want string
	}{
		{name: "command", want: `CommandName     = "my-service"`},
		{name: "runtime", want: `RuntimeName     = "my-service"`},
		{name: "project", want: `ProjectName     = "my-service"`},
		{name: "migrate lock sanitized", want: `MigrateLockName = "my_service_migrate"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !strings.Contains(got, tt.want) {
				t.Fatalf("rewritten source missing %q:\n%s", tt.want, got)
			}
		})
	}
	if strings.Contains(got, "app_client") {
		t.Fatalf("rewritten source still contains old identity:\n%s", got)
	}
}

func TestRunStandardInstantiation(t *testing.T) {
	t.Parallel()

	root := newFixture(t, "example.com/newproj")
	init := newStubbedInitializer(root)

	result, err := init.Run(Options{Name: "my_service"})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	// appmeta 身份已重写。
	meta := readFile(t, filepath.Join(root, "internal", "appmeta", "meta.go"))
	if !strings.Contains(meta, `CommandName     = "my_service"`) {
		t.Fatalf("appmeta identity not rewritten:\n%s", meta)
	}
	if strings.Contains(meta, "app_client") {
		t.Fatalf("appmeta still contains old identity:\n%s", meta)
	}

	// 未被逐一列举为删除目标的文件必须原样保留（中性哨兵，见 sentinelFiles）。
	assertSentinelsIntact(t, root)
	if len(result.Deleted) != 0 {
		t.Fatalf("initializer deleted files it must not touch: %v", result.Deleted)
	}

	// 覆盖率地板已重置。
	mk := readFile(t, filepath.Join(root, "Makefile"))
	if !strings.Contains(mk, "COVERAGE_MIN ?= "+starterCoverageMin) {
		t.Fatalf("coverage floor not reset:\n%s", mk)
	}

	// 提示手动移除 Laravel 层。
	if len(result.Notes) == 0 {
		t.Fatal("expected a legacy-removal note")
	}
}

func TestRunDryRunWritesNothing(t *testing.T) {
	t.Parallel()

	root := newFixture(t, "example.com/newproj")
	metaBefore := readFile(t, filepath.Join(root, "internal", "appmeta", "meta.go"))
	mkBefore := readFile(t, filepath.Join(root, "Makefile"))

	init := newStubbedInitializer(root)
	// dry-run 不应触发 git 重置/自检；若被调用则 t.Fatal。
	init.gitReinit = func(string) error { t.Fatal("gitReinit called in dry-run"); return nil }
	init.selfCheck = func(string) error { t.Fatal("selfCheck called in dry-run"); return nil }

	result, err := init.Run(Options{Name: "my_service", DryRun: true})
	if err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if !result.DryRun {
		t.Fatal("result.DryRun = false, want true")
	}
	if len(result.Rewritten) == 0 {
		t.Fatal("dry-run should still report planned rewrites")
	}

	if got := readFile(t, filepath.Join(root, "internal", "appmeta", "meta.go")); got != metaBefore {
		t.Fatal("dry-run modified appmeta")
	}
	if got := readFile(t, filepath.Join(root, "Makefile")); got != mkBefore {
		t.Fatal("dry-run modified Makefile")
	}
	assertSentinelsIntact(t, root)
}

func TestRunRefusesDirtyTree(t *testing.T) {
	t.Parallel()

	root := newFixture(t, "example.com/newproj")
	init := newStubbedInitializer(root)
	init.workspaceClean = func(string) (bool, error) { return false, nil }

	_, err := init.Run(Options{Name: "my_service"})
	if err == nil || !strings.Contains(err.Error(), "uncommitted") {
		t.Fatalf("expected dirty-tree refusal, got %v", err)
	}
}

func TestRunRefusesScaffoldMother(t *testing.T) {
	t.Parallel()

	root := newFixture(t, motherModulePath)
	init := newStubbedInitializer(root)

	_, err := init.Run(Options{Name: "my_service"})
	if err == nil || !strings.Contains(err.Error(), "scaffold mother") {
		t.Fatalf("expected scaffold-mother refusal, got %v", err)
	}
}

func TestRunRequiresName(t *testing.T) {
	t.Parallel()

	root := newFixture(t, "example.com/newproj")
	init := newStubbedInitializer(root)

	if _, err := init.Run(Options{Name: "  "}); err == nil {
		t.Fatal("expected error for empty name")
	}
}

func TestRunIsIdempotent(t *testing.T) {
	t.Parallel()

	root := newFixture(t, "example.com/newproj")
	init := newStubbedInitializer(root)

	if _, err := init.Run(Options{Name: "my_service"}); err != nil {
		t.Fatalf("first Run returned error: %v", err)
	}
	second, err := init.Run(Options{Name: "my_service"})
	if err != nil {
		t.Fatalf("second Run returned error: %v", err)
	}
	if len(second.Rewritten) != 0 || len(second.Deleted) != 0 {
		t.Fatalf("second run not idempotent: rewritten=%v deleted=%v", second.Rewritten, second.Deleted)
	}
}

// --- helpers ---

// sentinelFiles 是中性哨兵：它们代表「派生项目作者自己拥有、initializer 绝不该碰」的文件。
//
// 刻意覆盖三种形态：根级文档、嵌套目录中的文档、以及非 Markdown 文件——因为最容易出现的
// 误删规则正是「删掉所有非白名单的根级 .md」，那会连派生项目作者自己的 README 补充文档一起删掉。
//
// 名称必须保持中性：判据里一旦出现具体开发工具的目录名/文件名，检查规则本身就成了该工具链的标识。
var sentinelFiles = map[string]string{
	"NOTES.md":                  "# project notes\n\nlocal-only sentinel\n",
	"docs/design/overview.md":   "# design overview\n\nnested sentinel\n",
	"docs/design/data.json":     "{\"sentinel\":true}\n",
	"internal/keep/keep_me.txt": "sentinel inside internal\n",
}

// assertSentinelsIntact 断言全部中性哨兵仍存在且内容逐字节未变。
func assertSentinelsIntact(t *testing.T, root string) {
	t.Helper()
	for name, want := range sentinelFiles {
		full := filepath.Join(root, filepath.FromSlash(name))
		got, err := os.ReadFile(full)
		if err != nil {
			t.Fatalf("sentinel %s missing after run: %v", name, err)
		}
		if string(got) != want {
			t.Fatalf("sentinel %s modified:\ngot  %q\nwant %q", name, got, want)
		}
	}
}

func newStubbedInitializer(root string) *Initializer {
	return &Initializer{
		root:           root,
		workspaceClean: func(string) (bool, error) { return true, nil },
		gitReinit:      func(string) error { return nil },
		selfCheck:      func(string) error { return nil },
	}
}

func newFixture(t *testing.T, modulePath string) string {
	t.Helper()

	root := t.TempDir()
	files := map[string]string{
		"go.mod": "module " + modulePath + "\n\ngo 1.26\n",
		"internal/appmeta/meta.go": `package appmeta

const (
	CommandName     = "app_client"
	RuntimeName     = " Client"
	ProjectName     = "go-gin-api"
	MigrateLockName = "app_client_migrate"
)
`,
		"Makefile": "COVERAGE_MIN ?= 57.0\n\ncheck:\n\t@echo check\n",
	}
	// 中性哨兵：验证 initializer 不删除未被逐一列举为删除目标的文件。
	maps.Copy(files, sentinelFiles)
	for name, content := range files {
		full := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatalf("MkdirAll: %v", err)
		}
		if err := os.WriteFile(full, []byte(content), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}
	}
	return root
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(%s): %v", path, err)
	}
	return string(b)
}
