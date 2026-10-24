package config

import (
	"errors"
	"fmt"
	"strings"
)

// minRateLimitSecretLength 是限流键 HMAC 密钥的最小字节数。
//
// 与执行层的下限保持一致：短密钥让 HMAC 可暴力破解，键空间重新变得可枚举，
// 那等于回到普通哈希——而普通哈希对 IPv4 这种只有 2^32 种取值的输入
// 完全挡不住反查。
const minRateLimitSecretLength = 32

// validateRateLimitConfig 校验密钥与枚举；数值及 Redis 补满/TTL 边界由装配期 Policy.Validate 统一校验。
//
// 策略集合为空时不校验密钥：母版默认不启用限流，强制配一个用不到的密钥
// 只会让人随便填一个凑数，反而降低了它在真正启用时的可信度。
func validateRateLimitConfig(cfg RateLimitConfig) error {
	if len(cfg.Policies) == 0 {
		return nil
	}

	var errs []error

	if len(strings.TrimSpace(cfg.KeyHMACSecret)) < minRateLimitSecretLength {
		errs = append(errs, fmt.Errorf(
			"ratelimit.key_hmac_secret must be at least %d bytes when policies are configured: "+
				"subject identifiers are keyed-hashed into limiter keys, and a short secret makes "+
				"that key space enumerable again", minRateLimitSecretLength))
	}

	seen := make(map[string]struct{}, len(cfg.Policies))
	for i, p := range cfg.Policies {
		if _, dup := seen[p.ID]; dup {
			errs = append(errs, fmt.Errorf("ratelimit.policies[%d]: duplicate policy id %q", i, p.ID))
		}
		seen[p.ID] = struct{}{}

		if _, err := parseRateLimitScope(p.Scope); err != nil {
			errs = append(errs, fmt.Errorf("ratelimit.policies[%d] (%s): %w", i, p.ID, err))
		}
		if _, err := parseRateLimitStore(p.Store); err != nil {
			errs = append(errs, fmt.Errorf("ratelimit.policies[%d] (%s): %w", i, p.ID, err))
		}
		if _, err := parseRateLimitFailureMode(p.FailureMode); err != nil {
			errs = append(errs, fmt.Errorf("ratelimit.policies[%d] (%s): %w", i, p.ID, err))
		}
	}

	return errors.Join(errs...)
}

// 枚举的字符串取值。与限流执行层的字符串枚举保持一致，
// 配置里写的就是日志与错误里看到的那个词。
const (
	scopeIP     = "ip"
	scopeUser   = "user"
	scopeGlobal = "global"

	storeLocal = "local"
	storeRedis = "redis"

	failureOpen     = "fail_open"
	failureClosed   = "fail_closed"
	failureFallback = "local_fallback"
)

func parseRateLimitScope(raw string) (string, error) {
	switch strings.TrimSpace(raw) {
	case scopeIP, scopeUser, scopeGlobal:
		return strings.TrimSpace(raw), nil
	default:
		return "", fmt.Errorf("scope %q is invalid: must be one of %s|%s|%s",
			raw, scopeIP, scopeUser, scopeGlobal)
	}
}

func parseRateLimitStore(raw string) (string, error) {
	switch strings.TrimSpace(raw) {
	case storeLocal, storeRedis:
		return strings.TrimSpace(raw), nil
	default:
		return "", fmt.Errorf("store %q is invalid: must be one of %s|%s", raw, storeLocal, storeRedis)
	}
}

func parseRateLimitFailureMode(raw string) (string, error) {
	switch strings.TrimSpace(raw) {
	case failureOpen, failureClosed, failureFallback:
		return strings.TrimSpace(raw), nil
	default:
		return "", fmt.Errorf(
			"failure_mode %q is invalid: must be one of %s|%s|%s (there is no safe default — "+
				"whether an unavailable backend should reject or allow is a business decision)",
			raw, failureOpen, failureClosed, failureFallback)
	}
}
