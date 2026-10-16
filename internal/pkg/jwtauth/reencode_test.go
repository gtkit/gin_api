package jwtauth

import (
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/gtkit/gojwt"
)

func reencodingFixture(t *testing.T, legacy bool) (string, string) {
	t.Helper()
	dir := t.TempDir()
	privatePath, publicPath := filepath.Join(dir, "private.pem"), filepath.Join(dir, "public.pem")
	if err := gojwt.GenerateEd25519Keys(privatePath, publicPath); err != nil {
		t.Fatal(err)
	}
	if legacy {
		for _, file := range []struct {
			path, label string
			private     bool
		}{{privatePath, "ED25519 PRIVATE KEY", true}, {publicPath, "ED25519 PUBLIC KEY", false}} {
			data := readReencodingFile(t, file.path)
			var raw []byte
			if file.private {
				key, err := gojwt.ParseEd25519PrivateKeyPEM(data)
				if err != nil {
					t.Fatal(err)
				}
				raw = key
			} else {
				key, err := gojwt.ParseEd25519PublicKeyPEM(data)
				if err != nil {
					t.Fatal(err)
				}
				raw = key
			}
			if err := os.WriteFile(file.path, pem.EncodeToMemory(&pem.Block{Type: file.label, Bytes: raw}), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	return privatePath, publicPath
}

func readReencodingFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestReencodePreservesIdentityAndOldToken(t *testing.T) {
	t.Parallel()
	for _, legacy := range []bool{true, false} {
		name := "standard"
		if legacy {
			name = "legacy"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			privatePath, publicPath := reencodingFixture(t, legacy)
			beforePrivate, beforePublic := readReencodingFile(t, privatePath), readReencodingFile(t, publicPath)
			old, err := gojwt.NewJwtEd25519(privatePath, publicPath, gojwt.WithKeyID("existing-kid"))
			if err != nil {
				t.Fatal(err)
			}
			token, err := old.GenerateToken(42)
			if err != nil {
				t.Fatal(err)
			}
			out := filepath.Join(t.TempDir(), "standard")
			if reencodeErr := ReencodeEd25519Keys(privatePath, publicPath, out); reencodeErr != nil {
				t.Fatal(reencodeErr)
			}
			newPrivate := readReencodingFile(t, filepath.Join(out, "jwtpri.pem"))
			newPublic := readReencodingFile(t, filepath.Join(out, "jwtpub.pem"))
			privateBlock, _ := pem.Decode(newPrivate)
			publicBlock, _ := pem.Decode(newPublic)
			if privateBlock == nil || privateBlock.Type != "PRIVATE KEY" || publicBlock == nil || publicBlock.Type != "PUBLIC KEY" {
				t.Fatal("converted PEM labels are not standard")
			}
			private, err := x509.ParsePKCS8PrivateKey(privateBlock.Bytes)
			if err != nil {
				t.Fatal(err)
			}
			public, err := x509.ParsePKIXPublicKey(publicBlock.Bytes)
			if err != nil {
				t.Fatal(err)
			}
			originalPrivate, err := gojwt.ParseEd25519PrivateKeyPEM(beforePrivate)
			if err != nil {
				t.Fatal(err)
			}
			originalPublic, err := gojwt.ParseEd25519PublicKeyPEM(beforePublic)
			if err != nil {
				t.Fatal(err)
			}
			if !originalPrivate.Equal(private) || !originalPublic.Equal(public) {
				t.Fatal("key material changed")
			}
			converted, err := gojwt.NewJwtEd25519(filepath.Join(out, "jwtpri.pem"), filepath.Join(out, "jwtpub.pem"))
			if err != nil {
				t.Fatal(err)
			}
			claims, err := converted.ParseToken(token)
			if err != nil || claims.UserID != 42 {
				t.Fatalf("old token no longer verifies: %v", err)
			}
			if !bytes.Equal(beforePrivate, readReencodingFile(t, privatePath)) || !bytes.Equal(beforePublic, readReencodingFile(t, publicPath)) {
				t.Fatal("source files changed")
			}
			if err := ReencodeEd25519Keys(privatePath, publicPath, out); !errors.Is(err, os.ErrExist) {
				t.Fatalf("second conversion error = %v, want ErrExist", err)
			}
			if !bytes.Equal(newPrivate, readReencodingFile(t, filepath.Join(out, "jwtpri.pem"))) || !bytes.Equal(newPublic, readReencodingFile(t, filepath.Join(out, "jwtpub.pem"))) {
				t.Fatal("existing output files changed")
			}
		})
	}
}

func TestReencodeRejectsInvalidInputBeforeWriting(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"private missing", "public missing", "invalid private", "invalid public", "mismatched pair", "inconsistent seed", "empty output"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			privatePath, publicPath := reencodingFixture(t, true)
			out := filepath.Join(t.TempDir(), "out")
			switch scenario {
			case "private missing":
				privatePath += ".missing"
			case "public missing":
				publicPath += ".missing"
			case "invalid private":
				privatePath = publicPath
			case "invalid public":
				publicPath = privatePath
			case "mismatched pair":
				_, publicPath = reencodingFixture(t, true)
			case "inconsistent seed":
				block, _ := pem.Decode(readReencodingFile(t, privatePath))
				block.Bytes[ed25519.SeedSize] ^= 1
				if err := os.WriteFile(privatePath, pem.EncodeToMemory(block), 0o600); err != nil {
					t.Fatal(err)
				}
			case "empty output":
				out = " "
			}
			if err := ReencodeEd25519Keys(privatePath, publicPath, out); err == nil {
				t.Fatal("invalid input accepted")
			}
			if _, err := os.Lstat(out); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("invalid input created output")
			}
		})
	}
}

func TestReencodeRefusesExistingPaths(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"directory", "file", "source directory", "symlink"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			privatePath, publicPath := reencodingFixture(t, true)
			out := filepath.Join(t.TempDir(), "occupied")
			switch kind {
			case "directory":
				if err := os.Mkdir(out, 0o700); err != nil {
					t.Fatal(err)
				}
			case "file":
				if err := os.WriteFile(out, []byte("preserve"), 0o600); err != nil {
					t.Fatal(err)
				}
			case "source directory":
				out = filepath.Dir(privatePath)
			case "symlink":
				if err := os.Symlink(t.TempDir(), out); err != nil {
					t.Fatal(err)
				}
			}
			before := readReencodingFile(t, privatePath)
			if err := ReencodeEd25519Keys(privatePath, publicPath, out); !errors.Is(err, os.ErrExist) {
				t.Fatalf("error = %v, want ErrExist", err)
			}
			if !bytes.Equal(before, readReencodingFile(t, privatePath)) {
				t.Fatal("input was modified")
			}
		})
	}
}
