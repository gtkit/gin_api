package cmd

import (
	"testing"
)

func TestModuleCommandIsNotRegistered(t *testing.T) {
	t.Parallel()

	for _, command := range NewRootCmd(nil).Commands() {
		if command.Name() == "module" {
			t.Fatal("module generator command must not be registered")
		}
	}
}

func TestNewRootCmdRegistersExpectedCommands(t *testing.T) {
	t.Parallel()

	rootCmd := NewRootCmd(nil)
	want := map[string]bool{
		"server":  true,
		"jwt":     true,
		"migrate": true,
		"doctor":  true,
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
