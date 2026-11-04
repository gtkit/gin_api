//go:build ignore

// security-source scans the current tracked and non-ignored source without copying it.
package main

import (
	"bytes"
	"context"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

const sourceLimit = 128 << 20

type sourcePart struct {
	File        string
	First, Last int
}

type secretFinding struct {
	RuleID      string
	File        string
	StartLine   int
	EndLine     int
	Fingerprint string
}

func collectSource(ctx context.Context) ([]byte, []sourcePart, error) {
	cmd := exec.CommandContext(ctx, "git", "ls-files", "-z", "--cached", "--others", "--exclude-standard")
	files, err := cmd.Output()
	if err != nil {
		return nil, nil, fmt.Errorf("enumerate source: %w", err)
	}
	names := strings.Split(strings.TrimSuffix(string(files), "\x00"), "\x00")
	slices.Sort(names)
	names = slices.Compact(names)
	var stream bytes.Buffer
	var parts []sourcePart
	line := 1
	for _, name := range names {
		if name == "" {
			continue
		}
		if !filepath.IsLocal(name) {
			return nil, nil, errors.New("source path escapes project")
		}
		info, err := os.Lstat(name)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, nil, fmt.Errorf("inspect source %s: %w", name, err)
		}
		if !info.Mode().IsRegular() {
			return nil, nil, fmt.Errorf("source %s is not a regular file", name)
		}
		remaining := int64(sourceLimit - stream.Len() - 1)
		if remaining < 0 || info.Size() > remaining {
			return nil, nil, errors.New("source scan exceeds 128 MiB; split the reviewed scope explicitly")
		}
		file, err := os.Open(name)
		if err != nil {
			return nil, nil, fmt.Errorf("read source %s: %w", name, err)
		}
		data, readErr := io.ReadAll(io.LimitReader(file, remaining+1))
		if err := errors.Join(readErr, file.Close()); err != nil {
			return nil, nil, err
		}
		if int64(len(data)) > remaining {
			return nil, nil, errors.New("source changed beyond the scan byte limit")
		}
		stream.Write(data)
		stream.WriteByte('\n')
		count := bytes.Count(data, []byte{'\n'}) + 1
		parts = append(parts, sourcePart{File: filepath.ToSlash(name), First: line, Last: line + count - 1})
		line += count
	}
	return stream.Bytes(), parts, nil
}

func mapFindings(data []byte, parts []sourcePart) ([]secretFinding, error) {
	var findings []secretFinding
	if err := json.Unmarshal(data, &findings); err != nil {
		return nil, fmt.Errorf("decode secret report: %w", err)
	}
	if findings == nil {
		return nil, errors.New("secret scanner did not produce an array")
	}
	for i := range findings {
		f := &findings[i]
		if f.RuleID == "" || f.StartLine < 1 || f.EndLine < f.StartLine {
			return nil, errors.New("invalid secret finding location or rule")
		}
		matched := false
		for _, p := range parts {
			if f.StartLine >= p.First && f.EndLine <= p.Last {
				f.File, f.StartLine, f.EndLine = p.File, f.StartLine-p.First+1, f.EndLine-p.First+1
				f.Fingerprint = fmt.Sprintf("%s:%s:%d", f.File, f.RuleID, f.StartLine)
				matched = true
				break
			}
		}
		if !matched {
			return nil, errors.New("secret finding spans an unmapped source boundary")
		}
	}
	return findings, nil
}

func validVulnerabilityReport(data []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	configured := false
	for {
		var message map[string]json.RawMessage
		err := decoder.Decode(&message)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("decode vulnerability report: %w", err)
		}
		if config, ok := message["config"]; ok {
			var value map[string]json.RawMessage
			if err := json.Unmarshal(config, &value); err != nil || value == nil {
				return errors.New("invalid vulnerability configuration record")
			}
			configured = true
		}
	}
	if !configured {
		return errors.New("vulnerability scanner returned no configuration record")
	}
	return nil
}

func writeSafeReport(path string, data []byte) error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	data = bytes.ReplaceAll(data, []byte(filepath.ToSlash(root)), []byte("."))
	data = bytes.ReplaceAll(data, []byte(filepath.ToSlash(home)), []byte("<home>"))
	return os.WriteFile(path, data, 0600)
}

func scanSource(ctx context.Context) error {
	input, parts, err := collectSource(ctx)
	if err != nil {
		return err
	}
	const output = ".cache/security"
	if err := os.MkdirAll(output, 0700); err != nil {
		return err
	}
	version, err := exec.CommandContext(ctx, "gitleaks", "version").Output()
	if err != nil {
		return fmt.Errorf("gitleaks is required: %w", err)
	}
	lock, err := os.ReadFile("security/toolchain.mk")
	if err != nil {
		return err
	}
	lockedVersion := strings.TrimPrefix(strings.TrimSpace(string(version)), "v")
	if !bytes.Contains(lock, []byte("gitleaks:v"+lockedVersion+"@")) {
		binary, err := exec.LookPath("gitleaks")
		if err != nil {
			return err
		}
		info, err := buildinfo.ReadFile(binary)
		if err != nil || info.Main.Path != "github.com/zricethezav/gitleaks/v8" ||
			!bytes.Contains(lock, []byte("gitleaks:"+info.Main.Version+"@")) {
			return errors.New("gitleaks version does not match security/toolchain.mk")
		}
	}
	vuln, scanErr := exec.CommandContext(ctx, "go", "tool", "govulncheck", "-format=json", "./...").Output()
	if err := validVulnerabilityReport(vuln); err != nil {
		return errors.Join(scanErr, err)
	}
	if err := writeSafeReport(filepath.Join(output, "govulncheck.json"), vuln); err != nil {
		return err
	}
	if scanErr != nil {
		return fmt.Errorf("vulnerability scan failed; inspect the redacted report: %w", scanErr)
	}
	// Raw scanner fields stay in memory; only mapped rule/location fields are saved.
	command := exec.CommandContext(ctx, "gitleaks", "stdin", "--no-banner", "--redact=100", "--exit-code=0", "--report-format=json", "--report-path=-")
	command.Stdin = bytes.NewReader(input)
	raw, err := command.Output()
	if err != nil {
		return fmt.Errorf("secret scanner failed: %w", err)
	}
	findings, err := mapFindings(raw, parts)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(findings, "", "  ")
	if err != nil {
		return err
	}
	if err := writeSafeReport(filepath.Join(output, "gitleaks.json"), data); err != nil {
		return err
	}
	if len(findings) > 0 {
		return fmt.Errorf("secret scan found %d finding(s); inspect rule and source locations", len(findings))
	}
	return nil
}

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := scanSource(ctx); err != nil {
		if _, writeErr := fmt.Fprintln(os.Stderr, "source security check:", err); writeErr != nil {
			os.Exit(2)
		}
		os.Exit(1)
	}
	if _, err := fmt.Println("source security check passed"); err != nil {
		os.Exit(2)
	}
}
