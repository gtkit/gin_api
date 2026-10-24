//go:build !scaffold

package cmd

import "testing"

// TestDefaultBuildExcludesScaffoldCommands 是「开发期脚手架工具不进入运行期二进制」
// 这条契约的反证测试：契约若为假，本用例失败。
//
// 约束标签是 `!scaffold` 而不是空：本仓的测试口径**带** `scaffold` tag 跑
// （见 Makefile 的 GO_BUILD_TAGS——脚手架自身的测试锁定着 module-scaffold-wiring
// 与 project-instantiation 两组需求，不带 tag 跑它们根本不会被编译）。而在那个
// 口径下两条命令本就应当在场，缺席断言必然失败。
//
// 因此本用例只在默认口径下存在，并由 `make check-scaffold-isolation` 单独执行
// 一次不带 tag 的 go test——否则它永远不会在门禁里跑，等于没有。
// 在场方向的断言在 root_scaffold_test.go。
//
// 为什么这两条命令不能留在服务二进制里：`module` 会重写
// internal/bootstrap/modules.go；`project init` 带 --force-reinit-git 且会永久
// 丢弃 git 历史。二者对派生项目都不存在正确用法——`project init` 在派生项目上
// 只可能失败或造成破坏。
func TestDefaultBuildExcludesScaffoldCommands(t *testing.T) {
	t.Parallel()

	rootCmd := NewRootCmd(nil)
	forbidden := []string{"module", "project"}

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
