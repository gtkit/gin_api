package cmd

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
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
	root := t.TempDir()
	t.Setenv("APP_ROOT", root)
	command := newServerCmd(nil)
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	err := command.ExecuteContext(t.Context())
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("default configuration error=%v", err)
	}
	pathErr, ok := errors.AsType[*os.PathError](err)
	wantPath := filepath.Join(root, "config", "env", "env.yml")
	if !ok || pathErr.Path != wantPath {
		t.Fatalf("configuration error=%v, want missing path %s", err, wantPath)
	}
	if !strings.HasPrefix(err.Error(), "bootstrap app: ") {
		t.Fatalf("missing command error context: %v", err)
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
