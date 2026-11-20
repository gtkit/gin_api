package upload

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gtkit/gouploader"
	goredis "github.com/redis/go-redis/v9"
)

// validUploaderOptions 给出本地驱动的最小合法装配参数。
func validUploaderOptions(t *testing.T, client goredis.UniversalClient) UploaderOptions {
	t.Helper()

	root := t.TempDir()
	return UploaderOptions{
		Dir:          root + "/files",
		ChunkDir:     root + "/chunks",
		MaxFileSize:  1 << 20,
		AllowedTypes: []string{".txt"},
		Redis:        client,
		KeyPrefix:    "test:",
	}
}

// TestNewUploaderRequiresExplicitSafety 钉住统一入口的安全参数必填：
// 全驱动要 chunk_dir/上限/白名单/Redis，local 另要 dir，驱动配置非法即失败。
func TestNewUploaderRequiresExplicitSafety(t *testing.T) {
	t.Parallel()

	server := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	tests := []struct {
		name   string
		mutate func(*UploaderOptions)
	}{
		{"empty chunk dir", func(o *UploaderOptions) { o.ChunkDir = " " }},
		{"zero max size", func(o *UploaderOptions) { o.MaxFileSize = 0 }},
		{"empty allowed types", func(o *UploaderOptions) { o.AllowedTypes = nil }},
		{"nil redis", func(o *UploaderOptions) { o.Redis = nil }},
		{"local missing dir", func(o *UploaderOptions) { o.Dir = "" }},
		{"unknown driver", func(o *UploaderOptions) { o.Storage.Driver = "gcs" }},
		{"cloud missing credentials", func(o *UploaderOptions) {
			o.Storage = StorageConfig{Driver: DriverS3, S3: S3StorageConfig{Endpoint: "minio.example.test", Bucket: "b"}}
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			o := validUploaderOptions(t, client)
			tt.mutate(&o)
			if _, err := NewUploader(o); err == nil {
				t.Fatal("NewUploader() = nil error, want validation failure")
			}
		})
	}
}

