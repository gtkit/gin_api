package config

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeJWTCommandConfig(t *testing.T, root, text string) {
	t.Helper()
	dir := filepath.Join(root, "config/env")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "env.yml"), []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestJWTCommandConfigRoot(t *testing.T) {
	for _, mode := range []string{"APP_ROOT", "working directory", "absolute key paths"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			t.Chdir(t.TempDir())
			t.Setenv("APP_ROOT", root)
			privatePath, publicPath := "private/custom.pem", "public/custom.pem"
			if mode == "working directory" {
				t.Chdir(root)
				t.Setenv("APP_ROOT", "")
			}
			if mode == "absolute key paths" {
				privatePath = filepath.Join(t.TempDir(), "private.pem")
				publicPath = filepath.Join(t.TempDir(), "public.pem")
			}
			writeJWTCommandConfig(t, root, "jwt:\n  active:\n    kid: configured-kid\n    private_key_path: "+filepath.ToSlash(privatePath)+"\n    public_key_path: "+filepath.ToSlash(publicPath)+"\n")
			keys, err := LoadJWTKeyConfigForCommand()
			if err != nil {
				t.Fatal(err)
			}
			if !filepath.IsAbs(privatePath) {
				privatePath = filepath.Join(root, privatePath)
				publicPath = filepath.Join(root, publicPath)
			}
			if keys.KID != "configured-kid" || keys.PrivateKeyPath != privatePath || keys.PublicKeyPath != publicPath {
				t.Fatal("command paths were not resolved from env and its project root")
			}
		})
	}
}

func TestJWTCommandConfigRejectsMissingPaths(t *testing.T) {
	for _, tc := range []struct{ name, text string }{
		{"missing active", "jwt: {}\n"},
		{"missing private", "jwt:\n  active:\n    public_key_path: public.pem\n"},
		{"missing public", "jwt:\n  active:\n    private_key_path: private.pem\n"},
		{"blank path", "jwt:\n  active:\n    private_key_path: ' '\n    public_key_path: public.pem\n"},
		{"same path", "jwt:\n  active:\n    private_key_path: same.pem\n    public_key_path: same.pem\n"},
		{"bad yaml", "jwt: [\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("APP_ROOT", root)
			writeJWTCommandConfig(t, root, tc.text)
			if _, err := LoadJWTKeyConfigForCommand(); err == nil {
				t.Fatal("invalid command configuration was accepted")
			}
		})
	}
}

func TestJWTCommandConfigDoesNotDiscoverOtherProjects(t *testing.T) {
	t.Setenv("APP_ROOT", t.TempDir())
	if _, err := LoadJWTKeyConfigForCommand(); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("error = %v, want missing env; must not discover bin or source directories", err)
	}
}
