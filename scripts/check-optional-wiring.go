//go:build ignore

// check-optional-wiring compiles explicit optional wiring in disposable copies.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"go/format"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

type wiringBlock struct{ id, path, feature, mode string }

var wiringManifest = []wiringBlock{
	{"taskqueue.impl", "internal/bootstrap/taskqueue.go", "taskqueue", "block"},
	{"outbound.impl", "internal/bootstrap/outbound.go", "outbound", "block"},
	{"auditlog.impl", "internal/bootstrap/auditlog.go", "auditlog", "block"},
	{"upload.impl", "internal/bootstrap/upload.go", "upload", "block"},
	{"taskqueue.tests", "internal/bootstrap/taskqueue_test.go", "taskqueue", "block"},
	{"providers.taskqueue.import", "internal/bootstrap/providers.go", "taskqueue", "line"},
	{"providers.outbound.import", "internal/bootstrap/providers.go", "outbound", "line"},
	{"providers.auditlog.import", "internal/bootstrap/providers.go", "auditlog", "line"},
	{"providers.upload.import", "internal/bootstrap/providers.go", "upload", "line"},
	{"providers.outbound.field", "internal/bootstrap/providers.go", "outbound", "line"},
	{"providers.auditlog.field", "internal/bootstrap/providers.go", "auditlog", "line"},
	{"providers.taskqueue.field", "internal/bootstrap/providers.go", "taskqueue", "line"},
	{"providers.upload.field", "internal/bootstrap/providers.go", "upload", "line"},
	{"providers.outbound.call", "internal/bootstrap/providers.go", "outbound", "line"},
	{"providers.taskqueue.call", "internal/bootstrap/providers.go", "taskqueue", "line"},
	{"providers.auditlog.call", "internal/bootstrap/providers.go", "auditlog", "line"},
	{"providers.upload.call", "internal/bootstrap/providers.go", "upload", "line"},
	{"modules.upload.import", "internal/bootstrap/modules.go", "upload", "line"},
	{"modules.auditlog.import", "internal/bootstrap/modules.go", "auditlog", "line"},
	{"modules.upload.fields", "internal/bootstrap/modules.go", "upload", "line"},
	{"modules.auditlog.routes", "internal/bootstrap/modules.go", "auditlog", "line"},
	{"modules.upload.routes", "internal/bootstrap/modules.go", "upload", "line"},
	{"modules.taskqueue.workers", "internal/bootstrap/modules.go", "taskqueue", "line"},
	{"modules.auditlog.workers", "internal/bootstrap/modules.go", "auditlog", "line"},
	{"requirements.taskqueue", "internal/bootstrap/requirements.go", "taskqueue", "line"},
	{"config.taskqueue.import", "internal/platform/config/types.go", "taskqueue", "line"},
	{"config.outbound.import", "internal/platform/config/types.go", "outbound", "line"},
	{"config.outbound.field", "internal/platform/config/types.go", "outbound", "line"},
	{"config.taskqueue.field", "internal/platform/config/types.go", "taskqueue", "line"},
	{"config.outbound.validate", "internal/platform/config/validate.go", "outbound", "line"},
	{"config.taskqueue.validate", "internal/platform/config/validate.go", "taskqueue", "line"},
	{"config.taskqueue.redis", "internal/platform/config/validate.go", "taskqueue", "line"},
	{"migrate.auditlog.import", "cmd/migrate.go", "auditlog", "line"},
	{"migrate.auditlog.model", "cmd/migrate.go", "auditlog", "line"},
	{"plugins.taskqueue.import", "cmd/plugins_integration_test.go", "taskqueue", "line"},
	{"plugins.outbound.import", "cmd/plugins_integration_test.go", "outbound", "line"},
	{"plugins.outbound.reset", "cmd/plugins_integration_test.go", "outbound", "line"},
	{"plugins.taskqueue.reset", "cmd/plugins_integration_test.go", "taskqueue", "line"},
	{"example.outbound", "config/env.yml.example", "outbound", "yaml"},
	{"example.taskqueue", "config/env.yml.example", "taskqueue", "yaml"},
}

var wiringCases = []string{"default", "taskqueue", "upload", "outbound", "auditlog", "all", "removed"}

