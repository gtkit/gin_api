package config

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	// maxJWTClockSkewSeconds 是允许的时钟偏移上限。
	//
	// 偏移窗口直接放宽 exp/nbf 判定，一个写成 86400 的配置等于把过期检查
	// 关掉整整一天，而它在配置里看起来只是"宽容一点"。
	maxJWTClockSkewSeconds = 120
	// maxJWTPreviousKeys 与 jwtauth 的硬上限保持一致。
	maxJWTPreviousKeys = 8
)

// validateJWTRotation 校验轮换相关配置。
//
// 全部在启动期完成：密钥配置错误的表现要么是"签出的令牌谁也验不了"，
// 要么是"某类令牌被静默拒绝"，两者都只在真实流量上暴露。
func validateJWTRotation(jwt JWTConfig, env Env) error {
	var errs []error

	// 废弃键 fail-fast，并给出迁移方向。
	//
	// 静默忽略比报错危险得多：使用者会以为自己配置的密钥正在生效，
	// 而实际用的是另一组（或根本没有）。
	if strings.TrimSpace(jwt.PrivateKeyPath) != "" || strings.TrimSpace(jwt.PublicKeyPath) != "" {
		errs = append(errs, errors.New(
			"jwt.private_key_path / jwt.public_key_path are no longer supported: keys are now "+
				"identified by a kid — move them under jwt.active (kid/private_key_path/public_key_path) "+
				"and put retired public keys under jwt.previous"))
	}

	if strings.TrimSpace(jwt.Active.KID) == "" {
		errs = append(errs, errors.New("jwt.active.kid is required"))
	}
	if strings.TrimSpace(jwt.Active.PrivateKeyPath) == "" {
		errs = append(errs, errors.New("jwt.active.private_key_path is required"))
	}
	if strings.TrimSpace(jwt.Active.PublicKeyPath) == "" {
		errs = append(errs, errors.New("jwt.active.public_key_path is required"))
	}

	if strings.TrimSpace(jwt.Issuer) == "" {
		errs = append(errs, errors.New(
			"jwt.issuer is required: a token that does not declare who issued it cannot be scoped"))
	}
	if len(jwt.Audiences) == 0 {
		errs = append(errs, errors.New(
			"jwt.audiences must contain at least one entry: without it a token issued for one "+
				"service can be replayed against another that shares the key"))
	}

	if len(jwt.Previous) > maxJWTPreviousKeys {
		errs = append(errs, fmt.Errorf(
			"jwt.previous holds %d keys, at most %d are allowed: tokens without a kid are verified "+
				"by trying keys in order, so an unbounded list makes that cost unbounded too",
			len(jwt.Previous), maxJWTPreviousKeys))
	}
	for i, prev := range jwt.Previous {
		if strings.TrimSpace(prev.KID) == "" {
			errs = append(errs, fmt.Errorf("jwt.previous[%d].kid is required", i))
		}
		if strings.TrimSpace(prev.PublicKeyPath) == "" {
			errs = append(errs, fmt.Errorf("jwt.previous[%d].public_key_path is required", i))
		}
		// previous 只做验签。留着私钥意味着一把已经退役的密钥仍能签发新令牌，
		// 而那正是轮换要终结的能力。
		if strings.TrimSpace(prev.PrivateKeyPath) != "" {
			errs = append(errs, fmt.Errorf(
				"jwt.previous[%d] must not carry a private key: retired keys verify only; "+
					"keeping the private key means a rotated-out key can still issue tokens", i))
		}
	}

	if jwt.ClockSkewSeconds < 0 {
		errs = append(errs, errors.New("jwt.clock_skew_seconds must not be negative"))
	}
	if jwt.ClockSkewSeconds > maxJWTClockSkewSeconds {
		errs = append(errs, fmt.Errorf(
			"jwt.clock_skew_seconds %d exceeds the %d ceiling: a wide skew window silently relaxes "+
				"expiry checking", jwt.ClockSkewSeconds, maxJWTClockSkewSeconds))
	}
	if jwt.CacheMaxEntries < 0 {
		errs = append(errs, fmt.Errorf(
			"jwt.cache_max_entries must not be negative, got %d (0 disables the cache)",
			jwt.CacheMaxEntries))
	}

	errs = append(errs, validateJWTCutoffs(jwt, env)...)

	return errors.Join(errs...)
}

// validateJWTCutoffs 校验两条兼容截止时刻。
//
// 两者**分别**校验、分别观测、分别删除：它们是不同的兼容维度
// （缺 kid 的存量令牌 vs 不同算法族的 Laravel 令牌），
// 共用一个开关会让其中一条的删除被另一条阻塞。
func validateJWTCutoffs(jwt JWTConfig, _ Env) []error {
	var errs []error

	for _, c := range []struct {
		key   string
		value string
	}{
		{"jwt.legacy_no_kid_accept_until", jwt.LegacyNoKIDAcceptUntil},
		{"jwt.laravel_legacy_accept_until", jwt.LaravelLegacyAcceptUntil},
	} {
		raw := strings.TrimSpace(c.value)
		if raw == "" {
			continue // 空 = 不接受，这是默认且安全的取值
		}
		parsed, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			errs = append(errs, fmt.Errorf(
				"%s must be an RFC3339 timestamp: an absolute instant is required so that a restart "+
					"cannot silently extend the compatibility window; got %q", c.key, raw))
			continue
		}
		// 上界：不允许配一个遥远的未来时刻。那等于把兼容口做成永久的，
		// 而"永久的过渡期"是这两个字段最常见的结局。
		if parsed.After(time.Now().Add(maxCutoffHorizon)) {
			errs = append(errs, fmt.Errorf(
				"%s is more than %s in the future: a compatibility window that long is indistinguishable "+
					"from a permanent one", c.key, maxCutoffHorizon))
		}
	}

	return errs
}

// maxCutoffHorizon 是兼容截止时刻允许的最远未来。
//
// 取值依据是默认 TTL（30 天）加一段迁移余量：截止时刻只需覆盖
// "切换时刻 + 旧令牌最大 TTL"，超出部分没有任何技术理由。
const maxCutoffHorizon = 45 * 24 * time.Hour
