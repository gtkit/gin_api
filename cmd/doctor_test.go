package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gtkit/gin-api/internal/diagnostics"
	"github.com/gtkit/gin-api/internal/platform/config"
)

// validConfigForDoctor 返回一份能通过严格校验的最小配置。
func validConfigForDoctor() *config.Config {
	return &config.Config{
		Env: config.EnvDev,
		App: config.AppConfig{Name: "app_client", Host: "127.0.0.1", Port: "8008"},
		// 认证模式没有默认值。取 external：它不要求 Redis，
		// 不给这些与认证无关的用例强加一个依赖。
		Auth: config.AuthConfig{Mode: config.AuthModeExternal},
		Database: config.DatabaseConfig{
			DBType: "mysql", Host: "127.0.0.1", Port: "3306", Name: "sleep", Username: "root",
		},
		Redis: config.RedisConfig{Addr: "127.0.0.1:6379", DBs: []int{0, 1, 2}},
		Log:   config.LogConfig{Level: "info", Path: "./logs/app_client"},
		JWT: config.JWTConfig{
			Active: config.JWTKeyConfig{
				KID:            "k2026a",
				PrivateKeyPath: "config/pem/k2026a.private.pem",
				PublicKeyPath:  "config/pem/k2026a.public.pem",
			},
			Issuer:    "gin-api",
			Audiences: []string{"app"},
		},
	}
}

// configWithRealKeysForDoctor 返回一份密钥文件真实存在的配置。
//
// jwt_keys 会 stat 活跃密钥。fixture 指向不存在的路径时，任何断言"doctor 整体
// 通过"的用例都会因为一项与被测行为无关的真实失败而失败——而密钥文件不进 git，
// 干净 clone 上必然没有它们。
func configWithRealKeysForDoctor(t *testing.T) *config.Config {
	t.Helper()

	dir := t.TempDir()
	cfg := validConfigForDoctor()
	cfg.JWT.Active.PrivateKeyPath = filepath.Join(dir, "active.private.pem")
	cfg.JWT.Active.PublicKeyPath = filepath.Join(dir, "active.public.pem")
	for _, p := range []string{cfg.JWT.Active.PrivateKeyPath, cfg.JWT.Active.PublicKeyPath} {
		if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
			t.Fatalf("write %s: %v", p, err)
		}
	}
	return cfg
}

// noProbes 明确表示本次不接线 deep 探测。
func noProbes(context.Context) (diagnostics.Probes, func() error, error) {
	return diagnostics.Probes{}, func() error { return nil }, nil
}

func TestDoctorCommand(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		load       configLoader
		wantErr    bool
		wantOutput []string
	}{
		{
			name: "valid config",
			load: func() (*config.Config, error) { return configWithRealKeysForDoctor(t), nil },
			wantOutput: []string{
				"config: ok",
				"env: dev",
				"application: app_client",
				"listen: 127.0.0.1:8008",
				"pprof: disabled",
				"deep: disabled",
			},
		},
		{
			name:    "load error",
			load:    func() (*config.Config, error) { return nil, errors.New("invalid yaml") },
			wantErr: true,
		},
		{
			name:    "nil config",
			load:    func() (*config.Config, error) { return nil, nil },
			wantErr: true,
		},
		{
			// 配置本身不合法时 doctor 必须失败：它的第一职责就是在部署前拦住这种配置。
			name:    "invalid config fails",
			load:    func() (*config.Config, error) { return &config.Config{Env: "dev"}, nil },
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cmd := newDoctorCmdWithDeps(tt.load, noProbes)
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)

			err := cmd.Execute()
			if (err != nil) != tt.wantErr {
				t.Fatalf("Execute() error = %v, wantErr %v (output: %s)", err, tt.wantErr, out.String())
			}
			for _, want := range tt.wantOutput {
				if !strings.Contains(out.String(), want) {
					t.Fatalf("output missing %q: %s", want, out.String())
				}
			}
			if strings.Contains(out.String(), "password") || strings.Contains(out.String(), "secret") {
				t.Fatalf("output contains a sensitive field name: %s", out.String())
			}
		})
	}
}

