package cmd

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// tableNameDecl 匹配 TableName 方法声明并取出类型名，**覆盖四种接收者形态**：
//
//	func (u *User) TableName() string    有名指针
//	func (*User) TableName() string      匿名指针  ← 脚手架模板生成的正是这种
//	func (u User) TableName() string     有名值
//	func (User) TableName() string       匿名值
//
// 此前只匹配第一种，于是对**脚手架自己生成**的模型完全失明：
// internal/scaffold/module_templates.go 产出的是 `func (*T) TableName()`。
// 也就是说判据声称要防的"新增一张表时漏注册"，在 `make module` 这条最常见的
// 路径上提供零保护——它当时看起来有效，只是因为仓内 5 个模型恰好都用有名指针。
var tableNameDecl = regexp.MustCompile(
	`func \(\s*(?:[A-Za-z_][A-Za-z0-9_]*\s+)?\*?([A-Za-z][A-Za-z0-9_]*)\s*\) TableName\(\) string`)

// TestEveryManagedModelIsRegisteredForDDL 是"模型注册表必须覆盖全部受管模型"的枚举判据。
//
// 漏注册没有任何错误信号：DDL 生成看不到那个模型，`make ddl diff` 的漂移检测也
// 看不到它。于是改动该模型的列宽、索引或可空性时，模型与库会静默分叉；而按
// database/README.md 重新生成基线时，产出会缺那张表。
//
// 这类缺陷需要枚举判据才能被判出来，靠人记不住：漏注册发生在"新增一张表"这个
// 动作里，而执行那个动作的人正是最不可能想起还有一处注册表要改的人。仓内此前
// 没有这条判据，于是连续两次新增表（user_roles、operation_audit_logs）都漏了
// 注册，而门禁一直全绿。
//
// 判据只看 WithDDLModels 的注册块，不看整份文件：模型名也会出现在 import 与
// 注释里，用整份文件做包含判断会把漏注册直接掩盖掉。
func TestEveryManagedModelIsRegisteredForDDL(t *testing.T) {
	t.Parallel()

	managed := managedModelNames(t)
	if len(managed) == 0 {
		t.Fatal("未从 internal/models 找到任何带 TableName 的模型：判据自身失效了")
	}

	block := ddlModelsBlock(t)

	var missing []string
	for _, name := range managed {
		// 精确到 `&models.T{}`，避免 `User` 命中 `UserProfile` 这类前缀重叠。
		if !strings.Contains(block, "&models."+name+"{}") {
			missing = append(missing, name)
		}
	}

	if len(missing) > 0 {
		sort.Strings(missing)
		t.Fatalf("这些受管模型未注册进 WithDDLModels: %s\n"+
			"后果是静默的：DDL 生成与 `make ddl diff` 的漂移检测都会跳过它们，"+
			"改动其列宽/索引/可空性不会有任何提示，重新生成基线时产出也会缺表。\n"+
			"注册块当前内容:\n%s", strings.Join(missing, ", "), block)
	}
}

// managedModelNames 扫出 internal/models 下全部实现 TableName 的类型名。
func managedModelNames(t *testing.T) []string {
	t.Helper()

	dir := filepath.Join("..", "internal", "models")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读取 %s: %v", dir, err)
	}

	var names []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			// 子目录不扫，但**不静默跳过**：模型放进子包时本判据看不见它，
			// 而看不见等于没有保护。把盲区变成显式失败，由人决定怎么扩展判据。
			t.Fatalf("internal/models 出现子目录 %q：本判据只扫该目录本层，"+
				"子包里的模型不会被检查——请扩展判据或说明该子目录不含受管模型", name)
		}
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("读取 %s: %v", name, err)
		}
		for _, m := range tableNameDecl.FindAllStringSubmatch(string(body), -1) {
			names = append(names, m[1])
		}
	}
	sort.Strings(names)
	return names
}

// ddlModelsBlock 截出 cmd/migrate.go 里 WithDDLModels(...) 的实参块。
func ddlModelsBlock(t *testing.T) string {
	t.Helper()

	body, err := os.ReadFile("migrate.go")
	if err != nil {
		t.Fatalf("读取 migrate.go: %v", err)
	}
	src := string(body)

	const marker = "migrate.WithDDLModels("
	start := strings.Index(src, marker)
	if start < 0 {
		t.Fatal("migrate.go 里找不到 migrate.WithDDLModels(：判据的锚点没了，" +
			"改名时必须同步改这里，否则判据会静默失效")
	}
	rest := src[start+len(marker):]
	end := strings.Index(rest, "\n\t\t)")
	if end < 0 {
		t.Fatal("找不到 WithDDLModels 实参块的结尾")
	}
	return rest[:end]
}
