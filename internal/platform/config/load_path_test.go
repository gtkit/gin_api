package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveConfigFileExplicitRoot(t *testing.T) {
	for _, tc := range []struct {
		name     string
		primary  bool
		v2       bool
		relative bool
		wantV2   bool
	}{
		{name: "primary", primary: true},
		{name: "v2", v2: true, wantV2: true},
		{name: "primary wins", primary: true, v2: true},
		{name: "missing reports primary"},
		{name: "relative primary", primary: true, relative: true},
		{name: "relative v2", v2: true, relative: true, wantV2: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := t.TempDir()
			root := filepath.Join(base, "runtime root with spaces")
			if tc.relative {
				t.Chdir(base)
				root = "runtime root with spaces"
			}
			if tc.primary {
				writeJWTCommandConfig(t, root, minimalConfigYAML())
			}
			if tc.v2 {
				writeJWTCommandConfig(t, filepath.Join(root, "v2"), minimalConfigYAML())
			}
			want := filepath.Join(root, configFile)
			if tc.wantV2 {
				want = filepath.Join(root, "v2", configFile)
			}
			got, err := resolveConfigFile(root)
			if err != nil || got != want {
				t.Fatalf("selected path=%q, error=%v; want %q", got, err, want)
			}
		})
	}
}

func TestLoadExplicitRootDoesNotFallback(t *testing.T) {
	clearConfigEnv(t)
	root := t.TempDir()
	t.Setenv("APP_ROOT", root)
	decoy := t.TempDir()
	writeJWTCommandConfig(t, decoy, minimalConfigYAML())
	t.Chdir(decoy)

	cfg, err := Load()
	if cfg != nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("config=%v, error=%v; want missing explicit-root config", cfg != nil, err)
	}
	pathErr, ok := errors.AsType[*os.PathError](err)
	want := filepath.Join(root, configFile)
	if !ok || pathErr.Path != want {
		t.Fatalf("configuration error=%v; want missing path %s", err, want)
	}
}

func TestLoadExplicitRootRejectsInvalidPrimary(t *testing.T) {
	for _, tc := range []struct {
		name      string
		contents  string
		directory bool
		prefix    string
	}{
		{name: "invalid YAML", contents: "application: [\n", prefix: "read config file "},
		{name: "unknown field", contents: minimalConfigYAML() + "\nunknown_setting: true\n", prefix: "decode config "},
		{name: "invalid value", contents: strings.Replace(minimalConfigYAML(), "dbtype: mysql", "dbtype: unsupported", 1), prefix: "validate config: "},
		{name: "directory", directory: true, prefix: "read config file "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearConfigEnv(t)
			root := t.TempDir()
			t.Setenv("APP_ROOT", root)
			writeJWTCommandConfig(t, filepath.Join(root, "v2"), minimalConfigYAML())
			if tc.directory {
				if err := os.MkdirAll(filepath.Join(root, configFile), 0o700); err != nil {
					t.Fatal(err)
				}
			} else {
				writeJWTCommandConfig(t, root, tc.contents)
			}
			cfg, err := Load()
			if cfg != nil || err == nil || !strings.HasPrefix(err.Error(), tc.prefix) {
				t.Fatalf("config=%v, error=%v; want primary failure with prefix %q", cfg != nil, err, tc.prefix)
			}
			if tc.directory {
				pathErr, ok := errors.AsType[*os.PathError](err)
				if !ok || pathErr.Path != filepath.Join(root, configFile) {
					t.Fatalf("directory error lost primary path: %v", err)
				}
			}
		})
	}
}

func TestLoadExplicitRootV2PreservesEnvironmentOverrides(t *testing.T) {
	clearConfigEnv(t)
	root := t.TempDir()
	t.Setenv("APP_ROOT", root)
	t.Setenv("APP_DATABASE__NAME", "explicit_root_override")
	writeJWTCommandConfig(t, filepath.Join(root, "v2"), minimalConfigYAML())

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.Name != "explicit_root_override" {
		t.Fatalf("database name=%q, want environment override", cfg.Database.Name)
	}
}

func TestResolveConfigFilePreservesStatError(t *testing.T) {
	root := filepath.Join(t.TempDir(), "invalid\x00root")
	wantPath := filepath.Join(root, configFile)
	_, probeErr := os.Stat(wantPath)
	wantErr, ok := errors.AsType[*os.PathError](probeErr)
	if !ok || errors.Is(probeErr, os.ErrNotExist) {
		t.Fatalf("invalid-path fixture did not produce a non-missing stat error: %v", probeErr)
	}

	selected, err := resolveConfigFile(root)
	gotErr, ok := errors.AsType[*os.PathError](err)
	if selected != "" || !ok || gotErr.Path != wantPath || gotErr.Op != wantErr.Op || !errors.Is(err, wantErr.Err) {
		t.Fatalf("selected path=%q, error=%v; want original stat failure %v", selected, err, probeErr)
	}
}
