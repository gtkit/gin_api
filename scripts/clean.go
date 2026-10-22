//go:build ignore

// clean.go removes only the default, named build outputs after checking every
// ancestor. It never recursively deletes a directory or follows a symlink.
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

func main() {
	binDir := flag.String("bin-dir", "./bin", "managed binary directory (only default bin is accepted)")
	appName := flag.String("app-name", "", "project binary name")
	coverage := flag.String("coverage-profile", ".cache/coverage.out", "managed coverage file (only default path is accepted)")
	flag.Parse()
	err := cleanOutputs(*binDir, *appName, *coverage, os.Stdout)
	if err == nil {
		return
	}
	if _, writeErr := fmt.Fprintln(os.Stderr, "clean:", err); writeErr != nil {
		os.Exit(1)
	}
	os.Exit(1)
}

func cleanOutputs(binDir, appName, coverage string, out io.Writer) (err error) {
	if filepath.Clean(binDir) != "bin" || filepath.Clean(coverage) != filepath.Join(".cache", "coverage.out") {
		return errors.New("only default bin/ and .cache/coverage.out are managed; custom outputs must be removed by their owner")
	}
	if !regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`).MatchString(appName) {
		return errors.New("invalid app name")
	}
	root, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("find project root: %w", err)
	}
	root, err = filepath.EvalSymlinks(root)
	if err != nil {
		return fmt.Errorf("resolve project root: %w", err)
	}
	rootFS, err := os.OpenRoot(root)
	if err != nil {
		return fmt.Errorf("open managed root: %w", err)
	}
	defer func() {
		if closeErr := rootFS.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close managed root: %w", closeErr))
		}
	}()
	info, err := rootFS.Stat("go.mod")
	if err != nil {
		return fmt.Errorf("inspect project go.mod: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("project go.mod must be a regular file")
	}
	paths := []string{filepath.Join("bin", appName), filepath.Join(".cache", "coverage.out")}
	// Validate both paths before the first mutation.
	for _, relative := range paths {
		current := ""
		parts := strings.Split(relative, string(filepath.Separator))
		for i, part := range parts {
			current = filepath.Join(current, part)
			info, err := rootFS.Lstat(current)
			if errors.Is(err, os.ErrNotExist) {
				break
			}
			if err != nil {
				return fmt.Errorf("inspect managed output %s: %w", current, err)
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("refuse symlink in managed output path %s", current)
			}
			if i < len(parts)-1 && !info.IsDir() {
				return fmt.Errorf("managed output parent %s is not a directory", current)
			}
			if i == len(parts)-1 && !info.Mode().IsRegular() {
				return fmt.Errorf("managed output %s is not a regular file", current)
			}
		}
	}
	var errs []error
	for _, relative := range paths {
		if err := rootFS.Remove(relative); err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				errs = append(errs, fmt.Errorf("remove managed output %s: %w", relative, err))
			}
			continue
		}
		if _, err := fmt.Fprintln(out, "removed", relative); err != nil {
			errs = append(errs, fmt.Errorf("write clean result: %w", err))
		}
	}
	return errors.Join(errs...)
}
