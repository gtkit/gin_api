//go:build scaffold

package cmd

import (
	"errors"
	"testing"

	"github.com/gtkit/gin-api/internal/projectinit"
)

// TestScaffoldBuildRegistersProjectCommand 确保项目初始化入口仍由开发期标签提供。
func TestScaffoldBuildRegistersProjectCommand(t *testing.T) {
	t.Parallel()

	rootCmd := NewRootCmd(nil)
	want := []string{"project"}

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

func TestProjectInitOutputFailuresAreReturned(t *testing.T) {
	t.Parallel()
	failure := errors.New("summary output failed")
	root := NewRootCmd(nil)
	root.SetOut(versionFailWriter{failure})
	if err := printProjectInitResult(root, &projectinit.Result{DryRun: true}); !errors.Is(err, failure) {
		t.Fatalf("project summary: %v", err)
	}
}
