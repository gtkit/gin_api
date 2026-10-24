package bootstrap

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gtkit/gin-api/internal/testutil"
)

// writeSelfSignedCert 生成一对自签名证书文件，有效期由调用方指定。
//
// 用真实的密钥对而不是固定 fixture：固定 fixture 迟早会过期，
// 于是一个与本次改动无关的测试会在某天突然失败。
func writeSelfSignedCert(t *testing.T, notBefore, notAfter time.Time) (certPath, keyPath string) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}

	template := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "localhost"},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		DNSNames:     []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}

	dir := t.TempDir()
	certPath = filepath.Join(dir, "cert.pem")
	keyPath = filepath.Join(dir, "key.pem")

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(certPath, certPEM, 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	return certPath, keyPath
}

// TestPrebindBindsPortBeforeServing 验证预绑定确实占住了端口。
//
// 这正是它存在的意义：端口占用必须在 worker 启动屏障**之前**暴露，
// 否则 required one-shot 的不可逆副作用（数据修复、发通知）会先于失败发生。
func TestPrebindBindsPortBeforeServing(t *testing.T) {
	testutil.RequireLoopback(t)
	t.Parallel()

	bound, err := prebindHTTP(HTTPConfig{Host: "127.0.0.1", Port: "0"})
	if err != nil {
		t.Fatalf("prebindHTTP: %v", err)
	}
	defer func() {
		if err := bound.Close(); err != nil {
			t.Errorf("bound.Close(): %v", err)
		}
	}()

	addr, ok := bound.listener.Addr().(*net.TCPAddr)

	if !ok {
		t.Fatalf("listener address has type %T", bound.listener.Addr())
	}
	// 同一端口再绑一次必须失败——证明第一次真的占住了。
	second, err := prebindHTTP(HTTPConfig{Host: "127.0.0.1", Port: strconv.Itoa(addr.Port)})
	if err == nil {
		if err := second.Close(); err != nil {
			t.Errorf("second.Close(): %v", err)
		}
		t.Fatal("binding an already-bound port succeeded; the prebind did not actually hold it")
	}
}

// TestPrebindReportsPortConflict 验证端口冲突以可读错误返回而不是 panic。
func TestPrebindReportsPortConflict(t *testing.T) {
	testutil.RequireLoopback(t)
	t.Parallel()

	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() {
		if err := held.Close(); err != nil {
			t.Errorf("held.Close(): %v", err)
		}
	}()

	addr, ok := held.Addr().(*net.TCPAddr)

	if !ok {
		t.Fatalf("listener address has type %T", held.Addr())
	}

	port := strconv.Itoa(addr.Port)
	if _, err := prebindHTTP(HTTPConfig{Host: "127.0.0.1", Port: port}); err == nil {
		t.Fatal("prebindHTTP succeeded on an occupied port")
	} else if !strings.Contains(err.Error(), port) {
		t.Fatalf("error %q does not name the conflicting address", err)
	}
}

// TestPrebindTLSValidation 覆盖证书有效期的三档处置。
//
// 三者的处置完全不同，因此必须分别断言：
//   - 未生效 / 已过期 → 启动失败（每个握手都会失败，服务起来也没用）；
//   - 临近过期        → 正常启动（证书**现在仍然有效**，拒绝启动等于
//     用一次确定的停机换一个尚未发生的问题）。
func TestPrebindTLSValidation(t *testing.T) {
	testutil.RequireLoopback(t)
	t.Parallel()

	now := time.Now()

	cases := []struct {
		name      string
		notBefore time.Time
		notAfter  time.Time
		wantErr   string
	}{
		{
			name:      "valid certificate",
			notBefore: now.Add(-time.Hour),
			notAfter:  now.Add(365 * 24 * time.Hour),
		},
		{
			name:      "near expiry still starts",
			notBefore: now.Add(-time.Hour),
			notAfter:  now.Add(24 * time.Hour),
		},
		{
			name:      "expired certificate rejected",
			notBefore: now.Add(-48 * time.Hour),
			notAfter:  now.Add(-time.Hour),
			wantErr:   "expired",
		},
		{
			name:      "not yet valid certificate rejected",
			notBefore: now.Add(24 * time.Hour),
			notAfter:  now.Add(48 * time.Hour),
			wantErr:   "not valid until",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			certPath, keyPath := writeSelfSignedCert(t, tc.notBefore, tc.notAfter)
			cfg := HTTPConfig{
				Host: "127.0.0.1", Port: "0",
				EnableTLS: true, CertFile: certPath, KeyFile: keyPath,
			}

			bound, err := prebindHTTP(cfg)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("prebindHTTP: %v", err)
				}
				defer func() {
					if err := bound.Close(); err != nil {
						t.Errorf("bound.Close(): %v", err)
					}
				}()
				if bound.certificate == nil {
					t.Fatal("TLS enabled but no certificate was loaded")
				}
				return
			}

			if err == nil {
				if err := bound.Close(); err != nil {
					t.Errorf("bound.Close(): %v", err)
				}
				t.Fatalf("prebindHTTP succeeded with %s certificate", tc.name)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("error %q does not mention %q", err, tc.wantErr)
			}
		})
	}
}

// TestPrebindTLSFailureDoesNotLeaveListener 验证证书校验失败时不留下已绑定端口。
//
// 留下的话，进程退出前那个端口一直被占着，紧接着的重启会因端口冲突再次失败——
// 一个配置问题因此变成一次需要人工介入的故障。
func TestPrebindTLSFailureDoesNotLeaveListener(t *testing.T) {
	testutil.RequireLoopback(t)
	t.Parallel()

	now := time.Now()
	certPath, keyPath := writeSelfSignedCert(t, now.Add(-48*time.Hour), now.Add(-time.Hour))

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("listener address has type %T", listener.Addr())
	}
	port := strconv.Itoa(addr.Port)
	if err := listener.Close(); err != nil {
		t.Fatalf("close probe listener: %v", err)
	}

	cfg := HTTPConfig{
		Host: "127.0.0.1", Port: port,
		EnableTLS: true, CertFile: certPath, KeyFile: keyPath,
	}
	if _, err := prebindHTTP(cfg); err == nil {
		t.Fatal("prebindHTTP succeeded with an expired certificate")
	}

	// 端口必须仍然可绑：证书先验、端口后绑，失败路径上根本没绑过。
	again, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		t.Fatalf("port %s is still held after a failed prebind: %v", port, err)
	}
	if err := again.Close(); err != nil {
		t.Errorf("again.Close(): %v", err)
	}
}

// TestPrebindMissingCertFiles 验证密钥文件缺失时报错且不泄漏文件内容。
func TestPrebindMissingCertFiles(t *testing.T) {
	t.Parallel()

	cfg := HTTPConfig{
		Host: "127.0.0.1", Port: "0",
		EnableTLS: true,
		CertFile:  filepath.Join(t.TempDir(), "missing.pem"),
		KeyFile:   filepath.Join(t.TempDir(), "missing.key"),
	}

	_, err := prebindHTTP(cfg)
	if err == nil {
		t.Fatal("prebindHTTP succeeded with missing key pair files")
	}
	if !strings.Contains(err.Error(), "load tls key pair") {
		t.Fatalf("error %q does not identify the key pair loading step", err)
	}
}
