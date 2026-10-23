package auth

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gtkit/logger/v2"
)

func TestAuthAuditorPreservesRequestContext(t *testing.T) {
	// The native logger is global; this test deliberately runs serially.
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	output, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := output.Close(); err != nil {
			t.Error(err)
		}
	})
	instance, err := func() (*logger.Logger, error) {
		previousOutput := os.Stdout
		os.Stdout = output
		defer func() { os.Stdout = previousOutput }()
		return logger.New(logger.WithConsole(true), logger.WithFile(false), logger.WithOutJSON(true), logger.WithLevel("info"), logger.WithReplaceGlobals(false))
	}()
	if err != nil {
		t.Fatal(err)
	}
	previous := logger.Default()
	logger.SetDefault(instance)
	t.Cleanup(func() {
		logger.SetDefault(previous)
		instance.Sync()
	})

	auditor := authAuditor{}
	first := logger.ContextWithRequestID(t.Context(), "first-request")
	auditor.Auditf(first, "login succeeded: uid=%d", 42)
	second, cancel := context.WithCancel(logger.ContextWithRequestID(t.Context(), "second-request"))
	defer cancel()
	cancel()
	auditor.Failf(second, "login rejected: wrong password for uid=%d", 43)
	auditor.Auditf(first, "logout: %s", "access token revoked, no refresh token supplied")
	instance.Sync()

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
	want := []struct{ requestID, message, level string }{
		{"first-request", "login succeeded: uid=42", "INFO"},
		{"second-request", "login rejected: wrong password for uid=43", "WARN"},
		{"first-request", "logout: access token revoked, no refresh token supplied", "INFO"},
	}
	if len(records) != len(want) {
		t.Fatalf("log count = %d, want %d; output: %s", len(records), len(want), contents)
	}
	for i, expected := range want {
		got := records[i]
		if got["request_id"] != expected.requestID || got["msg"] != expected.message || got["level"] != expected.level {
			t.Errorf("log %d = %v, want request_id=%s msg=%s level=%s", i, got, expected.requestID, expected.message, expected.level)
		}
	}
}
