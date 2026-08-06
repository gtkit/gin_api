package bootstrap

import (
	"crypto/tls"
	"net"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/testutil"
)

// TestServeTLSUsesPreloadedCertificateAfterFilesRemoved 钉住"校验后的证书被直接使用"：
// prebind 装载并校验证书后**删除证书文件**，服务仍能完成真实 TLS 握手——
// 文件已不存在还能握手，即证明服务期没有从路径二次装载。
func TestServeTLSUsesPreloadedCertificateAfterFilesRemoved(t *testing.T) {
	testutil.RequireLoopback(t)

	certPath, keyPath := writeSelfSignedCert(t, time.Now().Add(-time.Hour), time.Now().Add(24*time.Hour))
	cfg := HTTPConfig{
		Host: "127.0.0.1", Port: "0",
		EnableTLS: true, CertFile: certPath, KeyFile: keyPath,
		TLSConfig:         newTLSConfig(),
		ReadHeaderTimeout: time.Second, ReadTimeout: time.Second,
		IdleTimeout: time.Second, ShutdownTimeout: time.Second,
	}

	bound, err := prebindHTTP(cfg)
	if err != nil {
		t.Fatalf("prebindHTTP: %v", err)
	}

	// 删除证书文件：此后任何路径装载都不可能成功。
	if err := os.Remove(certPath); err != nil {
		t.Fatalf("remove cert file: %v", err)
	}
	if err := os.Remove(keyPath); err != nil {
		t.Fatalf("remove key file: %v", err)
	}

	cfg.TLSConfig.Certificates = []tls.Certificate{*bound.certificate}
	server := &http.Server{
		Handler:           http.NotFoundHandler(),
		TLSConfig:         cfg.TLSConfig,
		ReadHeaderTimeout: time.Second,
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- serveHTTPServer(server, cfg, bound.listener) }()

	addr := bound.listener.Addr().String()
	var conn *tls.Conn
	deadline := time.Now().Add(3 * time.Second)
	for {
		// 自签测试证书跳过链校验：握手能否完成才是断言对象。
		conn, err = tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true})
		if err == nil || time.Now().After(deadline) {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("TLS handshake failed after cert files were removed: %v", err)
	}
	_ = conn.Close()

	if err := server.Close(); err != nil {
		t.Fatalf("close server: %v", err)
	}
	select {
	case err := <-serveErr:
		if err != nil {
			t.Fatalf("serveHTTPServer returned %v, want nil after close", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("serveHTTPServer did not return after server close")
	}
}

// TestServeTLSFailsWithoutLoadedCertificate 钉住 fail-closed：
// 启用 TLS 但没有已装载证书时明确报错，不回退到路径装载。
func TestServeTLSFailsWithoutLoadedCertificate(t *testing.T) {
	testutil.RequireLoopback(t)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = listener.Close() }()

	cfg := HTTPConfig{
		Host: "127.0.0.1", Port: "0",
		EnableTLS: true, TLSConfig: newTLSConfig(),
	}
	err = serveHTTPServer(&http.Server{ReadHeaderTimeout: time.Second}, cfg, listener)
	if err == nil {
		t.Fatal("serveHTTPServer started TLS serving without any loaded certificate")
	}
	if !strings.Contains(err.Error(), "no certificate loaded") {
		t.Fatalf("error %q does not explain the missing loaded certificate", err)
	}
}
