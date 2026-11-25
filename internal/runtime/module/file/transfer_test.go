package file

import (
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/gtkit/gin-api/internal/module/file/application"
	"github.com/gtkit/gin-api/internal/optional/upload"
	"github.com/redis/go-redis/v9"
)

func TestRecoveryTransferKeepsSessionBindingAndMergeMetadata(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() {
		if err := client.Close(); err != nil {
			t.Error(err)
		}
	})
	root := t.TempDir()
	up, sessions, err := upload.NewRecoveryUploader(upload.UploaderOptions{
		Dir: filepath.Join(root, "files"), ChunkDir: filepath.Join(root, "chunks"),
		Redis: client, KeyPrefix: "transfer-test:", MaxFileSize: 1024, AllowedTypes: []string{".txt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(up.Close)
	ctx := t.Context()
	transfer := NewTransfer(up)
	checksum := fmt.Sprintf("%x", sha256.Sum256([]byte("hello")))
	init, err := transfer.ChunkInit(ctx, 7, &application.ChunkInitRequest{FileName: "a.txt", FileSize: 5, Checksum: checksum})
	if err != nil {
		t.Fatal(err)
	}
	lookup := recoverySessionLookup(sessions)
	before, bound, err := lookup(ctx, init.UploadID)
	if err != nil || !bound || before.UserID != "7" || before.FileName != "a.txt" ||
		before.FileSize != 5 || before.TotalChunks != 1 || before.Checksum != checksum || !before.Assemble || before.StorageKey == "" {
		t.Fatalf("session projection=%+v bound=%t err=%v", before, bound, err)
	}
	if _, err := transfer.UploadChunk(ctx, 8, init.UploadID, 0, strings.NewReader("other"), 5, ""); !errors.Is(err, application.ErrSessionOwner) {
		t.Fatalf("foreign chunk error: %v", err)
	}
	if _, err := transfer.UploadChunk(ctx, 7, init.UploadID, 0, strings.NewReader("hello"), 5, ""); err != nil {
		t.Fatal(err)
	}
	chunk, _, err := lookup(ctx, init.UploadID)
	if err != nil || chunk.UploadedCount != 1 {
		t.Fatalf("uploaded session projection=%+v err=%v", chunk, err)
	}
	result, err := transfer.Merge(ctx, 7, init.UploadID, nil)
	if err != nil || result.StorageKey != before.StorageKey || result.FileSize != 5 || result.Checksum != checksum {
		t.Fatalf("merged object=%+v err=%v", result, err)
	}
	after, bound, err := lookup(ctx, init.UploadID)
	if err != nil || !bound || !after.Merged || after.MergeKey != result.StorageKey {
		t.Fatalf("merged session projection=%+v bound=%t err=%v", after, bound, err)
	}
	stream, err := transfer.Get(ctx, result.StorageKey)
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(stream)
	if err := errors.Join(readErr, stream.Close()); err != nil || string(body) != "hello" {
		t.Fatalf("merged content=%q err=%v", body, err)
	}
}
