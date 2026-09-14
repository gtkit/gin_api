//go:build scaffold

package scaffold

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// markerPattern 匹配母版源码里的所有 scaffold 标记。
var markerPattern = regexp.MustCompile(`//\s*(scaffold:[a-z-]+)`)

// TestEveryProvidersMarkerIsClaimed 钉住 providers.go 里每个标记都有归属。
//
// 缺陷背景：`scaffold:module-authorizers` 曾既不在注入集、也不在校验集里——
// 它看起来是一个接线点，实际不参与任何流程。后果不是少一个功能，而是**依赖它的
// 功能静默不生效**：生成器提示里写着"授权实现已注册"，而那个注册从未发生，
// 母版的分阶段装配结构被破坏时也不会有任何信号。
//
// 因此这里不断言"标记有几个"，而断言"每个都被认领"：新增标记时若忘了登记，
// 本用例失败并指出是哪一个。
func TestEveryProvidersMarkerIsClaimed(t *testing.T) {
	t.Parallel()

	root := repoRoot(t)
	source, err := os.ReadFile(filepath.Join(root, "internal", "bootstrap", "providers.go"))
	if err != nil {
		t.Fatalf("读取 providers.go: %v", err)
	}

	imports, serviceFields, services, modules := providersMarkers()
	claimed := []string{imports, serviceFields, services, modules}
	claimed = append(claimed, providersValidateOnlyMarkers()...)

	claimedNames := make([]string, 0, len(claimed))
	for _, c := range claimed {
		if m := markerPattern.FindStringSubmatch(c); m != nil {
			claimedNames = append(claimedNames, m[1])
		}
	}

	found := map[string]bool{}
	for _, m := range markerPattern.FindAllStringSubmatch(string(source), -1) {
		found[m[1]] = true
	}
	if len(found) == 0 {
		t.Fatal("providers.go 里没有找到任何 scaffold 标记，测试前提不成立")
	}

	for name := range found {
		if !slices.Contains(claimedNames, name) {
			t.Errorf("标记 %q 出现在 providers.go 里，但既不在注入集也不在校验集——"+
				"它是一个看起来是接线点、实际不参与任何流程的裸注释；"+
				"请把它登记进 providersMarkers 或 providersValidateOnlyMarkers", name)
		}
	}

	// 反向：登记了却不存在的标记同样是缺陷——生成器会在校验阶段中止。
	for _, name := range claimedNames {
		if !found[name] {
			t.Errorf("标记 %q 已被生成器认领，但 providers.go 里不存在", name)
		}
	}
}

// repoRoot 定位仓库根目录。
func repoRoot(t *testing.T) string {
	t.Helper()

	out, err := exec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Skipf("git rev-parse 不可用，跳过：%v", err)
	}
	return strings.TrimSpace(string(out))
}
