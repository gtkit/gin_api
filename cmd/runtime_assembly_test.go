package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/gtkit/gin-api/internal/bootstrap"
)

func TestCommandHelpDoesNotLoadRuntimeConfiguration(t *testing.T) {
	t.Setenv("APP_ROOT", t.TempDir())
	for _, args := range [][]string{{"server", "--help"}, {"migrate"}, {"migrate", "help"}, {"make", "help"}, {"doctor", "--help"}, {"jwt", "--help"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			command := NewRootCmd(nil)
			var output bytes.Buffer
			command.SetOut(&output)
			command.SetErr(&output)
			command.SetArgs(args)
			if err := command.ExecuteContext(t.Context()); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), "Usage:") {
				t.Fatalf("missing help: %s", output.String())
			}
		})
	}
}

func TestServerUsesDefaultConfigurationLoader(t *testing.T) {
	t.Setenv("APP_ROOT", t.TempDir())
	command := newServerCmd(nil)
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	err := command.ExecuteContext(t.Context())
	if err == nil || !strings.Contains(err.Error(), "bootstrap app") || !strings.Contains(err.Error(), "config") {
		t.Fatalf("default configuration error=%v", err)
	}
}

func TestDefaultRuntimesHonorCanceledStartup(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if owner, err := bootstrap.NewRuntime(ctx, bootstrap.Options{}); owner != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("runtime=(%v,%v)", owner, err)
	}
	if owner, err := bootstrap.NewMigrationRuntime(ctx, bootstrap.MigrationOptions{}); owner != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("migration=(%v,%v)", owner, err)
	}
}
