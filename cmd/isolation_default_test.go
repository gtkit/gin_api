//go:build !scaffold

package cmd

import "testing"

// TestDefaultBuildExcludesScaffoldCommands 确保项目初始化工具不进入服务二进制。
// make check-scaffold-isolation 单独以默认构建运行它；带标签的入口断言见 root_scaffold_test.go。
func TestDefaultBuildExcludesScaffoldCommands(t *testing.T) {
	t.Parallel()

	rootCmd := NewRootCmd(nil)
	forbidden := []string{"project"}

	registered := make(map[string]bool)
	for _, cmd := range rootCmd.Commands() {
		registered[cmd.Name()] = true
	}

	for _, name := range forbidden {
		if registered[name] {
			t.Fatalf("command %q is registered in a default (non-scaffold) build; "+
				"a file is missing its '//go:build scaffold' constraint", name)
		}
	}
}
