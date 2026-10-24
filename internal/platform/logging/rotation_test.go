package logging

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/logger/v2"
)

func TestFileLoggersHonorSizeLimit(t *testing.T) {
	dir := t.TempDir()
	owner := mustOpen(t, Options{Config: &config.Config{Log: config.LogConfig{
		Path: filepath.Join(dir, "application"), Level: "info", FileStdout: 1,
		MaxSize: 1, MaxAge: 1, MaxBackups: 2,
	}}})
	t.Cleanup(func() {
		if err := owner.Close(context.WithoutCancel(t.Context())); err != nil {
			t.Error(err)
		}
	})

	line := strings.Repeat("x", 8192)
	for range 160 {
		logger.Info(line)
		owner.AccessLogger().Info(line)
	}
	if err := owner.Close(t.Context()); err != nil {
		t.Fatal(err)
	}
	files, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, file := range files {
		info, err := file.Info()
		if err != nil {
			t.Fatal(err)
		}
		kind := "main"
		if strings.Contains(file.Name(), "_access") {
			kind = "access"
		}
		counts[kind]++
		if info.Size() > 1<<20 {
			t.Errorf("%s log %s has %d bytes, exceeding configured 1 MiB", kind, file.Name(), info.Size())
		}
		if strings.HasSuffix(file.Name(), ".gz") {
			t.Errorf("compression is disabled, found %s", file.Name())
		}
	}
	for _, kind := range []string{"main", "access"} {
		if counts[kind] < 2 {
			t.Errorf("%s logs did not rotate: %d file(s)", kind, counts[kind])
		}
	}
}
