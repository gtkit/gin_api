package config

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
)

// minAppSecretLength 是签名 appSecret 的最小字节数。
//
// 与限流、刷新令牌重放材料的密钥下界同值：短密钥让 HMAC 可被暴力枚举，
// appSecret 是整个签名体系的根信任，一旦可猜就等同于没有签名。
const minAppSecretLength = 32

// 时间参数的内置默认值。均放行显式覆盖，仅约束取值不为负。
const (
	defaultSignatureMaxSkewSeconds     = 60  // 时钟偏移容忍
	defaultSignatureValidWindowSeconds = 300 // 时间戳有效期（5 分钟）
)

// validateSignatureConfig 校验服务端间签名的静态配置。
//
// 客户端集合为空时不校验密钥：母版默认不启用签名，强制配一个用不到的
// appSecret 只会让人随便填一个凑数，反而降低它在真正启用时的可信度。
//
// appSecret 由消费方自己注入，绝不写进本项目的 env.yml.example 明文。
func validateSignatureConfig(cfg SignatureConfig) error {
	if len(cfg.Clients) == 0 {
		return nil
	}

	var errs []error
	for appID, secret := range cfg.Clients {
		if strings.TrimSpace(appID) == "" {
			errs = append(errs, errors.New("signature.clients contains an empty app id"))
		}
		if trimmed := strings.TrimSpace(secret); len(trimmed) < minAppSecretLength {
			errs = append(errs, fmt.Errorf(
				"signature.clients[%q] secret must be at least %d bytes: it is the HMAC key, "+
					"and a short secret makes it brute-forceable", appID, minAppSecretLength))
		}
	}
	if cfg.MaxSkewSeconds < 0 {
		errs = append(errs, errors.New("signature.max_skew_seconds must not be negative"))
	}
	if cfg.ValidWindowSeconds < 0 {
		errs = append(errs, errors.New("signature.valid_window_seconds must not be negative"))
	}
	window, skew := int64(cfg.ValidWindowSeconds), int64(cfg.MaxSkewSeconds)
	if window == 0 {
		window = defaultSignatureValidWindowSeconds
	}
	if skew == 0 {
		skew = defaultSignatureMaxSkewSeconds
	}
	const maxSeconds = math.MaxInt64/int64(time.Second) - 1
	if window > maxSeconds || (window >= 0 && skew > (maxSeconds-window)/2) {
		errs = append(errs, errors.New("signature timestamp and skew windows overflow the nonce lifetime"))
	}
	return errors.Join(errs...)
}

// EffectiveMaxSkew 返回允许的时钟偏移，零值（未配置）回落默认 60 秒。
func (c SignatureConfig) EffectiveMaxSkew() time.Duration {
	if c.MaxSkewSeconds == 0 {
		return defaultSignatureMaxSkewSeconds * time.Second
	}
	return time.Duration(c.MaxSkewSeconds) * time.Second
}

// EffectiveValidWindow 返回时间戳有效期，零值（未配置）回落默认 5 分钟。
func (c SignatureConfig) EffectiveValidWindow() time.Duration {
	if c.ValidWindowSeconds == 0 {
		return defaultSignatureValidWindowSeconds * time.Second
	}
	return time.Duration(c.ValidWindowSeconds) * time.Second
}
