package middleware

import (
	"path/filepath"
	"testing"

	gojwt "github.com/gtkit/gojwt"

	"github.com/gtkit/gin-api/internal/pkg/jwtauth"
	"github.com/gtkit/gin-api/internal/platform/config"
)

// testJWTConfig 生成一对**临时** Ed25519 密钥并返回指向它们的配置。
//
// 为什么不复用仓库里的 config/pem/*.pem：那些是真实私钥，已被 .gitignore 正确排除、
// 零跟踪。依赖它们会让测试只在"开发者本机恰好生成过密钥"时通过——
// fresh clone 与 gonew 派生出来的项目都没有这些文件，`make check` 会直接失败，
// 连带让 `project init` 的收尾自检永远无法成功。
//
// 用 t.TempDir() 生成一次性密钥后，测试对仓库外部状态零依赖。
func testJWTConfig(t *testing.T) *config.Config {
	t.Helper()

	dir := t.TempDir()
	priPath := filepath.Join(dir, "jwtpri.pem")
	pubPath := filepath.Join(dir, "jwtpub.pem")
	if err := gojwt.GenerateEd25519Keys(priPath, pubPath); err != nil {
		t.Fatalf("generate ephemeral ed25519 keypair: %v", err)
	}

	return &config.Config{
		JWT: config.JWTConfig{
			PrivateKeyPath: priPath,
			PublicKeyPath:  pubPath,
		},
	}
}

// newTestIssuer 构造一个使用一次性密钥的 JWT issuer。
func newTestIssuer(t *testing.T) *gojwt.JwtEd25519 {
	t.Helper()

	issuer, err := jwtauth.NewWithConfig(testJWTConfig(t))
	if err != nil {
		t.Fatalf("NewWithConfig returned error: %v", err)
	}
	return issuer
}
