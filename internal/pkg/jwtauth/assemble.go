package jwtauth

import (
	"errors"
	"fmt"
	"time"

	"github.com/gtkit/gin-api/internal/platform/config"
	"github.com/gtkit/gin-api/internal/runtime/resource"
)

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

	cutoff, err := parseCutoff(cfg.JWT.UnkeyedTokenAcceptUntil)
	if err != nil {
		return nil, err
	}

	parser, err := NewParser(keys, ParserOptions{
		Issuer:                  cfg.JWT.Issuer,
		Audiences:               cfg.JWT.Audiences,
		ClockSkew:               time.Duration(cfg.JWT.ClockSkewSeconds) * time.Second,
		UnkeyedTokenAcceptUntil: cutoff,
	}, cfg.JWT.CacheMaxEntries)
	if err != nil {
		return nil, fmt.Errorf("build jwt parser: %w", err)
	}

	if cfg.Auth.Mode == config.AuthModeExternal {
		return &Assembly{Parser: parser, KeySet: keys}, nil
	}

	// 签发寿命与认证响应中的 expires_in 共用配置来源。
	ttl := cfg.JWT.EffectiveTokenTTL(cfg.Auth.Mode)

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

// loadKeySet 按认证模式读取密钥；external 不解析或读取私钥路径。
func loadKeySet(cfg *config.Config) (*KeySet, error) {
	switch cfg.Auth.Mode {
	case config.AuthModeLocal, config.AuthModeExternal:
	default:
		return nil, fmt.Errorf("jwt assembly: unsupported auth mode %q", cfg.Auth.Mode)
	}
	public, err := LoadEd25519PublicKey(resource.ResolvePath(cfg.JWT.Active.PublicKeyPath))
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

	if cfg.Auth.Mode == config.AuthModeExternal {
		return NewVerificationKeySet(VerificationKey{KID: cfg.JWT.Active.KID, Public: public}, previous)
	}
	private, err := LoadEd25519PrivateKey(resource.ResolvePath(cfg.JWT.Active.PrivateKeyPath), cfg.Env.IsProd())
	if err != nil {
		return nil, fmt.Errorf("load active private key: %w", err)
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
		return time.Time{}, fmt.Errorf("parse jwt acceptance cutoff %q: %w", raw, err)
	}
	return parsed, nil
}
