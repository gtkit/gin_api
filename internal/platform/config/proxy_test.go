package config

import (
	"strings"
	"testing"

	"github.com/gtkit/gin-api/internal/shared/env"
)

func TestValidateProxyConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		proxy   ProxyConfig
		env     env.Env
		wantErr bool
		errWant string
	}{
		{
			// 默认值：不信任任何代理。这是本 change 的核心默认语义。
			name: "empty is valid and means trust nothing",
			env:  env.Prod,
		},
		{
			name:  "valid ipv4 cidr",
			proxy: ProxyConfig{TrustedProxies: []string{"10.0.0.0/8", "172.16.0.0/12"}, RemoteIPHeaders: []string{"X-Forwarded-For"}},
			env:   env.Prod,
		},
		{
			name:  "valid ipv6 cidr",
			proxy: ProxyConfig{TrustedProxies: []string{"fd00::/8"}, RemoteIPHeaders: []string{"X-Forwarded-For"}},
			env:   env.Prod,
		},
		{
			// Gin 的 SetTrustedProxies 同时接受单个 IP，校验口径必须与之一致，
			// 否则会出现"配置校验通过但 Gin 拒绝"的错位。
			name:  "single ip accepted",
			proxy: ProxyConfig{TrustedProxies: []string{"192.168.1.1", "::1"}, RemoteIPHeaders: []string{"X-Forwarded-For"}},
			env:   env.Prod,
		},
		{
			name:    "garbage entry rejected",
			proxy:   ProxyConfig{TrustedProxies: []string{"not-an-ip"}, RemoteIPHeaders: []string{"X-Forwarded-For"}},
			env:     env.Prod,
			wantErr: true, errWant: "not-an-ip",
		},
		{
			name:    "empty entry rejected",
			proxy:   ProxyConfig{TrustedProxies: []string{""}, RemoteIPHeaders: []string{"X-Forwarded-For"}},
			env:     env.Prod,
			wantErr: true, errWant: "is empty",
		},
		{
			name:    "bad mask rejected",
			proxy:   ProxyConfig{TrustedProxies: []string{"10.0.0.0/99"}, RemoteIPHeaders: []string{"X-Forwarded-For"}},
			env:     env.Prod,
			wantErr: true, errWant: "10.0.0.0/99",
		},
		{
			// 与"没配置"相比更隐蔽：看起来像已经配过了，实则回到不安全默认值。
			name:    "prod rejects ipv4 trust-all",
			proxy:   ProxyConfig{TrustedProxies: []string{"0.0.0.0/0"}, RemoteIPHeaders: []string{"X-Forwarded-For"}},
			env:     env.Prod,
			wantErr: true, errWant: "0.0.0.0/0",
		},
		{
			name:    "prod rejects ipv6 trust-all",
			proxy:   ProxyConfig{TrustedProxies: []string{"::/0"}, RemoteIPHeaders: []string{"X-Forwarded-For"}},
			env:     env.Prod,
			wantErr: true, errWant: "::/0",
		},
		{
			// 非生产允许：本地常需要 curl -H 'X-Forwarded-For: ...' 调试审计与限流。
			name:  "dev allows trust-all",
			proxy: ProxyConfig{TrustedProxies: []string{"0.0.0.0/0"}, RemoteIPHeaders: []string{"X-Forwarded-For"}},
			env:   env.Dev,
		},
		{
			name:  "test allows trust-all",
			proxy: ProxyConfig{TrustedProxies: []string{"0.0.0.0/0"}, RemoteIPHeaders: []string{"X-Forwarded-For"}},
			env:   env.Test,
		},
		{
			name:  "valid header names",
			proxy: ProxyConfig{RemoteIPHeaders: []string{"X-Forwarded-For", "X-Real-IP"}},
			env:   env.Prod,
		},
		{
			name:    "empty header rejected",
			proxy:   ProxyConfig{RemoteIPHeaders: []string{""}},
			env:     env.Prod,
			wantErr: true, errWant: "remote_ip_headers[0]",
		},
		{
			// 带空白的 header 名不可能匹配任何真实请求头，配了等于没配。
			name:    "header with whitespace rejected",
			proxy:   ProxyConfig{RemoteIPHeaders: []string{"X-Forwarded-For "}},
			env:     env.Prod,
			wantErr: true, errWant: "whitespace",
		},
		{
			// 声明了可信代理却不列转发头 = 矛盾配置。
			//
			// trusted_proxies 的唯一用途就是决定"来自哪些对端时才采信转发头"。
			// 列了代理不列头，那份信任没有任何作用，而使用者显然是想让代理后面的
			// 真实客户端 IP 生效。静默接受的后果：代理后所有客户端共用一个限流桶
			// （IP 维度限流退化成全局限流），审计里的来源 IP 全变成代理自己。
			name:    "trusted proxies without headers rejected",
			proxy:   ProxyConfig{TrustedProxies: []string{"10.0.0.0/8"}},
			env:     env.Prod,
			wantErr: true, errWant: "remote_ip_headers is empty",
		},
		{
			// 反向锚：两者都不配是合法的（没有代理在前面）。
			name:  "neither proxies nor headers is valid",
			proxy: ProxyConfig{},
			env:   env.Prod,
		},
		{
			name:    "header with invalid char rejected",
			proxy:   ProxyConfig{RemoteIPHeaders: []string{"X-Bad:Header"}},
			env:     env.Prod,
			wantErr: true, errWant: "invalid character",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateProxyConfig(tt.proxy, tt.env)
			if (err != nil) != tt.wantErr {
				t.Fatalf("validateProxyConfig() err = %v, wantErr = %v", err, tt.wantErr)
			}
			if tt.wantErr && !strings.Contains(err.Error(), tt.errWant) {
				t.Fatalf("error should mention %q, got %v", tt.errWant, err)
			}
		})
	}
}

func TestProxyWarnings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		proxy    ProxyConfig
		env      env.Env
		wantWarn bool
	}{
		{
			// 生产由校验直接拒绝，不走告警路径——否则会出现"既报错又告警"的重复输出。
			name:  "prod does not warn (validation rejects instead)",
			proxy: ProxyConfig{TrustedProxies: []string{"0.0.0.0/0"}, RemoteIPHeaders: []string{"X-Forwarded-For"}},
			env:   env.Prod,
		},
		{
			name:     "dev warns about trust-all",
			proxy:    ProxyConfig{TrustedProxies: []string{"0.0.0.0/0"}, RemoteIPHeaders: []string{"X-Forwarded-For"}},
			env:      env.Dev,
			wantWarn: true,
		},
		{
			name:     "test warns about ipv6 trust-all",
			proxy:    ProxyConfig{TrustedProxies: []string{"::/0"}, RemoteIPHeaders: []string{"X-Forwarded-For"}},
			env:      env.Test,
			wantWarn: true,
		},
		{
			name:  "specific cidr does not warn",
			proxy: ProxyConfig{TrustedProxies: []string{"10.0.0.0/8"}, RemoteIPHeaders: []string{"X-Forwarded-For"}},
			env:   env.Dev,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := tt.proxy.Warnings(tt.env)
			if (len(got) > 0) != tt.wantWarn {
				t.Fatalf("Warnings() = %v, wantWarn = %v", got, tt.wantWarn)
			}
		})
	}
}
