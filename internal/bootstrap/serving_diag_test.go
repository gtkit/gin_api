package bootstrap

import (
	"fmt"
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestNewHTTPServerSetsErrorLog 钉住 net/http 内部错误的日志出口：
// ErrorLog 为空即裸 stderr——TLS 握手失败、请求解析错误的第一现场会绕过结构化日志。
func TestNewHTTPServerSetsErrorLog(t *testing.T) {
	gin.SetMode(gin.TestMode)
	srv := newHTTPServer(gin.New(), HTTPConfig{Host: "127.0.0.1", Port: "0"})

	hs, ok := srv.(*http.Server)
	if !ok {
		t.Fatalf("newHTTPServer returned %T, want *http.Server", srv)
	}
	if hs.ErrorLog == nil {
		t.Fatal("http.Server.ErrorLog is nil; internal errors would fall through to bare stderr")
	}
}

// TestLogWriterForwardsTrimmedLines 钉住行适配器契约：
// 去尾部空白后整行转发、空行丢弃、恒返回写入的全部字节数。
func TestLogWriterForwardsTrimmedLines(t *testing.T) {
	var got []string
	w := logWriter{logf: func(format string, args ...any) {
		got = append(got, fmt.Sprintf(format, args...))
	}}

	line := []byte("engine warning: something\n")
	n, err := w.Write(line)
	if err != nil {
		t.Fatalf("Write: %v", err)
	}
	if n != len(line) {
		t.Fatalf("Write returned %d, want %d (io.Writer 契约)", n, len(line))
	}
	if len(got) != 1 || got[0] != "engine warning: something" {
		t.Fatalf("forwarded lines = %q, want the trimmed line", got)
	}

	n, err = w.Write([]byte("\n"))
	if err != nil || n != 1 {
		t.Fatalf("blank Write = (%d, %v), want (1, nil)", n, err)
	}
	if len(got) != 1 {
		t.Fatalf("blank line was forwarded: %q", got)
	}
}

// TestInstallGinDiagnosticsTakesOverWriters 钉住引擎诊断输出的接管：
// 安装后两个包级写出口都指向结构化日志适配器。
func TestInstallGinDiagnosticsTakesOverWriters(t *testing.T) {
	origOut, origErr := gin.DefaultWriter, gin.DefaultErrorWriter
	defer func() {
		gin.DefaultWriter, gin.DefaultErrorWriter = origOut, origErr
	}()

	installGinDiagnostics()

	if _, ok := gin.DefaultWriter.(logWriter); !ok {
		t.Fatalf("gin.DefaultWriter = %T, want logWriter", gin.DefaultWriter)
	}
	if _, ok := gin.DefaultErrorWriter.(logWriter); !ok {
		t.Fatalf("gin.DefaultErrorWriter = %T, want logWriter", gin.DefaultErrorWriter)
	}
}
