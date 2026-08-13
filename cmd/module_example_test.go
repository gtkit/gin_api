//go:build scaffold

package cmd

import (
	"strings"
	"testing"
)

// TestModuleCommandExamplesSatisfyAccessContract 钉住示例的可执行性：
// --access/--public 严格二选一且无默认值，示例缺了它照抄即报错——
// 那会把"必须做安全决策"变成一个看起来像使用故障的错误。
func TestModuleCommandExamplesSatisfyAccessContract(t *testing.T) {
	t.Parallel()

	cmd := newModuleCmd(nil)
	lines := strings.Split(cmd.Example, "\n")
	var checked int
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		checked++
		if !strings.Contains(line, "--access") && !strings.Contains(line, "--public") {
			t.Fatalf("example %q lacks the mandatory --access/--public choice", line)
		}
	}
	if checked == 0 {
		t.Fatal("module command has no examples to check")
	}
}