// TestDoctorJSONOutput 验证 --json 产出可被机器解析的固定 schema。
func TestDoctorJSONOutput(t *testing.T) {
	t.Parallel()

	cmd := newDoctorCmdWithDeps(func() (*config.Config, error) { return configWithRealKeysForDoctor(t), nil }, noProbes)
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--json"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("Execute(--json): %v (output: %s)", err, out.String())
	}

	var report diagnostics.Report
	if err := json.Unmarshal(out.Bytes(), &report); err != nil {
		t.Fatalf("--json output is not valid JSON: %v\n%s", err, out.String())
	}
	if report.SchemaVersion == 0 {
		t.Fatal("--json output has no schema_version")
	}
	if len(report.Checks) == 0 {
		t.Fatal("--json output has no checks")
	}
}

// TestDoctorDeepExitCodes 钉住 deep 模式的退出码契约：
// required 依赖不可达 → 1；仅 optional 依赖不可达 → 2。
//
// 两者退成同一个码时，部署脚本要么把降级当致命拦住发布，
// 要么把致命当降级放行一台起不来的实例。
func TestDoctorDeepExitCodes(t *testing.T) {
	t.Parallel()

	down := func(context.Context) error { return errors.New("connection refused") }
	up := func(context.Context) error { return nil }

	cases := []struct {
		name     string
		probes   diagnostics.Probes
		wantCode int
	}{
		{
			name:     "required dependency down exits 1",
			probes:   diagnostics.Probes{PingDB: down, PingRedis: up},
			wantCode: diagnostics.ExitFailed,
		},
		{
			name:     "optional dependency down exits 2",
			probes:   diagnostics.Probes{PingDB: up, PingRedis: down},
			wantCode: diagnostics.ExitDegraded,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			cmd := newDoctorCmdWithDeps(
				func() (*config.Config, error) { return configWithRealKeysForDoctor(t), nil },
				func(context.Context) (diagnostics.Probes, func() error, error) {
					return tc.probes, func() error { return nil }, nil
				})
			var out bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&out)
			cmd.SetArgs([]string{"--deep"})

			err := cmd.Execute()
			if err == nil {
				t.Fatalf("Execute(--deep) returned nil, want a coded error (output: %s)", out.String())
			}

			var coded interface{ ExitCode() int }
			if !errors.As(err, &coded) {
				t.Fatalf("error %v does not carry an exit code", err)
			}
			if got := coded.ExitCode(); got != tc.wantCode {
				t.Fatalf("exit code = %d, want %d", got, tc.wantCode)
			}
		})
	}
}

// TestDoctorDeepProbeBuildFailureIsReported 验证 deep 探测无法建立时命令不崩：
// 连不上正是它要报告的结论，报告里以 unreachable 呈现。
func TestDoctorDeepProbeBuildFailureIsReported(t *testing.T) {
	t.Parallel()

	cmd := newDoctorCmdWithDeps(
		func() (*config.Config, error) { return configWithRealKeysForDoctor(t), nil },
		func(context.Context) (diagnostics.Probes, func() error, error) {
			return diagnostics.Probes{}, func() error { return nil }, errors.New("cannot open resources")
		})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--deep"})

	_ = cmd.Execute()

	if !strings.Contains(out.String(), "deep probes unavailable") {
		t.Fatalf("output does not report the probe build failure: %s", out.String())
	}
}

func TestEnabledLabel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		enabled bool
		want    string
	}{
		{name: "enabled", enabled: true, want: "enabled"},
		{name: "disabled", enabled: false, want: "disabled"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := enabledLabel(tt.enabled); got != tt.want {
				t.Fatalf("enabledLabel(%v) = %q, want %q", tt.enabled, got, tt.want)
			}
		})
	}
}
