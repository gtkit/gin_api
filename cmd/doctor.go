package cmd

import (
	"context"
	"errors"
	"fmt"
	"net"
	"time"

	defaultconfig "github.com/gtkit/gin-api/internal/plugin/builtin/config"
	defaultmysql "github.com/gtkit/gin-api/internal/plugin/builtin/mysql"
	defaultredis "github.com/gtkit/gin-api/internal/plugin/builtin/redis"

	"github.com/gtkit/gin-api/internal/bootstrap"
	"github.com/gtkit/gin-api/internal/diagnostics"
	"github.com/gtkit/gin-api/internal/platform/config"

	"github.com/spf13/cobra"
)

// configLoader 抽象配置加载动作，便于测试注入桩替换 defaultconfig.Load。
type configLoader func() (*config.Config, error)

// probeBuilder 构造 deep 检查所需的真实探测能力，并返回释放函数。
//
// 参数化便于测试：deep 路径要建真实连接，测试里必须能换掉它，
// 否则这条路径要么测不到、要么依赖一台真库。
type probeBuilder func(ctx context.Context, cfg *config.Config) (diagnostics.Probes, func() error, error)

func init() {
	RegisterRootCommand(newDoctorCmd)
}

// newDoctorCmd 构造 doctor 子命令，默认使用 defaultconfig.Load 与真实资源探测。
func newDoctorCmd(_ *rootOptions) *cobra.Command {
	return newDoctorCmdWithDeps(defaultconfig.Load, buildRuntimeProbes)
}

// newDoctorCmdWithDeps 构造 doctor 子命令：不启动任何服务，只做部署前自检。
//
// 默认（无 --deep）只校验配置与文件元数据，不做任何网络 I/O——
// doctor 会在连不上生产依赖的环境里被执行（如 project init 的收尾），
// 默认就打真实连接会让它在完全正常的情况下报错。
//
// --deep 才执行连通性与目录可写检查。全程只读不修改。
func newDoctorCmdWithDeps(load configLoader, buildProbes probeBuilder) *cobra.Command {
	var (
		deep     bool
		asJSON   bool
		perCheck time.Duration
	)

	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Validate runtime configuration without starting services",
		Args:  cobra.NoArgs,
		// 退出码由报告推导（0 全通 / 2 仅 optional 降级 / 1 失败），
		// 因此这里自行控制输出，不让 cobra 再打印一遍用法。
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) (err error) {
			cfg, err := load()
			if err != nil {
				return commandError("validate runtime configuration", err)
			}
			if cfg == nil {
				return fmt.Errorf("validate runtime configuration: config loader returned nil")
			}

			// 必需性由装配层的能力推导给出，不让诊断层自己再算一遍。
			//
			// 命令层是唯一同时看得到装配层与诊断层的位置，因此注入点在这里：
			// 诊断层若反向 import 装配层，就会在装配起不来时连诊断也用不了，
			// 而那正是最需要诊断的时候。
			opts := diagnostics.Options{
				Deep:          deep,
				Timeout:       perCheck,
				RedisRequired: bootstrap.RedisRequired(cfg),
			}
			if deep && buildProbes != nil {
				probes, release, probeErr := buildProbes(cmd.Context(), cfg)
				if release != nil {
					defer func() {
						if closeErr := release(); closeErr != nil {
							err = &exitCodeError{code: diagnostics.ExitFailed, cause: errors.Join(err, commandError("release deep probes", closeErr))}
						}
					}()
				}
				if probeErr != nil {
					opts.ProbesUnavailable = true
					if _, writeErr := fmt.Fprintf(cmd.ErrOrStderr(), "deep probes unavailable: %v\n", probeErr); writeErr != nil {
						return errors.Join(probeErr, commandError("write deep probe diagnostic", writeErr))
					}
				} else {
					opts.Probes = probes
				}
			}

			report := diagnostics.Run(cmd.Context(), cfg, opts)

			if asJSON {
				if err := diagnostics.WriteJSON(cmd.OutOrStdout(), report); err != nil {
					return commandError("write doctor report", err)
				}
			} else {
				if err := printHumanReport(cmd, cfg, report); err != nil {
					return commandError("write doctor report", err)
				}
			}

			if code := report.ExitCode(); code != diagnostics.ExitOK {
				return &exitCodeError{code: code, report: report}
			}
			return nil
		},
	}

	cmd.Flags().BoolVar(&deep, "deep", false,
		"run runtime probes (database/redis connectivity, log directory writability)")
	cmd.Flags().BoolVar(&asJSON, "json", false, "emit a machine-readable report")
	cmd.Flags().DurationVar(&perCheck, "check-timeout", 3*time.Second, "per-check timeout")

	return cmd
}

// exitCodeError 携带 doctor 的退出码。
//
// doctor 的退出码是**契约**（0/2/1），脚本按它判定；用普通 error 会让所有
// 非零情形都退成 1，"仅 optional 降级"与"required 失败"就无法区分。
type exitCodeError struct {
	code   int
	report diagnostics.Report
	cause  error
}

func (e *exitCodeError) Error() string {
	if e.cause != nil {
		return e.cause.Error()
	}
	failed, degraded := 0, 0
	for _, c := range e.report.Checks {
		switch c.Status {
		case diagnostics.StatusFail:
			failed++
		case diagnostics.StatusDegraded:
			degraded++
		case diagnostics.StatusPass, diagnostics.StatusSkipped:
		}
	}
	return fmt.Sprintf("doctor found %d failed and %d degraded check(s)", failed, degraded)
}

// ExitCode 让根命令取出应当使用的进程退出码。
func (e *exitCodeError) ExitCode() int { return e.code }
func (e *exitCodeError) Unwrap() error { return e.cause }

func printHumanReport(cmd *cobra.Command, cfg *config.Config, report diagnostics.Report) error {
	if _, err := fmt.Fprintf(cmd.OutOrStdout(), "config: ok\nenv: %s\napplication: %s\nlisten: %s\npprof: %s\ndeep: %s\n", cfg.Env, cfg.App.Name, net.JoinHostPort(cfg.App.Host, cfg.App.Port), enabledLabel(cfg.Pprof.Enabled), enabledLabel(report.Deep)); err != nil {
		return err
	}
	for _, check := range report.Checks {
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%-18s %-9s %-22s %s\n", check.Name, check.Status, check.ErrorCode, check.Detail); err != nil {
			return err
		}
	}
	return nil
}

// buildRuntimeProbes 只绑定已加载配置，不做 I/O 或初始化完整 Runtime。
// 各探针在 diagnostics 的独立预算内建连、校验和释放；一项失败不抹掉其他结果。
func buildRuntimeProbes(_ context.Context, cfg *config.Config) (diagnostics.Probes, func() error, error) {
	return diagnostics.Probes{
		PingDB: func(ctx context.Context) error {
			client, err := defaultmysql.OpenDB(ctx, cfg.Database, cfg.Database.RuntimeCredentials(), nil)
			if err != nil {
				return err
			}
			return client.Close()
		},
		PingRedis: func(ctx context.Context) error { return defaultredis.ProbeRedis(ctx, cfg.Redis) },
	}, func() error { return nil }, nil
}

// enabledLabel 把布尔开关渲染为人类可读的 "enabled"/"disabled" 文案，用于自检输出。
func enabledLabel(enabled bool) string {
	if enabled {
		return "enabled"
	}

	return "disabled"
}
