package bootstrap

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gtkit/logger/v2"
)

func TestHTTPErrorLogClassificationAndWindow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "http-log.jsonl")
	output, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	previousOutput := os.Stdout
	os.Stdout = output
	instance, err := logger.New(logger.WithConsole(true), logger.WithFile(false), logger.WithOutJSON(true), logger.WithLevel("info"), logger.WithReplaceGlobals(false))
	os.Stdout = previousOutput
	if err != nil {
		if closeErr := output.Close(); closeErr != nil {
			t.Error(closeErr)
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
	now := time.Unix(1, 0)
	writer := &httpErrorWriter{now: func() time.Time { return now }}
	message := []byte(tlsHandshakeErrorPrefix + "127.0.0.1:1: test failure\n")
	var group sync.WaitGroup
	for range 100 {
		group.Go(func() {
			if n, err := writer.Write(message); n != len(message) || err != nil {
				t.Errorf("Write = %d, %v", n, err)
			}
		})
	}
	group.Wait()
	for _, message := range []string{"unclassified HTTP error\n", "http: panic serving 127.0.0.1: test panic\n"} {
		if n, err := writer.Write([]byte(message)); n != len(message) || err != nil {
			t.Fatalf("Write = %d, %v", n, err)
		}
	}
	now = now.Add(tlsErrorLogInterval)
	if _, err := writer.Write(message); err != nil {
		t.Fatal(err)
	}
	other := &httpErrorWriter{now: func() time.Time { return now }}
	if _, err := other.Write(message); err != nil {
		t.Fatal(err)
	}
	instance.Sync()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var entries []map[string]any
	for line := range strings.Lines(string(contents)) {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatal(err)
		}
		entries = append(entries, entry)
	}
	if len(entries) != 5 {
		t.Fatalf("HTTP log entries = %d, want 5: %v", len(entries), entries)
	}
	for i, level := range []string{"WARN", "ERROR", "ERROR", "WARN", "WARN"} {
		if entries[i]["level"] != level {
			t.Errorf("entry %d level=%v want %s", i, entries[i]["level"], level)
		}
	}
	if entries[3]["suppressed"] != float64(99) {
		t.Fatalf("suppressed=%v, want 99", entries[3])
	}
	if httpServerErrorLog().Writer() == httpServerErrorLog().Writer() {
		t.Fatal("servers share error log state")
	}
}
