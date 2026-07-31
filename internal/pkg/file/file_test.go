package file

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/platform/config"
)

func TestIsPathValidAllowsNestedPathInsideStorage(t *testing.T) {
	t.Parallel()

	storagePath := filepath.Join(t.TempDir(), "uploads")
	targetPath := filepath.Join(storagePath, "images", "2026-03-22", "avatar.png")

	if !IsPathValid(storagePath, targetPath) {
		t.Fatalf("expected nested path %q to be valid inside storage %q", targetPath, storagePath)
	}

	service := NewFileService(storagePath)
	if !service.isPathValid(targetPath) {
		t.Fatalf("expected service validator to allow nested path %q", targetPath)
	}
}

func TestIsPathValidRejectsPathOutsideStorage(t *testing.T) {
	t.Parallel()

	storagePath := filepath.Join(t.TempDir(), "uploads")
	targetPath := filepath.Join(storagePath, "..", "escape.txt")

	if IsPathValid(storagePath, targetPath) {
		t.Fatalf("expected path %q to be rejected", targetPath)
	}

	service := NewFileService(storagePath)
	if service.isPathValid(targetPath) {
		t.Fatalf("expected service validator to reject %q", targetPath)
	}
}

func TestMergeChunksSortsChunkNamesNumerically(t *testing.T) {
	storagePath := t.TempDir()
	service := NewFileService(storagePath)

	chunkDir := filepath.Join(storagePath, "chunks", "upload-1")
	if err := os.MkdirAll(chunkDir, 0o755); err != nil {
		t.Fatalf("mkdir chunk dir: %v", err)
	}

	chunks := map[string][]byte{
		"1.chunk":  []byte("A"),
		"2.chunk":  []byte("B"),
		"10.chunk": []byte("C"),
	}
	for name, content := range chunks {
		if err := os.WriteFile(filepath.Join(chunkDir, name), content, 0o600); err != nil {
			t.Fatalf("write chunk %s: %v", name, err)
		}
	}

	if err := service.MergeChunks("upload-1", "merged.txt"); err != nil {
		t.Fatalf("merge chunks: %v", err)
	}

	merged, err := os.ReadFile(filepath.Join(storagePath, "merged.txt"))
	if err != nil {
		t.Fatalf("read merged file: %v", err)
	}

	if !bytes.Equal(merged, []byte("ABC")) {
		t.Fatalf("expected merged content ABC, got %q", merged)
	}
}

func TestValidateFileTypeAllowsPlainTextAndResetsReader(t *testing.T) {
	t.Parallel()

	reader := bytes.NewReader([]byte("hello, world"))
	if err := ValidateFileType(reader); err != nil {
		t.Fatalf("expected plain text to be accepted, got error: %v", err)
	}

	position, err := reader.Seek(0, io.SeekCurrent)
	if err != nil {
		t.Fatalf("seek current: %v", err)
	}
	if position != 0 {
		t.Fatalf("expected reader position to reset to 0, got %d", position)
	}
}

func TestInitWithConfigSetsTimezone(t *testing.T) {
	resetFileHooksForTest()
	setTimeNowForTest(func() time.Time {
		return time.Date(2026, 3, 23, 4, 0, 0, 0, time.UTC)
	})

	InitWithConfig(&config.Config{App: config.AppConfig{Timezone: "Asia/Shanghai"}})
	got := TimenowInTimezone()

	if got.Location().String() != "Asia/Shanghai" {
		t.Fatalf("location = %s", got.Location())
	}
	if got.Hour() != 12 {
		t.Fatalf("hour = %d", got.Hour())
	}
}
