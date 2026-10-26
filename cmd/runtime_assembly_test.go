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
	"github.com/gtkit/gin-api/internal/platform/config"
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
	forceUnreachableTargets(t)
	requireIsolatedConfigFailure(t, root, true)
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

func TestCommandsRejectExplicitRootWithoutConfiguration(t *testing.T) {
	for _, tc := range []struct {
		name   string
		args   []string
		prefix string
	}{
		{"migrate", []string{"migrate", "up"}, "bootstrap runtime: "},
		{"make", []string{"make", "migration", "create_v11_probe_table"}, "bootstrap runtime: "},
		{"doctor", []string{"doctor"}, "validate runtime configuration: "},
		{"doctor deep", []string{"doctor", "--deep", "--json"}, "validate runtime configuration: "},
	} {
		for _, mode := range []string{"missing", "directory"} {
			t.Run(tc.name+"/"+mode, func(t *testing.T) {
				root := t.TempDir()
				t.Setenv("APP_ROOT", root)
				path := filepath.Join(root, "config", "env", "env.yml")
				if mode == "directory" {
					if err := os.MkdirAll(path, 0o700); err != nil {
						t.Fatal(err)
					}
				}
				forceUnreachableTargets(t)
				requireIsolatedConfigFailure(t, root, mode == "missing")
				command := NewRootCmd(nil)
				command.SetOut(io.Discard)
				command.SetErr(io.Discard)
				command.SetArgs(tc.args)
				err := command.ExecuteContext(t.Context())
				pathErr, ok := errors.AsType[*os.PathError](err)
				if !ok || pathErr.Path != path {
					t.Fatalf("command error=%v; want explicit-root file error for %s", err, path)
				}
				if mode == "missing" && !errors.Is(err, os.ErrNotExist) {
					t.Fatalf("missing configuration cause lost: %v", err)
				}
				if !strings.HasPrefix(err.Error(), tc.prefix) {
					t.Fatalf("command error=%v; want prefix %q", err, tc.prefix)
				}
			})
		}
	}
}

// requireIsolatedConfigFailure stops before command construction if the default
// loader no longer rejects this root. Load can itself read a fallback file; this
// check prevents subsequent command effects, not filesystem access by Load.
func requireIsolatedConfigFailure(t *testing.T, root string, wantMissing bool) {
	t.Helper()
	cfg, err := config.Load()
	pathErr, ok := errors.AsType[*os.PathError](err)
	want := filepath.Join(root, "config", "env", "env.yml")
	if cfg != nil || !ok || pathErr.Path != want || errors.Is(err, os.ErrNotExist) != wantMissing {
		// Neither cfg nor err is printed: a regressed loader may have read secrets.
		t.Fatal("explicit APP_ROOT did not isolate the configuration; refusing to run commands")
	}
}

// forceUnreachableTargets is only a fallback precaution: a loopback port can
// have a listener, so these overrides are not an isolation boundary.
func forceUnreachableTargets(t *testing.T) {
	t.Helper()
	t.Setenv("APP_DATABASE__HOST", "127.0.0.1")
	t.Setenv("APP_DATABASE__PORT", "1")
	t.Setenv("APP_REDIS__ADDR", "127.0.0.1:1")
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