// TestNewUploaderChunkSessionsLiveInRedis 钉住装配真实可用：
// 分片会话经 Redis 持久化（键在项目前缀下），init→传片→resume→merge
// 全链路走通，合并对象经 Storage 接口读回字节一致。
func TestNewUploaderChunkSessionsLiveInRedis(t *testing.T) {
	t.Parallel()

	server := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	uploader, err := NewUploader(validUploaderOptions(t, client),
		// 测试用小分片；生产默认 5MB 下限不变。
		gouploader.WithChunkSize(5, 1, 1<<20),
	)
	if err != nil {
		t.Fatalf("NewUploader() = %v, want nil", err)
	}
	t.Cleanup(uploader.Close)

	const payload = "hello upload"
	sum := sha256.Sum256([]byte(payload))
	fileHash := hex.EncodeToString(sum[:])
	ctx := gouploader.ContextWithUploadInfo(t.Context(), gouploader.UploadInfo{UserID: "7"})

	init, err := uploader.ChunkService().InitUpload(ctx, &gouploader.ChunkInitRequest{
		FileName: "a.txt", FileSize: int64(len(payload)), ChunkSize: 5, FileHash: fileHash,
	})
	if err != nil {
		t.Fatalf("InitUpload() = %v", err)
	}
	if init.TotalChunks != 3 {
		t.Fatalf("TotalChunks = %d, want 3", init.TotalChunks)
	}
	if !slices.ContainsFunc(server.Keys(), func(key string) bool {
		return strings.HasPrefix(key, "test:gu:sess:")
	}) {
		t.Fatalf("会话键未落入 Redis（前缀 test:gu:sess:）；实际键: %v", server.Keys())
	}

	for i := range 2 {
		part := payload[i*5 : (i+1)*5]
		if _, err := uploader.ChunkService().UploadChunk(ctx, init.UploadID, i, strings.NewReader(part), int64(len(part)), ""); err != nil {
			t.Fatalf("UploadChunk(%d) = %v", i, err)
		}
	}

	resume, err := uploader.ChunkService().Resume(ctx, &gouploader.ResumeRequest{FileHash: fileHash})
	if err != nil {
		t.Fatalf("Resume() = %v", err)
	}
	if resume.UploadID != init.UploadID || len(resume.UploadedParts) != 2 || len(resume.MissingParts) != 1 {
		t.Fatalf("Resume() = %+v, want 同一会话且 2 已传 1 缺失", resume)
	}

	// 会话归属：他人身份操作同一会话必须被拒（钉住 WithUserIDFn 装配）。
	otherCtx := gouploader.ContextWithUploadInfo(t.Context(), gouploader.UploadInfo{UserID: "8"})
	if _, err := uploader.ChunkService().UploadChunk(otherCtx, init.UploadID, 2, strings.NewReader("xx"), 2, ""); !errors.Is(err, gouploader.ErrUnauthorized) {
		t.Fatalf("cross-owner UploadChunk() = %v, want ErrUnauthorized", err)
	}

	if _, err := uploader.ChunkService().UploadChunk(ctx, init.UploadID, 2, strings.NewReader(payload[10:]), 2, ""); err != nil {
		t.Fatalf("UploadChunk(2) = %v", err)
	}
	result, err := uploader.ChunkService().Merge(ctx, init.UploadID)
	if err != nil {
		t.Fatalf("Merge() = %v", err)
	}
	rc, err := uploader.Storage().Get(ctx, result.StorageKey)
	if err != nil {
		t.Fatalf("Get() = %v", err)
	}
	defer func() { _ = rc.Close() }()
	got, err := io.ReadAll(rc)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if !bytes.Equal(got, []byte(payload)) {
		t.Fatalf("read back = %q, want %q", got, payload)
	}
}

// TestNewUploaderSweepsStaleAssembleFiles 钉住本地驱动的装配残留回收：
// 中断的代理合并留下的 *.assembling 文件由孤儿清理循环删除。
func TestNewUploaderSweepsStaleAssembleFiles(t *testing.T) {
	t.Parallel()

	server := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	o := validUploaderOptions(t, client)
	uploader, err := NewUploader(o,
		// 测试用极短 TTL/间隔；生产默认 24h/1h。
		gouploader.WithOrphanCleanup(time.Nanosecond, 20*time.Millisecond),
	)
	if err != nil {
		t.Fatalf("NewUploader() = %v", err)
	}
	t.Cleanup(uploader.Close)

	stale := o.Dir + "/stale.txt.assembling"
	if err := os.WriteFile(stale, []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(stale); errors.Is(err, os.ErrNotExist) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("stale assemble file was not swept")
}

// TestNewUploaderCallerCanReplaceBackgroundErrorSink 钉住默认后台错误出口
// 排在 extra 之前：装配方回调收到启动期安全建议（plainHashStore 触发，
// 见 background_error_test.go），与 NewLocalUploader 同款语义。
func TestNewUploaderCallerCanReplaceBackgroundErrorSink(t *testing.T) {
	t.Parallel()

	server := miniredis.RunT(t)
	client := goredis.NewClient(&goredis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	var captured []string
	uploader, err := NewUploader(validUploaderOptions(t, client),
		gouploader.WithHashStore(plainHashStore{}),
		gouploader.WithOnBackgroundError(func(err error) {
			captured = append(captured, err.Error())
		}),
	)
	if err != nil {
		t.Fatalf("NewUploader() = %v", err)
	}
	t.Cleanup(uploader.Close)

	if !slices.ContainsFunc(captured, func(msg string) bool {
		return strings.Contains(msg, "UserScopedHashStore")
	}) {
		t.Fatalf("装配方回调未收到启动期安全建议: %v", captured)
	}
}
