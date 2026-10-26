//go:build ignore

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestOptionalWiringManifest(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test source path unavailable")
	}
	root := filepath.Dir(filepath.Dir(file))
	files, err := readSources(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range wiringCases {
		enabled, err := enabledFeatures(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := renderSources(files, enabled, wiringManifest); err != nil {
			t.Fatalf("case %s: %v", name, err)
		}
	}
	if len(wiringManifest) != 40 {
		t.Fatalf("manifest has %d blocks, want 40", len(wiringManifest))
	}
	if _, err := enabledFeatures("unknown"); err == nil {
		t.Fatal("unknown case accepted")
	}
	if _, err := renderSources(files, map[string]bool{}, append(wiringManifest, wiringManifest[0])); err == nil {
		t.Fatal("duplicate manifest block accepted")
	}
}

func TestOptionalWiringUnitRestoration(t *testing.T) {
	for _, tc := range []struct{ mode, body, want string }{
		{"line", "\t// value := 1\n\t// // kept comment\n", "\tvalue := 1\n\t// kept comment\n"},
		{"block", "/*\nvar value = 1\n*/\n", "\nvar value = 1\n\n"},
		{"yaml", "# option:\n#   enabled: false\n", "option:\n  enabled: false\n"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			got, err := restoreBlock(tc.body, tc.mode)
			if err != nil || got != tc.want {
				t.Fatalf("restore = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
	spec := wiringBlock{"fixture", "fixture.go", "outbound", "line"}
	source := "package fixture\n// wiring:begin fixture outbound line\n// import \"github.com/gtkit/gin-api/internal/optional/outbound\"\n// wiring:end fixture\n"
	for _, enabled := range []bool{false, true} {
		got, err := renderMarked("fixture.go", source, "example.test/app", map[string]bool{"outbound": enabled}, map[string]wiringBlock{"fixture": spec}, map[string]bool{})
		if err != nil {
			t.Fatal(err)
		}
		if enabled && !strings.Contains(got, "import \"example.test/app/internal/optional/outbound\"") {
			t.Fatal("derived module path was not restored")
		}
		if !enabled && got != source {
			t.Fatal("disabled wiring changed source")
		}
	}
}

func TestOptionalWiringUnitRejectsBrokenMarkers(t *testing.T) {
	spec := wiringBlock{"x", "x.go", "outbound", "line"}
	begin := "// wiring:begin x outbound line\n"
	end := "// wiring:end x\n"
	for _, source := range []string{
		begin + "// var x = 1\n",
		begin + begin + end + end,
		begin + "// var x = 1\n// wiring:end y\n",
		begin + "var x = 1\n" + end,
		begin + "// var x = 1\n" + end + begin + "// var x = 2\n" + end,
		"// wiring:begin unknown outbound line\n// var x = 1\n// wiring:end unknown\n",
	} {
		if _, err := renderMarked("x.go", source, "example.test/app", map[string]bool{"outbound": true}, map[string]wiringBlock{"x": spec}, map[string]bool{}); err == nil {
			t.Fatalf("broken marker accepted: %s", source)
		}
	}
	files := map[string][]byte{"go.mod": []byte("module example.test/app\n"), "x.go": []byte("package x\n")}
	if _, err := renderSources(files, nil, []wiringBlock{spec}); err == nil {
		t.Fatal("missing marker accepted")
	}
}

func TestOptionalWiringUnitNegativeCall(t *testing.T) {
	for _, count := range []int{0, 1, 2} {
		files := map[string][]byte{"internal/bootstrap/providers.go": []byte(strings.Repeat("buildOutbound(&inf, cleanup)\n", count))}
		err := injectBrokenWiring(files)
		if (err == nil) != (count == 1) {
			t.Fatalf("negative fixture count=%d error=%v", count, err)
		}
		if err == nil && !strings.Contains(string(files["internal/bootstrap/providers.go"]), "missingOptionalWiringCall") {
			t.Fatal("negative probe did not break the copied call")
		}
	}
}

func TestOptionalWiringSafetyCopyAndCleanup(t *testing.T) {
	root := t.TempDir()
	for _, path := range []string{"go.mod", "main.go", "internal/demo/demo.go", "internal/optional/demo/demo.go", "config/env.yml.example", "config/env/env.yml", "go.work", ".env", "secret.pem", ".cache/private.go"} {
		full := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("fixture"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	files, err := readSources(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"config/env/env.yml", "go.work", ".env", "secret.pem", ".cache/private.go"} {
		if _, copied := files[forbidden]; copied {
			t.Fatalf("copied forbidden source %s", forbidden)
		}
	}
	workspace, err := newWiringWorkspace(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := workspace.close(); err != nil {
			t.Error(err)
		}
	})
	if err := workspace.write(files, true); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(workspace.path, "internal/optional")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("removed case retained optional directory: %v", err)
	}
	if err := workspace.write(map[string][]byte{"../escape.go": []byte("x")}, false); err == nil {
		t.Fatal("outside destination accepted")
	}
	outside := &wiringWorkspace{path: root, parent: filepath.Dir(root)}
	if err := outside.close(); err == nil {
		t.Fatal("unowned directory removed")
	}
	if err := os.Symlink(filepath.Join(root, "main.go"), filepath.Join(root, "internal/demo/link.go")); err != nil {
		t.Fatal(err)
	}
	if _, err := readSources(root); err == nil {
		t.Fatal("source symlink accepted")
	}
	if err := workspace.close(); err != nil {
		t.Fatal(err)
	}
	if err := workspace.close(); err != nil {
		t.Fatal("repeated cleanup failed:", err)
	}
}

func TestOptionalWiringSafetyEnvironmentAndTimeout(t *testing.T) {
	env := wiringEnvironment([]string{"PATH=/test", "HOME=/home", "APP_DATABASE__PASSWORD=secret", "GOFLAGS=-race", "GOWORK=/other", "GOCACHE=/owned-cache"}, "/owned-root")
	for _, required := range []string{"GOWORK=off", "GOENV=off", "GOPROXY=off", "GOSUMDB=off", "GOFLAGS=", "APP_ROOT=/owned-root", "GOCACHE=/owned-cache"} {
		if !slices.Contains(env, required) {
			t.Errorf("missing environment %s", required)
		}
	}
	for _, value := range env {
		if strings.Contains(value, "secret") || value == "GOFLAGS=-race" {
			t.Fatal("unsafe environment retained")
		}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	step, err := executeWiringStep(ctx, t.TempDir(), append(os.Environ(), "WIRING_SLEEP_FIXTURE=1"), []string{os.Args[0], "-test.run=^TestWiringSleepFixture$"})
	if !errors.Is(err, context.DeadlineExceeded) || step.ExitCode == 0 {
		t.Fatalf("timeout = %+v, %v", step, err)
	}
}

func TestWiringSleepFixture(t *testing.T) {
	if os.Getenv("WIRING_SLEEP_FIXTURE") == "1" {
		time.Sleep(5 * time.Second)
	}
}
