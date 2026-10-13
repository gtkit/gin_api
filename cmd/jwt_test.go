package cmd

import (
	"bytes"
	"crypto/ed25519"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gtkit/gojwt"
)

// TestJWTGenerateProducesUsableKeyPair 验证产物可用。
//
// 权限断言不在这里：Unix 权限位在 Windows 上没有对应语义（Go 恒报 0666），
// 放在一起会让整条用例连同"密钥是否配对"这种跨平台断言一起失去覆盖。
// 见 jwt_perm_unix_test.go。
func TestJWTGenerateProducesUsableKeyPair(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	result, err := generateKeyPair("k2026a", dir)
	if err != nil {
		t.Fatalf("generateKeyPair: %v", err)
	}

	// 产物必须真的配对——不配对的一组密钥会让签发成功、验签全败。
	for _, file := range []struct{ path, label string }{{result.privatePath, "PRIVATE KEY"}, {result.publicPath, "PUBLIC KEY"}} {
		block, _ := pem.Decode(readFile(t, file.path))
		if block == nil || block.Type != file.label {
			t.Fatalf("unexpected PEM label for %s", file.path)
		}
	}
	signer, err := gojwt.NewJwtEd25519(result.privatePath, result.publicPath)
	if err != nil {
		t.Fatal(err)
	}
	token, err := signer.GenerateToken(42)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := signer.ParseToken(token); err != nil {
		t.Fatalf("gojwt round trip: %v", err)
	}
	priv := readPrivateKey(t, result.privatePath)
	pub := readPublicKey(t, result.publicPath)
	derived, ok := priv.Public().(ed25519.PublicKey)
	if !ok || !derived.Equal(pub) {
		t.Fatal("the generated key pair does not match")
	}
}

// TestJWTGenerateRefusesToOverwrite 钉住"目标存在即拒绝"。
//
// kid 必须稳定标识一组**不可变**的密钥材料：同 kid 原地替换会破坏缓存与
// 审计语义，也让"这个 kid 对应哪把密钥"变成一个时间相关的问题。
// 轮换的正确做法是生成新 kid。
func TestJWTGenerateRefusesToOverwrite(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	if _, err := generateKeyPair("k2026a", dir); err != nil {
		t.Fatalf("first generateKeyPair: %v", err)
	}

	before := readFile(t, filepath.Join(dir, "k2026a.private.pem"))

	_, err := generateKeyPair("k2026a", dir)
	if err == nil {
		t.Fatal("generateKeyPair overwrote an existing kid; rotation must create a new kid instead")
	}
	if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error %q does not explain the refusal", err)
	}

	// 原文件必须**原封不动**：一次被拒绝的生成不能造成任何副作用。
	after := readFile(t, filepath.Join(dir, "k2026a.private.pem"))
	if !bytes.Equal(before, after) {
		t.Fatal("the existing private key was modified by a refused generation")
	}
}

// TestJWTGenerateLeavesNothingBehindOnPartialFailure 验证失败不留半成品。
//
// 构造方式：先占住公钥文件名，于是私钥能建、公钥建不了。
// 此时本次新建的私钥必须被删除——留下一个孤立的私钥意味着
// 下一次同 kid 生成会因"已存在"被拒，而那个文件谁也不认识。
func TestJWTGenerateLeavesNothingBehindOnPartialFailure(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	publicPath := filepath.Join(dir, "kpartial.public.pem")
	if err := os.WriteFile(publicPath, []byte("occupied"), 0o644); err != nil {
		t.Fatalf("occupy public key path: %v", err)
	}

	if _, err := generateKeyPair("kpartial", dir); err == nil {
		t.Fatal("generateKeyPair succeeded although the public key path was occupied")
	}

	if _, err := os.Stat(filepath.Join(dir, "kpartial.private.pem")); err == nil {
		t.Fatal("a private key was left behind after a failed generation")
	}
}

