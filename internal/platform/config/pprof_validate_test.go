package config

import (
	"strings"
	"testing"

	"github.com/gtkit/gin-api/internal/shared/env"
)

// TestValidatePprofConfig 覆盖 pprof 绑定地址与采样率的全部约束。
//
// 三条设计决定各有对应用例：
//   - **不分环境**：dev 上的非回环地址同样拒绝。pprof 没有鉴权，
//     谁能连上谁就能拿到堆栈、堆快照与 CPU profile；"这是测试环境"不构成
//     放宽的理由，而按环境区分的规则终究会被复制到生产。
//   - **只接受字面 IP**：`localhost` 依赖 DNS 与 /etc/hosts，被改写时会让端点
//     绑到非回环地址，而配置看起来完全正常。
//   - **用 IsLoopback 判定**：127.0.0.2 与展开写法的 IPv6 回环同样合法，
//     字符串白名单会误拒；而白名单一旦被"顺手扩充"，扩进非回环地址不会有提示。
func TestValidatePprofConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		env      env.Env
		pprof    PprofConfig
		wantErr  bool
		wantHint string
	}{
		{name: "disabled ignores everything", env: env.Prod, pprof: PprofConfig{Enabled: false, Host: "0.0.0.0"}},
		{name: "empty host falls back to loopback", env: env.Prod, pprof: PprofConfig{Enabled: true}},
		{name: "ipv4 loopback ok", env: env.Prod, pprof: PprofConfig{Enabled: true, Host: "127.0.0.1"}},
		{
			// 127.0.0.0/8 整段都是回环，字符串白名单会误拒它。
			name: "alternate ipv4 loopback ok", env: env.Prod,
			pprof: PprofConfig{Enabled: true, Host: "127.0.0.2"},
		},
		{name: "ipv6 loopback ok", env: env.Prod, pprof: PprofConfig{Enabled: true, Host: "::1"}},
		{
			name: "expanded ipv6 loopback ok", env: env.Prod,
			pprof: PprofConfig{Enabled: true, Host: "0:0:0:0:0:0:0:1"},
		},
		{
			name: "wildcard rejected", env: env.Prod,
			pprof: PprofConfig{Enabled: true, Host: "0.0.0.0"}, wantErr: true, wantHint: "not a loopback",
		},
		{
			name: "private ip rejected", env: env.Prod,
			pprof: PprofConfig{Enabled: true, Host: "10.0.0.5"}, wantErr: true, wantHint: "not a loopback",
		},
		{
			// 关键回归：dev 上同样拒绝。旧实现只在 prod 校验。
			name: "wildcard rejected in dev too", env: env.Env("dev"),
			pprof: PprofConfig{Enabled: true, Host: "0.0.0.0"}, wantErr: true, wantHint: "not a loopback",
		},
		{
			// 关键回归：主机名一律拒绝，包括 localhost。
			name: "hostname rejected", env: env.Env("dev"),
			pprof: PprofConfig{Enabled: true, Host: "localhost"}, wantErr: true, wantHint: "not a hostname",
		},
		{
			name: "negative block rate rejected", env: env.Env("dev"),
			pprof:   PprofConfig{Enabled: true, Host: "127.0.0.1", BlockProfileRate: -1},
			wantErr: true, wantHint: "block_profile_rate",
		},
		{
			name: "negative mutex fraction rejected", env: env.Env("dev"),
			pprof:   PprofConfig{Enabled: true, Host: "127.0.0.1", MutexProfileFraction: -1},
			wantErr: true, wantHint: "mutex_profile_fraction",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := validatePprofConfig(tc.env, tc.pprof)
			if (err != nil) != tc.wantErr {
				t.Fatalf("validatePprofConfig(%s, %+v) err=%v, wantErr=%v", tc.env, tc.pprof, err, tc.wantErr)
			}
			if tc.wantHint != "" && !strings.Contains(err.Error(), tc.wantHint) {
				t.Fatalf("error %q does not mention %q", err, tc.wantHint)
			}
		})
	}
}
