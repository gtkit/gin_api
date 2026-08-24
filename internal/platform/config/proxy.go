package config

import (
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

// trustAllCIDRs 是等价于「信任所有代理」的两个写法。
//
// 它们比"没配置"更隐蔽——看起来像是已经配过了，实则回到了不安全默认值，
// 因此生产环境必须拒绝。
var trustAllCIDRs = []string{"0.0.0.0/0", "::/0"}

// ProxyWarnings 返回不阻断启动、但必须让运维看见的代理配置问题。
//
// 与校验错误分开：非生产环境允许 0.0.0.0/0（本地常需要 curl -H 'X-Forwarded-For: ...'
// 调试审计与限流，一刀切会逼开发者改代码绕过），但不能让它无声地被带到预发环境。
func (c ProxyConfig) Warnings(env Env) []string {
	if env == EnvProd {
		return nil // 生产由校验直接拒绝，不走告警路径。
	}
	var warnings []string
	for _, entry := range c.TrustedProxies {
		if isTrustAllCIDR(entry) {
			warnings = append(warnings, fmt.Sprintf(
				"proxy.trusted_proxies contains %q, which trusts every peer and lets any client forge its IP; "+
					"allowed outside production for debugging only", strings.TrimSpace(entry)))
		}
	}
	return warnings
}

func isTrustAllCIDR(entry string) bool {
	return slices.Contains(trustAllCIDRs, strings.TrimSpace(entry))
}

// validateProxyConfig 校验可信代理与转发头配置。
func validateProxyConfig(proxy ProxyConfig, env Env) error {
	var errs []error

	for i, entry := range proxy.TrustedProxies {
		trimmed := strings.TrimSpace(entry)
		if trimmed == "" {
			errs = append(errs, fmt.Errorf("proxy.trusted_proxies[%d] is empty", i))
			continue
		}
		// Gin 的 SetTrustedProxies 同时接受 CIDR 与单个 IP，这里按同一口径校验，
		// 避免"配置校验通过但 Gin 拒绝"的错位。
		if _, err := netip.ParsePrefix(trimmed); err != nil {
			if _, ipErr := netip.ParseAddr(trimmed); ipErr != nil {
				errs = append(errs, fmt.Errorf(
					"proxy.trusted_proxies[%d] %q is neither a valid CIDR nor a valid IP address", i, trimmed))
				continue
			}
		}
		if env == EnvProd && isTrustAllCIDR(trimmed) {
			errs = append(errs, fmt.Errorf(
				"proxy.trusted_proxies[%d] %q trusts every peer and is rejected in production: "+
					"it is equivalent to the unsafe default and lets any client forge X-Forwarded-For", i, trimmed))
		}
	}

	for i, header := range proxy.RemoteIPHeaders {
		if err := validateHeaderToken(header); err != nil {
			errs = append(errs, fmt.Errorf("proxy.remote_ip_headers[%d]: %w", i, err))
		}
	}

	// 声明了可信代理却不列转发头 = 矛盾配置，拒绝启动。
	//
	// `trusted_proxies` 的**唯一**用途就是决定"来自哪些对端时才采信转发头"。
	// 列了代理却不列头，那份信任没有任何作用，而使用者显然是想让代理后面的
	// 真实客户端 IP 生效。
	//
	// **为什么必须 fail-closed 而不是留空回落框架默认值**：客户端 IP 是限流主体
	// 与审计事实的来源。静默回落到框架默认（X-Forwarded-For / X-Real-IP）会让
	// 配置说的和实际做的不一致；而静默取连接对端则让代理后面所有客户端共用
	// 同一个限流桶（IP 维度限流退化成全局限流，第一个繁忙时段把所有人打成 429），
	// 审计里的来源 IP 也全部变成代理自己。两种静默都是错的，只有报错能让人改对。
	if len(proxy.TrustedProxies) > 0 && len(proxy.RemoteIPHeaders) == 0 {
		errs = append(errs, errors.New(
			"proxy.trusted_proxies is set but proxy.remote_ip_headers is empty: trusting proxies only "+
				"has an effect through those headers, so this combination silently makes every client "+
				"behind the proxy share one rate-limit bucket and records the proxy's own IP in audit "+
				"logs. List the header your proxy actually sets (commonly [\"X-Forwarded-For\"]), or "+
				"clear proxy.trusted_proxies if no proxy fronts this service"))
	}

	return errors.Join(errs...)
}

// validateHeaderToken 校验一个 HTTP header 名是否为合法 token（RFC 9110 §5.6.2）。
//
// 不接受空白：带空白的 header 名不可能匹配任何真实请求头，配了等于没配，
// 属于必须在启动期暴露的配置错误而不是静默无效。
func validateHeaderToken(header string) error {
	if header == "" {
		return errors.New("header name is empty")
	}
	if strings.TrimSpace(header) != header {
		return fmt.Errorf("header name %q has leading or trailing whitespace", header)
	}
	for _, r := range header {
		if !isTokenChar(r) {
			return fmt.Errorf("header name %q contains an invalid character %q", header, r)
		}
	}
	return nil
}

// isTokenChar 判定 RFC 9110 的 tchar。
func isTokenChar(r rune) bool {
	switch {
	case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		return true
	}
	return strings.ContainsRune("!#$%&'*+-.^_`|~", r)
}
