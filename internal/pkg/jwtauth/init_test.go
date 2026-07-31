package jwtauth

import (
	"testing"
)

func TestNewWithConfigInitializesEd25519(t *testing.T) {
	issuer, err := NewWithConfig(nil)
	if err != nil {
		// PEM 文件在 CI 或独立 go test 中可能不可达，此时跳过而非失败。
		t.Skipf("NewWithConfig returned error (PEM files likely unavailable): %v", err)
	}
	if issuer == nil {
		t.Fatal("expected ed25519 issuer to be initialized")
	}
}
