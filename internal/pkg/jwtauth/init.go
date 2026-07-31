package jwtauth

import (
	"strings"
	"time"

	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/runtime/resource"

	gojwt "github.com/gtkit/gojwt"
)

const (
	defaultJWTPrivateKeyPath = "config/pem/jwtpri.pem"
	defaultJWTPublicKeyPath  = "config/pem/jwtpub.pem"
	// defaultJWTTTL 与历史硬编码一致（30 天），保证缺省配置时行为不变。
	defaultJWTTTL = 30 * 24 * time.Hour
)

// NewWithConfig 创建一个新的 JWT issuer（不改包级全局），由调用方持有其返回值。
//
// key 路径与 token TTL 由 JWTConfig 驱动；缺省（cfg 为 nil 或字段留空/<=0）时
// 回退到历史默认值（config/pem/jwtpri.pem、jwtpub.pem、30 天），故不配置也行为不变。
func NewWithConfig(cfg *config.Config) (*gojwt.JwtEd25519, error) {
	privateKeyPath := defaultJWTPrivateKeyPath
	publicKeyPath := defaultJWTPublicKeyPath
	ttl := defaultJWTTTL

	if cfg != nil {
		if p := strings.TrimSpace(cfg.JWT.PrivateKeyPath); p != "" {
			privateKeyPath = p
		}
		if p := strings.TrimSpace(cfg.JWT.PublicKeyPath); p != "" {
			publicKeyPath = p
		}
		if cfg.JWT.TokenTTLHours > 0 {
			ttl = time.Duration(cfg.JWT.TokenTTLHours) * time.Hour
		}
	}

	return gojwt.NewJwtEd25519(
		resource.ResolvePath(privateKeyPath),
		resource.ResolvePath(publicKeyPath),
		gojwt.WithTokenDuration(ttl),
	)
}