func enabledFeatures(name string) (map[string]bool, error) {
	if !slices.Contains(wiringCases, name) {
		return nil, fmt.Errorf("unknown wiring case %q", name)
	}
	enabled := map[string]bool{}
	for _, feature := range []string{"taskqueue", "upload", "outbound", "auditlog"} {
		enabled[feature] = name == "all" || name == feature
	}
	return enabled, nil
}

func restoreBlock(body, mode string) (string, error) {
	if mode == "block" {
		trimmed := strings.TrimSpace(body)
		if !strings.HasPrefix(trimmed, "/*") || !strings.HasSuffix(trimmed, "*/") {
			return "", errors.New("block wiring must retain its outer comment")
		}
		return strings.TrimSuffix(strings.TrimPrefix(trimmed, "/*"), "*/") + "\n", nil
	}
	prefix := "//"
	if mode == "yaml" {
		prefix = "#"
	} else if mode != "line" {
		return "", fmt.Errorf("unknown wiring mode %q", mode)
	}
	var out strings.Builder
	for line := range strings.Lines(body) {
		if strings.TrimSpace(line) == "" {
			out.WriteString(line)
			continue
		}
		content := strings.TrimLeft(line, " \t")
		indent := line[:len(line)-len(content)]
		if !strings.HasPrefix(content, prefix) {
			return "", errors.New("line wiring contains an uncommented line")
		}
		content = strings.TrimPrefix(strings.TrimPrefix(content, prefix), " ")
		out.WriteString(indent + content)
	}
	return out.String(), nil
}

func renderMarked(path, source, module string, enabled map[string]bool, expected map[string]wiringBlock, seen map[string]bool) (string, error) {
	var out, body strings.Builder
	var current *wiringBlock
	var opening string
	for line := range strings.Lines(source) {
		fields := strings.Fields(line)
		marker := len(fields) > 1 && (fields[0] == "//" || fields[0] == "#")
		if marker && fields[1] == "wiring:begin" {
			if current != nil || len(fields) != 5 {
				return "", errors.New("nested or malformed wiring start")
			}
			spec, ok := expected[fields[2]]
			if !ok || spec.path != path || spec.feature != fields[3] || spec.mode != fields[4] || seen[spec.id] {
				return "", fmt.Errorf("unknown, misplaced or duplicate wiring block %q", fields[2])
			}
			seen[spec.id] = true
			current, opening = &spec, line
			body.Reset()
			continue
		}
		if marker && fields[1] == "wiring:end" {
			if current == nil || len(fields) != 3 || fields[2] != current.id {
				return "", errors.New("mismatched wiring end")
			}
			restored, err := restoreBlock(body.String(), current.mode)
			if err != nil {
				return "", fmt.Errorf("%s: %w", current.id, err)
			}
			out.WriteString(opening)
			if enabled[current.feature] {
				restored = strings.ReplaceAll(restored, "github.com/gtkit/gin-api/", module+"/")
				out.WriteString(restored)
			} else {
				out.WriteString(body.String())
			}
			out.WriteString(line)
			current = nil
			continue
		}
		if current != nil {
			body.WriteString(line)
		} else {
			out.WriteString(line)
		}
	}
	if current != nil {
		return "", errors.New("unterminated wiring block")
	}
	return out.String(), nil
}

func selectedSource(path string) bool {
	if path == "go.mod" || path == "go.sum" || path == "config/env.yml.example" {
		return true
	}
	if filepath.Ext(path) != ".go" {
		return false
	}
	top, _, nested := strings.Cut(filepath.ToSlash(path), "/")
	return !nested || top == "cmd" || top == "internal" || top == "pkg"
}

func readSources(root string) (map[string][]byte, error) {
	files := map[string][]byte{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if entry.IsDir() {
			if relative == "." {
				return nil
			}
			top, _, _ := strings.Cut(relative, "/")
			if top != "cmd" && top != "internal" && top != "pkg" && relative != "config" {
				return filepath.SkipDir
			}
			return nil
		}
		if !selectedSource(relative) {
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("refusing non-regular source %s", relative)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		files[relative] = data
		return nil
	})
	return files, err
}

