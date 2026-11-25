package redis

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gtkit/gin-api/internal/platform/config"
)

func TestRedisTLSVerificationAndReconnect(t *testing.T) {
	const name = "redis.test"
	valid, ca := newRedisTLSFixture(t, name, false)
	expired, expiredCA := newRedisTLSFixture(t, name, true)
	for _, limit := range []int{0, 1} {
		t.Run(strconv.Itoa(limit), func(t *testing.T) {
			srv, err := miniredis.RunTLS(valid)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(srv.Close)
			cfg := config.RedisConfig{
				Addr: srv.Addr(), DBs: []int{2}, PoolSize: 1, MaxActiveConns: limit,
				DialTimeoutMS: 200,
				TLS:           config.ClientTLSConfig{Enabled: true, ServerName: name, CAFile: ca},
			}
			client, err := OpenRedis(t.Context(), cfg)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := client.Close(); err != nil {
					t.Error(err)
				}
			})
			if err := client.HealthCheck(t.Context()); err != nil {
				t.Fatal(err)
			}
			// Close the listener and all sockets, then force both pools to dial new TLS connections.
			srv.Close()
			if err := srv.StartAddrTLS(cfg.Addr, valid); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			if err := client.HealthCheck(ctx); err != nil {
				t.Fatalf("reconnect after listener restart: %v", err)
			}
			for _, tt := range []struct {
				name, serverName, ca string
				cert                 *tls.Config
				check                func(error) bool
			}{
				{"wrong hostname", "other.test", ca, valid, func(err error) bool { _, ok := errors.AsType[x509.HostnameError](err); return ok }},
				{"unknown CA", name, expiredCA, valid, func(err error) bool { _, ok := errors.AsType[x509.UnknownAuthorityError](err); return ok }},
				{"expired certificate", name, expiredCA, expired, func(err error) bool {
					invalid, ok := errors.AsType[x509.CertificateInvalidError](err)
					return ok && invalid.Reason == x509.Expired
				}},
			} {
				t.Run(tt.name, func(t *testing.T) {
					srv, err := miniredis.RunTLS(tt.cert)
					if err != nil {
						t.Fatal(err)
					}
					t.Cleanup(srv.Close)
					bad := cfg
					bad.Addr, bad.TLS.ServerName, bad.TLS.CAFile = srv.Addr(), tt.serverName, tt.ca
					got, err := OpenRedis(t.Context(), bad)
					if got != nil {
						_ = got.Close()
						t.Fatal("unverified Redis connection was published")
					}
					if !tt.check(err) {
						t.Fatalf("unexpected certificate error: %v", err)
					}
				})
			}
		})
	}
}

func newRedisTLSFixture(t *testing.T, name string, expired bool) (*tls.Config, string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	cert := &x509.Certificate{
		SerialNumber: big.NewInt(1), DNSNames: []string{name},
		NotBefore: now.Add(-2 * time.Hour), NotAfter: now.Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true,
		KeyUsage:    x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	if expired {
		cert.NotAfter = now.Add(-time.Hour)
	}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	ca := filepath.Join(t.TempDir(), "ca.pem")
	if err := os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	return &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}},
	}, ca
}
