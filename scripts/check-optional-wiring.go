//go:build ignore

// check-optional-wiring verifies build profiles directly in the original project.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func profiles(name string) ([]string, error) {
	switch name {
	case "default", "optional":
		return []string{name}, nil
	case "all":
		return []string{"default", "optional"}, nil
	default:
		return nil, errors.New("case must be default, optional or all")
	}
}

func wiringEnvironment(inherited []string) []string {
	out := make([]string, 0, len(inherited)+3)
	for _, entry := range inherited {
		key, _, _ := strings.Cut(entry, "=")
		if key != "GOFLAGS" && key != "GOWORK" && key != "GOTOOLCHAIN" {
			out = append(out, entry)
		}
	}
	return append(out, "GOFLAGS=", "GOWORK=off", "GOTOOLCHAIN=local")
}

func runWiring(ctx context.Context, root, profile string, output io.Writer) error {
	run := func(args ...string) ([]byte, error) {
		cmd := exec.CommandContext(ctx, "go", args...)
		cmd.Dir = root
		cmd.Env = wiringEnvironment(os.Environ())
		body, err := cmd.CombinedOutput()
		if err != nil {
			return nil, fmt.Errorf("go %s: %w\n%s", strings.Join(args, " "), err, body)
		}
		return body, nil
	}
	tags := ""
	if profile == "optional" {
		tags = "optional"
	}
	args := []string{"list", "-mod=readonly", "-deps"}
	if tags != "" {
		args = append(args, "-tags", tags)
	}
	deps, err := run(append(args, ".")...)
	if err != nil {
		return err
	}
	for _, feature := range []string{"taskqueue", "outbound", "upload", "auditlog"} {
		present := strings.Contains(string(deps), "/internal/optional/"+feature+"\n")
		if present != (profile == "optional") {
			return fmt.Errorf("%s profile: unexpected %s dependency", profile, feature)
		}
	}
	args = []string{"build", "-mod=readonly", "-trimpath", "-o", os.DevNull}
	if tags != "" {
		args = append(args, "-tags", tags)
	}
	if _, err := run(append(args, ".")...); err != nil {
		return err
	}
	// Compile integration test entrypoints without executing or connecting them.
	testTags := "integration"
	if tags != "" {
		testTags += "," + tags
	}
	for _, pkg := range []string{"./cmd", "./internal/bootstrap", "./internal/platform/config"} {
		if _, err := run("test", "-c", "-mod=readonly", "-vet=off", "-trimpath", "-tags", testTags, "-o", os.DevNull, pkg); err != nil {
			return err
		}
	}
	_, err = fmt.Fprintf(output, "%s: dependency isolation and builds passed\n", profile)
	return err
}

func main() {
	name := flag.String("case", "all", "build profile: default, optional or all")
	timeout := flag.Duration("timeout", 5*time.Minute, "total build budget, positive and at most 15m")
	flag.Parse()
	selected, err := profiles(*name)
	if err == nil && (flag.NArg() != 0 || *timeout <= 0 || *timeout > 15*time.Minute) {
		err = errors.New("invalid arguments or build budget")
	}
	if err == nil {
		root, rootErr := os.Getwd()
		err = rootErr
		if err == nil {
			if _, statErr := os.Stat(filepath.Join(root, "go.mod")); statErr != nil {
				err = errors.New("run from the original project root")
			}
		}
		if err == nil {
			ctx, cancel := context.WithTimeout(context.Background(), *timeout)
			defer cancel()
			for _, profile := range selected {
				if err = runWiring(ctx, root, profile, os.Stdout); err != nil {
					break
				}
			}
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
