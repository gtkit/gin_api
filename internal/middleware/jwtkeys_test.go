package middleware

import (
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/gtkit/gojwt"

	"github.com/gtkit/gin-api/internal/pkg/jwtauth"
	"github.com/gtkit/gin-api/internal/platform/config"
	sharedauth "github.com/gtkit/gin-api/internal/shared/auth"
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
		Auth: config.AuthConfig{Mode: config.AuthModeLocal},
		JWT: config.JWTConfig{
			Active: config.JWTKeyConfig{
				KID:            "test-kid",
				PrivateKeyPath: priPath,
				PublicKeyPath:  pubPath,
			},
			Issuer:    "gin-api-test",
			Audiences: []string{"app"},
		},
	}
}

// newTestJWT 装配一次性的轮换密钥体系。
func newTestJWT(t *testing.T) *jwtauth.Assembly {
	t.Helper()

	assembly, err := jwtauth.Assemble(testJWTConfig(t))
	if err != nil {
		t.Fatalf("Assemble returned error: %v", err)
	}
	return assembly
}

// newTestParser 返回一次性验签器。
func newTestParser(t *testing.T) jwtauth.TokenParser {
	t.Helper()
	return newTestJWT(t).Parser
}

// mustAuthenticate 构造认证中间件，装配失败直接让用例失败。
//
// 生产代码返回 error 而非 panic（装配错误应在启动期表达），
// 测试里用这个 helper 保持调用点简洁。
func mustAuthenticate(t *testing.T, v sharedauth.UserVerifier, p TokenParser, optional bool) gin.HandlerFunc {
	t.Helper()
	h := Auth(AuthOptions{Verifier: v, Parser: p, AllowMissingToken: optional})
	return h
}
