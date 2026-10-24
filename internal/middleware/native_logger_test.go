package middleware

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gtkit/logger/v2"
)

// newNativeTestLogger captures real SDK output; it does not replace log methods.
func newNativeTestLogger(t testing.TB, level string) (*logger.Logger, func() []map[string]any) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "native.jsonl")
	output, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	previousOutput := os.Stdout
	os.Stdout = output
	instance, err := logger.New(logger.WithConsole(true), logger.WithFile(false), logger.WithOutJSON(true), logger.WithLevel(level), logger.WithReplaceGlobals(false))
	os.Stdout = previousOutput
	if err != nil {
		if closeErr := output.Close(); closeErr != nil {
			t.Errorf("close log output: %v", closeErr)
		}
		t.Fatal(err)
	}
	previous := logger.Default()
	logger.SetDefault(instance)
	t.Cleanup(func() {
		logger.SetDefault(previous)
		instance.Sync()
		if err := output.Close(); err != nil {
			t.Error(err)
		}
	})
	return instance, func() []map[string]any {
		t.Helper()
		contents, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var records []map[string]any
		for line := range strings.Lines(string(contents)) {
			var record map[string]any
			if err := json.Unmarshal([]byte(line), &record); err != nil {
				t.Fatal(err)
			}
			records = append(records, record)
		}
		return records
	}
}
