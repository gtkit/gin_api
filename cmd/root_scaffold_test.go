//go:build scaffold

package cmd

import (
	"bytes"
	"testing"

	"github.com/gtkit/gin-api/internal/scaffold"
)

// TestScaffoldBuildRegistersScaffoldCommands 是隔离契约的另一半：带 tag 构建时
// 两条命令必须在场。只断言"默认构建里没有"是不够的——那条断言在命令被彻底
// 删除时同样通过，而那不是本 change 想要的结果。
func TestScaffoldBuildRegistersScaffoldCommands(t *testing.T) {
	t.Parallel()

	rootCmd := NewRootCmd(nil)
	want := []string{"module", "project"}

	registered := make(map[string]bool)
	for _, cmd := range rootCmd.Commands() {
		registered[cmd.Name()] = true
	}

	for _, name := range want {
		if !registered[name] {
			t.Fatalf("command %q is not registered under the scaffold build tag", name)
		}
	}
}

func TestPrintGenerationSummaryDryRun(t *testing.T) {
	t.Parallel()

	rootCmd := NewRootCmd(nil)
	var out bytes.Buffer
	rootCmd.SetOut(&out)

	printGenerationSummary(rootCmd, &scaffold.GenerateResult{
		DryRun:  true,
		Created: []string{"internal/module/demo/application/service.go"},
		Updated: []string{"internal/bootstrap/providers.go"},
	})

	got := out.String()
	if !bytes.Contains([]byte(got), []byte("would create internal/module/demo/application/service.go")) {
		t.Fatalf("summary missing dry-run create line: %s", got)
	}
	if !bytes.Contains([]byte(got), []byte("would update internal/bootstrap/providers.go")) {
		t.Fatalf("summary missing dry-run update line: %s", got)
	}
	if !bytes.Contains([]byte(got), []byte("dry-run mode: no files were written")) {
		t.Fatalf("summary missing dry-run tail: %s", got)
	}
}