// TestJWTGenerateRejectsBadOutDir 覆盖输出目录的三类非法形态。
func TestJWTGenerateRejectsBadOutDir(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()

	t.Run("empty", func(t *testing.T) {
		t.Parallel()
		if _, err := generateKeyPair("k", ""); err == nil {
			t.Fatal("generateKeyPair accepted an empty out-dir")
		}
	})

	t.Run("not a directory", func(t *testing.T) {
		t.Parallel()
		file := filepath.Join(dir, "afile")
		if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
			t.Fatalf("write file: %v", err)
		}
		if _, err := generateKeyPair("k", file); err == nil {
			t.Fatal("generateKeyPair accepted a regular file as out-dir")
		}
	})

	t.Run("symlink", func(t *testing.T) {
		t.Parallel()
		target := t.TempDir()
		link := filepath.Join(t.TempDir(), "link")
		if err := os.Symlink(target, link); err != nil {
			t.Skipf("symlink not supported: %v", err)
		}
		_, err := generateKeyPair("k", link)
		if err == nil {
			t.Fatal("generateKeyPair accepted a symlinked out-dir; " +
				"key material must land where the caller named, not where a link points")
		}
		if !strings.Contains(err.Error(), "symlink") {
			t.Fatalf("error %q does not explain the symlink refusal", err)
		}
	})
}

// TestJWTCommandRejectsBadKID 验证非法 kid 在动手之前被拦。
func TestJWTCommandRejectsBadKID(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cmd := newJWTGenerateCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--kid", "bad/kid", "--out-dir", dir})

	if err := cmd.Execute(); err == nil {
		t.Fatal("jwt generate accepted a kid containing a path separator")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("a rejected kid still produced %d file(s)", len(entries))
	}
}

// TestJWTCommandNeverPrintsPrivateKeyMaterial 钉住输出脱敏。
//
// 这条命令的输出经常被直接粘进工单、聊天窗口或 CI 日志。
func TestJWTCommandNeverPrintsPrivateKeyMaterial(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cmd := newJWTGenerateCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--kid", "k2026b", "--out-dir", dir})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("jwt generate: %v", err)
	}

	printed := out.String()
	if strings.Contains(printed, "PRIVATE KEY") {
		t.Fatalf("stdout contains private key material:\n%s", printed)
	}
	// 但必须给出 kid 与路径，否则使用者拿不到接下来要填的配置。
	for _, want := range []string{"k2026b", "kid:", "config snippet"} {
		if !strings.Contains(printed, want) {
			t.Fatalf("stdout does not contain %q:\n%s", want, printed)
		}
	}
}

// TestBareJWTCommandFails 验证不带子命令的旧用法失败并给出迁移提示。
//
// 旧行为是直接覆盖默认密钥文件——在轮换语义下那是破坏性的。
func TestBareJWTCommandFails(t *testing.T) {
	t.Parallel()

	cmd := newJWTCmd(&rootOptions{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(nil)

	err := cmd.Execute()
	if err == nil {
		t.Fatal("the bare `jwt` usage succeeded; it must fail with a migration hint")
	}
	if !strings.Contains(err.Error(), "jwt generate") {
		t.Fatalf("error %q does not point at the new subcommand", err)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return data
}

func readPrivateKey(t *testing.T, path string) ed25519.PrivateKey {
	t.Helper()
	block, _ := pem.Decode(readFile(t, path))
	if block == nil {
		t.Fatalf("%s is not valid PEM", path)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatalf("parse private key: %v", err)
	}
	key, ok := parsed.(ed25519.PrivateKey)
	if !ok {
		t.Fatalf("private key is %T, want ed25519", parsed)
	}
	return key
}

func readPublicKey(t *testing.T, path string) ed25519.PublicKey {
	t.Helper()
	block, _ := pem.Decode(readFile(t, path))
	if block == nil {
		t.Fatalf("%s is not valid PEM", path)
	}
	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		t.Fatalf("parse public key: %v", err)
	}
	key, ok := parsed.(ed25519.PublicKey)
	if !ok {
		t.Fatalf("public key is %T, want ed25519", parsed)
	}
	return key
}