func sourceModule(data []byte) (string, error) {
	for line := range strings.Lines(string(data)) {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[0] == "module" {
			return fields[1], nil
		}
	}
	return "", errors.New("go.mod has no module declaration")
}

func renderSources(files map[string][]byte, enabled map[string]bool, manifest []wiringBlock) (map[string][]byte, error) {
	module, err := sourceModule(files["go.mod"])
	if err != nil {
		return nil, err
	}
	expected, seen := map[string]wiringBlock{}, map[string]bool{}
	for _, block := range manifest {
		if _, exists := expected[block.id]; exists {
			return nil, fmt.Errorf("duplicate manifest block %s", block.id)
		}
		expected[block.id] = block
	}
	out := make(map[string][]byte, len(files))
	for path, source := range files {
		rendered, err := renderMarked(path, string(source), module, enabled, expected, seen)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		data := []byte(rendered)
		if filepath.Ext(path) == ".go" {
			data, err = format.Source(data)
			if err != nil {
				return nil, fmt.Errorf("format restored source %s: %w", path, err)
			}
		}
		out[path] = data
	}
	for id := range expected {
		if !seen[id] {
			return nil, fmt.Errorf("missing wiring block %s", id)
		}
	}
	return out, nil
}

type wiringWorkspace struct {
	path, parent string
	identity     os.FileInfo
}

func newWiringWorkspace(parent string) (*wiringWorkspace, error) {
	dir, err := os.MkdirTemp(parent, "gin-api-wiring-")
	if err != nil {
		return nil, err
	}
	identity, err := os.Lstat(dir)
	if err != nil {
		return nil, errors.Join(err, os.RemoveAll(dir))
	}
	return &wiringWorkspace{path: dir, parent: filepath.Dir(dir), identity: identity}, nil
}

