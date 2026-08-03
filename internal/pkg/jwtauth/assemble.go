package jwtauth

import (
	"errors"
	"fmt"
	"time"

	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/runtime/resource"
)

// defaultTokenTTL 与历史硬编码一致（30 天）。
//
// **刻意不改**：TTL 会直接影响存量客户端的重登频率，而本批次的目标是
// 让轮换不再造成批量失效，不是调整令牌寿命。
const defaultTokenTTL = 30 * 24 * time.Hour

// Assembly 是一次装配出的签发器与验签器。
type Assembly struct {
	Issuer *Issuer
	Parser *Parser
	// KeySet 供启动日志与诊断使用。
	KeySet *KeySet
}

// Assemble 按配置装配轮换密钥体系。
//
// 全部失败都是启动失败：密钥配置错误的表现要么是"签出的令牌谁也验不了"，
// 要么是"某类令牌被静默拒绝"——两者都只在真实流量上暴露。
func Assemble(cfg *config.Config) (*Assembly, error) {
	if cfg == nil {
		return nil, errors.New("jwt assembly requires a config")
	}

	keys, err := loadKeySet(cfg)
	if err != nil {
		return nil, err
	}

	cutoff, err := parseCutoff(cfg.JWT.LegacyNoKIDAcceptUntil)
	if err != nil {
		return nil, err
	}

	parser, err := NewParser(keys, ParserOptions{
		Issuer:                 cfg.JWT.Issuer,
		Audiences:              cfg.JWT.Audiences,
		ClockSkew:              time.Duration(cfg.JWT.ClockSkewSeconds) * time.Second,
		LegacyNoKIDAcceptUntil: cutoff,
	}, cfg.JWT.CacheMaxEntries)
	if err != nil {
		return nil, fmt.Errorf("build jwt parser: %w", err)
	}

	ttl := defaultTokenTTL
	if cfg.JWT.TokenTTLHours > 0 {
		ttl = time.Duration(cfg.JWT.TokenTTLHours) * time.Hour
	}

	issuer, err := NewIssuer(keys, parser, IssuerOptions{
		Issuer:    cfg.JWT.Issuer,
		Audiences: cfg.JWT.Audiences,
		TTL:       ttl,
	})
	if err != nil {
		return nil, fmt.Errorf("build jwt issuer: %w", err)
	}

	return &Assembly{Issuer: issuer, Parser: parser, KeySet: keys}, nil
}

// loadKeySet 读取并校验全部密钥文件。
func loadKeySet(cfg *config.Config) (*KeySet, error) {
	// 生产环境对私钥权限 fail-fast；其余环境交给 readKeyFile 的普通校验。
	//
	// 分环境是有意的：开发机上密钥常来自 git-ignored 的临时文件，
	// 权限不严格；而在生产上一把 group 可读的私钥等于把签发能力交给
	// 同机器上的任何一个进程。
	strictPerm := cfg.Env.IsProd()

	privatePath := resource.ResolvePath(cfg.JWT.Active.PrivateKeyPath)
	publicPath := resource.ResolvePath(cfg.JWT.Active.PublicKeyPath)

	private, err := LoadEd25519PrivateKey(privatePath, strictPerm)
	if err != nil {
		return nil, fmt.Errorf("load active private key: %w", err)
	}
	public, err := LoadEd25519PublicKey(publicPath)
	if err != nil {
		return nil, fmt.Errorf("load active public key: %w", err)
	}

	previous := make([]VerificationKey, 0, len(cfg.JWT.Previous))
	for i, prev := range cfg.JWT.Previous {
		pub, err := LoadEd25519PublicKey(resource.ResolvePath(prev.PublicKeyPath))
		if err != nil {
			return nil, fmt.Errorf("load previous public key %d (%s): %w", i, prev.KID, err)
		}
		previous = append(previous, VerificationKey{KID: prev.KID, Public: pub})
	}

	return NewKeySet(
		SigningKey{KID: cfg.JWT.Active.KID, Private: private, Public: public},
		previous,
	)
}

// parseCutoff 解析绝对截止时刻。
func parseCutoff(raw string) (time.Time, error) {
	if raw == "" {
		return time.Time{}, nil
	}
	parsed, err := time.Parse(time.RFC3339, raw)
	if err != nil {
		return time.Time{}, fmt.Errorf("parse jwt legacy cutoff %q: %w", raw, err)
	}
	return parsed, nil
}
