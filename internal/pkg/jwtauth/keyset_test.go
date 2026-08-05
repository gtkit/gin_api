package jwtauth

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestValidateKID 钉住 kid 的字符集限制。
//
// kid 同时是 JWT 头里的值与密钥**文件名的一部分**。放行路径分隔符意味着
// 一个攻击者控制的 kid 能让密钥加载去读任意路径——而它看起来只是个标识符。
func TestValidateKID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		kid     string
		wantErr bool
	}{
		{name: "alphanumeric", kid: "key2026"},
		{name: "with dash", kid: "prod-key-1"},
		{name: "with underscore", kid: "prod_key_1"},
		{name: "with dot", kid: "prod.key.1"},

		{name: "empty rejected", kid: "", wantErr: true},
		{name: "slash rejected", kid: "../../etc/passwd", wantErr: true},
		{name: "backslash rejected", kid: `a\b`, wantErr: true},
		{name: "space rejected", kid: "a b", wantErr: true},
		{name: "over 64 chars rejected", kid: strings.Repeat("a", 65), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateKID(tt.kid)
			if gotErr := err != nil; gotErr != tt.wantErr {
				t.Fatalf("ValidateKID(%q) = %v, wantErr = %v", tt.kid, err, tt.wantErr)
			}
		})
	}
}

// newKeyPair 生成一对测试密钥。
func newTestKeyPair(t *testing.T) (ed25519.PublicKey, ed25519.PrivateKey) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey: %v", err)
	}
	return pub, priv
}

// TestKeySetLookup 覆盖 KeySet 的三个访问器。
func TestKeySetLookup(t *testing.T) {
	t.Parallel()

	activePub, activePriv := newTestKeyPair(t)
	prevPub, _ := newTestKeyPair(t)

	set, err := NewKeySet(
		SigningKey{KID: "active", Private: activePriv, Public: activePub},
		[]VerificationKey{{KID: "previous", Public: prevPub}},
	)
	if err != nil {
		t.Fatalf("NewKeySet: %v", err)
	}

	if set.ActiveKID() != "active" {
		t.Errorf("ActiveKID() = %q, want active", set.ActiveKID())
	}
	if !set.SigningKey().Equal(activePriv) {
		t.Error("SigningKey() returned a different key")
	}

	// 活跃与历史公钥都必须能按 kid 取到：轮换期间两者签发的令牌都在流通。
	for _, kid := range []string{"active", "previous"} {
		if _, ok := set.PublicKeyFor(kid); !ok {
			t.Errorf("PublicKeyFor(%q) not found", kid)
		}
	}
	if _, ok := set.PublicKeyFor("unknown"); ok {
		t.Error("PublicKeyFor returned a key for an unknown kid: an unknown kid must be " +
			"rejected outright, never fall back to trying every key")
	}

	// OrderedKeys 的顺序决定无 kid 存量令牌的尝试顺序，活跃密钥必须在最前。
	ordered := set.OrderedKeys()
	if len(ordered) != 2 || ordered[0].KID != "active" {
		t.Fatalf("OrderedKeys() = %+v, want the active key first", ordered)
	}
}

// TestNewKeySetRejectsInvalidInput 覆盖装配期校验。
func TestNewKeySetRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	pub, priv := newTestKeyPair(t)
	otherPub, _ := newTestKeyPair(t)

	tests := []struct {
		name     string
		active   SigningKey
		previous []VerificationKey
	}{
		{name: "empty kid", active: SigningKey{KID: "", Private: priv, Public: pub}},
		{name: "invalid kid", active: SigningKey{KID: "a/b", Private: priv, Public: pub}},
		{name: "no private key", active: SigningKey{KID: "k", Public: pub}},
		{
			// 公钥与私钥对不上：签出来的令牌自己都验不过，而这只有在第一次
			// 验签时才会暴露——那时已经有令牌发出去了。
			name:   "mismatched pair",
			active: SigningKey{KID: "k", Private: priv, Public: otherPub},
		},
		{
			name:     "duplicate kid",
			active:   SigningKey{KID: "k", Private: priv, Public: pub},
			previous: []VerificationKey{{KID: "k", Public: otherPub}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if _, err := NewKeySet(tt.active, tt.previous); err == nil {
				t.Fatal("NewKeySet accepted an invalid key set")
			}
		})
	}
}

// writePEM 把密钥写成 PEM 文件。
func writePEM(t *testing.T, dir, name, blockType string, der []byte, mode os.FileMode) string {
	t.Helper()
	path := filepath.Join(dir, name)
	data := pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der})
	if err := os.WriteFile(path, data, mode); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

// TestLoadEd25519Keys 覆盖密钥文件加载的成功与失败路径。
func TestLoadEd25519Keys(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	pub, priv := newTestKeyPair(t)

	privDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("MarshalPKCS8PrivateKey: %v", err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatalf("MarshalPKIXPublicKey: %v", err)
	}

	privPath := writePEM(t, dir, "k.private.pem", "PRIVATE KEY", privDER, 0o600)
	pubPath := writePEM(t, dir, "k.public.pem", "PUBLIC KEY", pubDER, 0o644)

	t.Run("loads a valid pair", func(t *testing.T) {
		t.Parallel()

		got, err := LoadEd25519PrivateKey(privPath, false)
		if err != nil {
			t.Fatalf("LoadEd25519PrivateKey: %v", err)
		}
		if !got.Equal(priv) {
			t.Error("the loaded private key differs from the written one")
		}

		gotPub, err := LoadEd25519PublicKey(pubPath)
		if err != nil {
			t.Fatalf("LoadEd25519PublicKey: %v", err)
		}
		if !gotPub.Equal(pub) {
			t.Error("the loaded public key differs from the written one")
		}
	})

	t.Run("rejects a missing file", func(t *testing.T) {
		t.Parallel()

		if _, err := LoadEd25519PrivateKey(filepath.Join(dir, "nope.pem"), false); err == nil {
			t.Fatal("a missing key file was accepted")
		}
	})

	t.Run("rejects non-PEM content", func(t *testing.T) {
		t.Parallel()

		bad := filepath.Join(dir, "bad.pem")
		if err := os.WriteFile(bad, []byte("not pem at all"), 0o600); err != nil {
			t.Fatalf("write: %v", err)
		}
		if _, err := LoadEd25519PrivateKey(bad, false); err == nil {
			t.Fatal("a non-PEM file was accepted as a private key")
		}
		if _, err := LoadEd25519PublicKey(bad); err == nil {
			t.Fatal("a non-PEM file was accepted as a public key")
		}
	})

	t.Run("rejects a public key where a private one is expected", func(t *testing.T) {
		t.Parallel()

		if _, err := LoadEd25519PrivateKey(pubPath, false); err == nil {
			t.Fatal("a public key file was accepted as a private key")
		}
	})

	t.Run("strict permission rejects a group-readable private key", func(t *testing.T) {
		t.Parallel()

		loose := writePEM(t, t.TempDir(), "loose.pem", "PRIVATE KEY", privDER, 0o644)
		if _, err := LoadEd25519PrivateKey(loose, true); err == nil {
			t.Fatal("a group-readable private key passed the strict permission check: " +
				"in production that is a key anyone on the host can copy")
		}
		// 非严格模式下同一个文件必须能读：开发环境不该被权限位挡住。
		if _, err := LoadEd25519PrivateKey(loose, false); err != nil {
			t.Fatalf("non-strict mode rejected a readable key: %v", err)
		}
	})
}
