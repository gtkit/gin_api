package cmd

import (
	"bytes"
	"testing"

	"github.com/gtkit/gin-api/internal/scaffold"
)

func TestNewRootCmdRegistersExpectedCommands(t *testing.T) {
	t.Parallel()

	rootCmd := NewRootCmd()
	want := map[string]bool{
		"server": true,
		"jwt":    true,
		"module": true,
		"doctor": true,
	}

	got := make(map[string]bool)
	for _, cmd := range rootCmd.Commands() {
		got[cmd.Name()] = true
	}

	for name := range want {
		if !got[name] {
			t.Fatalf("command %q is not registered", name)
		}
	}
}

func TestPrintGenerationSummaryDryRun(t *testing.T) {
	t.Parallel()

	rootCmd := NewRootCmd()
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
