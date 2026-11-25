package http

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/module/file/application"
)

func multipartFixture(t *testing.T, content string, fields map[string]string) *http.Request {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "transfer.txt")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(part, content); err != nil {
		t.Fatal(err)
	}
	for name, value := range fields {
		if err := writer.WriteField(name, value); err != nil {
			t.Fatal(err)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/upload", &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())
	req.ContentLength = -1
	return req
}

func TestMultipartUsesBoundedSpillAndCountsChunkedBody(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	data := strings.Repeat("x", multipartMemoryBytes+4096)
	form, err := readTransferForm(multipartFixture(t, data, nil), int64(len(data)), dir, false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = form.payload.Close() })
	sum := sha256.Sum256([]byte(data))
	if form.payload.file == nil || form.payload.memory != nil || form.size != int64(len(data)) || form.checksum != hex.EncodeToString(sum[:]) {
		t.Fatalf("incorrect spill result: size=%d disk=%t memory=%d", form.size, form.payload.file != nil, len(form.payload.memory))
	}
	if entries, err := os.ReadDir(dir); err != nil || runtime.GOOS != "windows" && len(entries) != 0 {
		t.Fatalf("unexpected visible spill files: count=%d err=%v", len(entries), err)
	}
	r, err := form.payload.Reader()
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(r)
	if err != nil || string(got) != data {
		t.Fatalf("staged data: size=%d err=%v", len(got), err)
	}
	if _, err := readTransferForm(multipartFixture(t, data, nil), int64(len(data)-1), dir, false); !errors.As(err, new(*http.MaxBytesError)) {
		t.Fatalf("overlong file = %v", err)
	}
}

func TestMultipartChunkMetadataAndStagingErrors(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	form, err := readTransferForm(multipartFixture(t, "chunk", map[string]string{"upload_id": "session"}), 5, dir, true)
	if err != nil || form.chunk.UploadID != "session" || form.chunk.ChunkIndex != 0 {
		t.Fatalf("file-first, optional index: form=%+v err=%v", form, err)
	}
	if _, err := readTransferForm(multipartFixture(t, "chunk", map[string]string{"upload_id": strings.Repeat("x", multipartFieldBytes+1)}), 5, dir, true); !errors.Is(err, errMultipartMetadata) {
		t.Fatalf("oversized metadata = %v", err)
	}
	if _, err := readTransferForm(multipartFixture(t, strings.Repeat("x", multipartMemoryBytes+1), nil),
		multipartMemoryBytes+1, dir+"/unavailable", false); !errors.Is(err, application.ErrDependencyUnavailable) {
		t.Fatalf("staging I/O failure = %v", err)
	}
}

func TestMultipartSpillLeavesNoFileAfterProcessExit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("this verifies Unix unlink-while-open behavior")
	}
	const dirEnv = "GIN_API_MULTIPART_CRASH_DIR"
	if dir := os.Getenv(dirEnv); dir != "" {
		var spool uploadSpool
		spool.dir = dir
		if _, err := spool.Write(make([]byte, multipartMemoryBytes+1)); err != nil || spool.file == nil {
			t.Fatalf("spill before exit: %v", err)
		}
		os.Exit(89)
	}
	dir := t.TempDir()
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestMultipartSpillLeavesNoFileAfterProcessExit$")
	cmd.Env = append(os.Environ(), dirEnv+"="+dir)
	output, err := cmd.CombinedOutput()
	if exit, ok := errors.AsType[*exec.ExitError](err); !ok || exit.ExitCode() != 89 {
		t.Fatalf("spill child exit: %v\n%s", err, output)
	}
	if entries, err := os.ReadDir(dir); err != nil || len(entries) != 0 {
		t.Fatalf("after process exit: spill files=%d err=%v", len(entries), err)
	}
}
