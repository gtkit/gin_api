package cmd

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/gtkit/gojwt"
)

func TestJWTReencodeCommand(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	privatePath, publicPath := filepath.Join(dir, "old.private.pem"), filepath.Join(dir, "old.public.pem")
	if err := gojwt.GenerateEd25519Keys(privatePath, publicPath); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(dir, "standard")
	for _, repeat := range []bool{false, true} {
		command := newJWTCmd(nil)
		var output bytes.Buffer
		command.SetOut(&output)
		command.SetErr(&output)
		command.SetArgs([]string{"reencode", "--private-key", privatePath, "--public-key", publicPath, "--out-dir", outDir})
		err := command.Execute()
		if repeat {
			if !errors.Is(err, os.ErrExist) {
				t.Fatalf("repeated command error = %v, want ErrExist", err)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(output.Bytes(), []byte("-----BEGIN")) {
			t.Fatal("command printed key material")
		}
		if _, err := gojwt.NewJwtEd25519(filepath.Join(outDir, "jwtpri.pem"), filepath.Join(outDir, "jwtpub.pem")); err != nil {
			t.Fatal(err)
		}
	}
}

func TestJWTReencodeCommandRequiresArguments(t *testing.T) {
	t.Parallel()
	for _, args := range [][]string{
		{"reencode"},
		{"reencode", "--private-key", "private.pem"},
		{"reencode", "--private-key", "private.pem", "--public-key", "public.pem"},
		{"reencode", "unexpected"},
	} {
		t.Run(filepath.Join(args...), func(t *testing.T) {
			t.Parallel()
			command := newJWTCmd(nil)
			command.SetOut(new(bytes.Buffer))
			command.SetErr(new(bytes.Buffer))
			command.SetArgs(args)
			if err := command.Execute(); err == nil {
				t.Fatal("missing flags or unexpected arguments accepted")
			}
		})
	}
}
