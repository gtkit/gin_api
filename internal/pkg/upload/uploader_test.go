package upload

import (
	"bytes"
	"io"
	"strings"
	"testing"

	"github.com/gtkit/gouploader"
)

// TestNewLocalUploaderRequiresExplicitSafety 钉住「安全决策不设默认值」：
// 目录、大小上限、类型白名单三项缺一不可——gouploader 自身的默认
// （10GB 上限、空白名单全放行）不允许经本入口静默继承。
func TestNewLocalUploaderRequiresExplicitSafety(t *testing.T) {
	t.Parallel()

	valid := LocalOptions{Dir: t.TempDir(), MaxFileSize: 1 << 20, AllowedTypes: []string{".jpg"}}
	tests := []struct {
		name   string
		mutate func(*LocalOptions)
	}{
		{"empty dir", func(o *LocalOptions) { o.Dir = "  " }},
		{"zero max size", func(o *LocalOptions) { o.MaxFileSize = 0 }},
		{"negative max size", func(o *LocalOptions) { o.MaxFileSize = -1 }},
		{"empty allowed types", func(o *LocalOptions) { o.AllowedTypes = nil }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			o := valid
			o.AllowedTypes = append([]string(nil), valid.AllowedTypes...)
			tt.mutate(&o)
			if _, err := NewLocalUploader(o); err == nil {
				t.Fatal("NewLocalUploader() = nil error, want explicit-safety validation failure")
			}
		})
	}
}

// TestNewLocalUploaderRoundTrip 钉住装配真实可用：构造成功后经 Storage
// 接口完成一次落盘读回往返（纯文件系统，受限环境可跑）。
func TestNewLocalUploaderRoundTrip(t *testing.T) {
	t.Parallel()

	uploader, err := NewLocalUploader(LocalOptions{
		Dir:          t.TempDir(),
		MaxFileSize:  1 << 20,
		AllowedTypes: []string{".txt"},
	}, gouploader.WithChunkTempDir(t.TempDir())) // 分片暂存也进临时目录，不污染工作区
	if err != nil {
		t.Fatalf("NewLocalUploader() = %v, want nil", err)
	}
	defer uploader.Close()

	const payload = "hello upload"
	if err := uploader.Storage().Save(t.Context(), "a/b.txt", strings.NewReader(payload), int64(len(payload))); err != nil {
		t.Fatalf("Save() = %v", err)
	}
	rc, err := uploader.Storage().Get(t.Context(), "a/b.txt")
	if err != nil {
		t.Fatalf("Get() = %v", err)
	}
	defer func() {
		if err := rc.Close(); err != nil {
			t.Errorf("rc.Close(): %v", err)
		}
	}()
	var buf bytes.Buffer
	if _, err := io.Copy(&buf, rc); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if buf.String() != payload {
		t.Fatalf("read back = %q, want %q", buf.String(), payload)
	}
}
