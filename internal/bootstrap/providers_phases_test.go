package bootstrap

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"github.com/gtkit/gin-api/internal/pkg/jwtauth"
	"github.com/gtkit/gin-api/internal/runtime/module/account"
	"gorm.io/gorm"

	"github.com/gtkit/gin-api/internal/platform/config"
)

// TestResolveInfraRequiresDao 覆盖 spec 场景「基础设施未就绪时中止装配」：
// Runtime 的 laravel DAO 为 nil 时，resolveInfra 必须返回错误且不继续后续阶段。
func TestResolveInfraRequiresDao(t *testing.T) {
	t.Parallel()

	rt := &Runtime{config: &config.Config{}}
	if _, err := resolveInfra(rt); err == nil {
		t.Fatal("resolveInfra expected error when laravel dao is nil, got nil")
	}
}

// TestBuildMiddlewares 覆盖 spec 场景「token parser 在中间件阶段装配」：
// laravel 关闭时正常装配 auth；laravel 开启但密钥为空时错误在本阶段被透传。
func TestBuildMiddlewares(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		laravel config.LaravelJWTConfig
		wantErr bool
	}{
		{
			name:    "laravel disabled wires auth middleware",
			laravel: config.LaravelJWTConfig{},
		},
		{
			name:    "laravel enabled with empty secret propagates error",
			laravel: config.LaravelJWTConfig{Enabled: true},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// 必须给一个真实的 JWT 装配：parser 缺失时 Authenticate 会
			// 直接拒绝装配（fail-closed），那正是期望的行为，
			// 但它会掩盖本用例真正要验的 laravel 分支。
			cfg := jwtTestConfig(t)
			cfg.JWT.Laravel = tt.laravel
			assembly, err := jwtauth.Assemble(cfg)
			if err != nil {
				t.Fatalf("Assemble: %v", err)
			}
			rt := &Runtime{config: cfg, jwt: assembly}

			// Account sources are an explicit dependency after the assembly
			// split. Keep this fixture complete so a missing verifier cannot
			// hide the Laravel parser branch that this test is exercising.
			accounts, err := account.New(&gorm.DB{})
			if err != nil {
				t.Fatal(err)
			}
			mw, err := buildMiddlewares(services{accounts: accounts}, rt)
			if tt.wantErr {
				if err == nil {
					t.Fatal("buildMiddlewares expected error, got nil")
				}
				return
			}
			if err != nil {
				t.Fatalf("buildMiddlewares returned error: %v", err)
			}
			if mw.auth == nil {
				t.Fatal("buildMiddlewares returned nil auth middleware")
			}
		})
	}
}

// jwtTestConfig 生成一次性 Ed25519 密钥并返回指向它们的完整配置。
//
// 不复用仓库里的 config/pem/*.pem：那些是真实私钥且已被正确排除、零跟踪。
// 依赖它们会让测试只在"开发者本机恰好生成过密钥"时通过——fresh clone 与
// gonew 派生出来的项目都没有这些文件。
func jwtTestConfig(t *testing.T) *config.Config {
	t.Helper()

	dir := t.TempDir()
	priPath := filepath.Join(dir, "test.private.pem")
	pubPath := filepath.Join(dir, "test.public.pem")

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("generate ed25519 key: %v", err)
	}
	privDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatalf("marshal private key: %v", err)
	}
	pubDER, err := x509.MarshalPKIXPublicKey(pub)
	if err != nil {
		t.Fatalf("marshal public key: %v", err)
	}
	if err := os.WriteFile(priPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privDER}), 0o600); err != nil {
		t.Fatalf("write private key: %v", err)
	}
	if err := os.WriteFile(pubPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: pubDER}), 0o600); err != nil {
		t.Fatalf("write public key: %v", err)
	}

	return &config.Config{
		Auth: config.AuthConfig{Mode: config.AuthModeExternal},
		JWT: config.JWTConfig{
			Active: config.JWTKeyConfig{
				KID: "test-kid", PrivateKeyPath: priPath, PublicKeyPath: pubPath,
			},
			Issuer:    "gin-api-test",
			Audiences: []string{"app"},
		},
	}
}
