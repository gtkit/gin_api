package cmd

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestJWTGenerateEnvDefaultsAndOverrides(t *testing.T) {
	for _, scenario := range []string{"configured paths", "kid override", "explicit directory", "explicit with env", "missing env", "empty directory", "missing explicit kid", "occupied public"} {
		t.Run(scenario, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("APP_ROOT", root)
			privatePath, publicPath := filepath.Join(root, "private/custom.pem"), filepath.Join(root, "public/custom.pem")
			args := []string{}
			wantKid := "configured-kid"
			withEnv := scenario != "explicit directory" && scenario != "missing explicit kid" && scenario != "missing env"
			if withEnv {
				configDir := filepath.Join(root, "config/env")
				for _, dir := range []string{configDir, filepath.Dir(privatePath), filepath.Dir(publicPath)} {
					if err := os.MkdirAll(dir, 0o700); err != nil {
						t.Fatal(err)
					}
				}
				data := []byte("jwt:\n  active:\n    kid: configured-kid\n    private_key_path: private/custom.pem\n    public_key_path: public/custom.pem\n")
				if err := os.WriteFile(filepath.Join(configDir, "env.yml"), data, 0o600); err != nil {
					t.Fatal(err)
				}
			}
			switch scenario {
			case "kid override":
				args = append(args, "--kid", "override-kid")
				wantKid = "override-kid"
			case "explicit directory", "explicit with env", "missing explicit kid":
				dir := filepath.Join(root, "override")
				if err := os.Mkdir(dir, 0o700); err != nil {
					t.Fatal(err)
				}
				args = append(args, "--out-dir", dir)
				if scenario != "missing explicit kid" {
					args = append(args, "--kid", "override-kid")
				}
				wantKid = "override-kid"
				privatePath, publicPath = filepath.Join(dir, "override-kid.private.pem"), filepath.Join(dir, "override-kid.public.pem")
			case "empty directory":
				args = append(args, "--out-dir", "", "--kid", "override-kid")
			case "occupied public":
				if err := os.WriteFile(publicPath, []byte("preserve existing public"), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			command := newJWTGenerateCmd()
			var output bytes.Buffer
			command.SetOut(&output)
			command.SetErr(io.Discard)
			command.SetArgs(args)
			err := command.Execute()
			if scenario == "missing env" || scenario == "empty directory" || scenario == "missing explicit kid" || scenario == "occupied public" {
				if err == nil {
					t.Fatal("invalid input or occupied destination accepted")
				}
				if _, statErr := os.Stat(privatePath); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatal("failed command created private key")
				}
				if scenario == "occupied public" && string(readFile(t, publicPath)) != "preserve existing public" {
					t.Fatal("existing public key changed")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), "kid: "+wantKid) {
				t.Fatal("kid did not follow env/CLI precedence")
			}
			if _, err := os.Stat(publicPath); err != nil {
				t.Fatal(err)
			}
			before := readFile(t, privatePath)
			command = newJWTGenerateCmd()
			command.SetOut(io.Discard)
			command.SetErr(io.Discard)
			command.SetArgs(args)
			if err := command.Execute(); !errors.Is(err, os.ErrExist) {
				t.Fatalf("repeated generation: %v", err)
			}
			if !bytes.Equal(before, readFile(t, privatePath)) {
				t.Fatal("existing key was replaced")
			}
			if scenario == "explicit with env" {
				if _, statErr := os.Stat(filepath.Join(root, "private/custom.pem")); !errors.Is(statErr, os.ErrNotExist) {
					t.Fatal("env path used despite CLI override")
				}
			}
		})
	}
}