func (w *wiringWorkspace) close() error {
	if w == nil || w.path == "" || filepath.Dir(w.path) != w.parent || !strings.HasPrefix(filepath.Base(w.path), "gin-api-wiring-") {
		return errors.New("refusing to remove an unowned wiring directory")
	}
	info, err := os.Lstat(w.path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || w.identity == nil || !os.SameFile(info, w.identity) {
		return errors.New("wiring directory is no longer a regular directory")
	}
	return os.RemoveAll(w.path)
}

func (w *wiringWorkspace) write(files map[string][]byte, removed bool) error {
	for path, data := range files {
		if removed && strings.HasPrefix(path, "internal/optional/") {
			continue
		}
		if !filepath.IsLocal(path) || !selectedSource(path) {
			return fmt.Errorf("refusing source destination %q", path)
		}
		destination := filepath.Join(w.path, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(destination), 0700); err != nil {
			return err
		}
		if err := os.WriteFile(destination, data, 0600); err != nil {
			return err
		}
	}
	return nil
}

func wiringEnvironment(inherited []string, root string) []string {
	allowed := map[string]bool{"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "TMPDIR": true,
		"LANG": true, "LC_ALL": true, "TZ": true, "DEVELOPER_DIR": true, "SDKROOT": true, "GOCACHE": true, "GOMODCACHE": true}
	var env []string
	for _, item := range inherited {
		key, _, _ := strings.Cut(item, "=")
		if allowed[key] {
			env = append(env, item)
		}
	}
	return append(env, "GOWORK=off", "GOENV=off", "GOTOOLCHAIN=local", "GOPROXY=off", "GOSUMDB=off", "GOFLAGS=", "APP_ROOT="+root)
}

type wiringStep struct {
	Argv     []string      `json:"argv"`
	Duration time.Duration `json:"duration_ns"`
	ExitCode int           `json:"exit_code"`
	Stdout   string        `json:"stdout,omitempty"`
	Stderr   string        `json:"stderr,omitempty"`
}

func executeWiringStep(parent context.Context, root string, env, argv []string) (wiringStep, error) {
	ctx, cancel := context.WithTimeout(parent, 120*time.Second)
	defer cancel()
	start := time.Now()
	command := exec.CommandContext(ctx, argv[0], argv[1:]...)
	command.Dir, command.Env = root, env
	command.WaitDelay = 2 * time.Second
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err := command.Run()
	code := 0
	if err != nil {
		code = -1
		if exit, ok := errors.AsType[*exec.ExitError](err); ok {
			code = exit.ExitCode()
		}
	}
	step := wiringStep{Argv: argv, Duration: time.Since(start), ExitCode: code, Stdout: stdout.String(), Stderr: stderr.String()}
	return step, errors.Join(err, ctx.Err())
}

type wiringResult struct {
	Case         string        `json:"case"`
	Negative     bool          `json:"negative"`
	SourceSHA256 string        `json:"source_sha256"`
	Duration     time.Duration `json:"duration_ns"`
	Steps        []wiringStep  `json:"steps"`
	Error        string        `json:"error,omitempty"`
}

func injectBrokenWiring(files map[string][]byte) error {
	const path = "internal/bootstrap/providers.go"
	const call = "buildOutbound(&inf, cleanup)"
	source := string(files[path])
	if strings.Count(source, call) != 1 {
		return errors.New("negative wiring probe requires one restored outbound call")
	}
	files[path] = []byte(strings.Replace(source, call, "missingOptionalWiringCall(&inf, cleanup)", 1))
	return nil
}

func runWiringCase(parent context.Context, root, name string, negative bool) (result wiringResult, resultErr error) {
	start := time.Now()
	result.Case = name
	result.Negative = negative
	defer func() {
		result.Duration = time.Since(start)
		if resultErr != nil {
			result.Error = resultErr.Error()
		}
	}()
	enabled, err := enabledFeatures(name)
	if err != nil {
		return result, err
	}
	files, err := readSources(root)
	if err != nil {
		return result, err
	}
	keys := make([]string, 0, len(files))
	for path := range files {
		keys = append(keys, path)
	}
	slices.Sort(keys)
	hash := sha256.New()
	for _, path := range keys {
		// hash.Hash writes never return an error.
		_, _ = fmt.Fprintf(hash, "%s\x00", path)
		_, _ = hash.Write(files[path])
	}
	result.SourceSHA256 = hex.EncodeToString(hash.Sum(nil))
	files, err = renderSources(files, enabled, wiringManifest)
	if err != nil {
		return result, err
	}
	if negative {
		if name != "all" {
			return result, errors.New("negative probe requires case all")
		}
		if err := injectBrokenWiring(files); err != nil {
			return result, err
		}
	}
	workspace, err := newWiringWorkspace("")
	if err != nil {
		return result, err
	}
	defer func() { resultErr = errors.Join(resultErr, workspace.close()) }()
	if err := workspace.write(files, name == "removed"); err != nil {
		return result, err
	}
	ctx, cancel := context.WithTimeout(parent, 300*time.Second)
	defer cancel()
	env := wiringEnvironment(os.Environ(), workspace.path)
	output := filepath.Join(workspace.path, "test-binaries")
	if err := os.MkdirAll(output, 0700); err != nil {
		return result, err
	}
	commands := [][]string{
		{"go", "build", "-mod=readonly", "-trimpath", "-o", filepath.Join(workspace.path, "app"), "."},
		{"go", "test", "-c", "-mod=readonly", "-vet=off", "-trimpath", "-tags=integration", "-o", output + string(os.PathSeparator), "./cmd", "./internal/bootstrap", "./internal/platform/config"},
	}
	for _, argv := range commands {
		step, err := executeWiringStep(ctx, workspace.path, env, argv)
		result.Steps = append(result.Steps, step)
		if err != nil {
			return result, err
		}
	}
	return result, nil
}

func main() {
	name := flag.String("case", "", "default, taskqueue, upload, outbound, auditlog, all, removed; empty runs all")
	negative := flag.Bool("negative", false, "case all only: deliberately break one copied call; build must fail")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected positional arguments")
		os.Exit(2)
	}
	if *negative && *name != "all" {
		fmt.Fprintln(os.Stderr, "negative probe requires -case all")
		os.Exit(2)
	}
	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	names := wiringCases
	if *name != "" {
		names = []string{*name}
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	for _, item := range names {
		result, err := runWiringCase(ctx, root, item, *negative)
		if encodeErr := json.NewEncoder(os.Stdout).Encode(result); encodeErr != nil {
			fmt.Fprintln(os.Stderr, encodeErr)
			os.Exit(1)
		}
		if err != nil {
			os.Exit(1)
		}
	}
}
