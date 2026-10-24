package cmd

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

type versionFailWriter struct{ failure error }

func (w versionFailWriter) Write([]byte) (int, error) { return 0, w.failure }

func TestVersionHasExplicitInputsAndNoRuntimeDependencies(t *testing.T) {
	t.Setenv("APP_ROOT", t.TempDir())
	for _, info := range []BuildInfo{{Version: "v0.4.0", Commit: "verified-commit", BuildTime: "2026-10-22T00:00:00Z"}, {}} {
		root := newRootCmd(nil, info)
		root.SetArgs([]string{"--version"})
		var output bytes.Buffer
		root.SetOut(&output)
		if err := root.Execute(); err != nil {
			t.Fatal(err)
		}
		for _, field := range []string{"version ", "commit=", "build_time="} {
			if !strings.Contains(output.String(), field) {
				t.Fatalf("missing %s: %s", field, output.String())
			}
		}
		if info.Version == "" && !strings.Contains(output.String(), "unknown") {
			t.Fatal("missing inputs were fabricated")
		}
		if info.Version != "" && !strings.Contains(output.String(), info.Version+" commit="+info.Commit+" build_time="+info.BuildTime) {
			t.Fatalf("inputs changed: %s", output.String())
		}
	}
	failure := errors.New("output failed")
	root := newRootCmd(nil, BuildInfo{Version: "v0.4.0"})
	root.SetArgs([]string{"--version"})
	root.SetOut(versionFailWriter{failure})
	if err := root.Execute(); !errors.Is(err, failure) {
		t.Fatalf("version output failure lost: %v", err)
	}
	if err := printBanner(versionFailWriter{failure}); !errors.Is(err, failure) {
		t.Fatalf("banner output failure lost: %v", err)
	}
}
