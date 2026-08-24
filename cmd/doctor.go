package cmd

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/gtkit/gin-api/internal/bootstrap"
	"github.com/gtkit/gin-api/internal/diagnostics"
	"github.com/gtkit/gin-api/internal/platform/config"

	"github.com/spf13/cobra"
)

// configLoader 抽象配置加载动作，便于测试注入桩替换 config.Load。
type configLoader func() (*config.Config, error)

// probeBuilder 构造 deep 检查所需的真实探测能力，并返回释放函数。
//
// 参数化便于测试：deep 路径要建真实连接，测试里必须能换掉它，
// 否则这条路径要么测不到、要么依赖一台真库。
type probeBuilder func(ctx context.Context) (diagnostics.Probes, func() error, error)

func init() {
	RegisterRootCommand(newDoctorCmd)
}

// newDoctorCmd 构造 doctor 子命令，默认使用 config.Load 与真实资源探测。
func newDoctorCmd(_ *rootOptions) *cobra.Command {
	return newDoctorCmdWithDeps(config.Load, buildRuntimeProbes)
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
		RunE: func(cmd *cobra.Command, _ []string) error {
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
				probes, release, probeErr := buildProbes(cmd.Context())
				if probeErr != nil {
					// 连不上正是 deep 检查要报告的结论本身，不该让命令直接崩掉。
					// 必须显式告知诊断层"探测能力没建起来"：只把 Probes 留空的话，
					// 它与"没接探针"无法区分，会被记成 skipped 而让退出码变成 0。
					cmd.PrintErrf("deep probes unavailable: %v\n", probeErr)
					opts.ProbesUnavailable = true
				} else {
					// 关闭错误要可见但不翻转诊断结论：doctor 的退出码由报告推导，
					// 探针连接自身的收尾失败打到 stderr 供操作者判断。
					defer func() {
						if cerr := release(); cerr != nil {
							cmd.PrintErrf("release deep probes: %v\n", cerr)
						}
					}()
					opts.Probes = probes
				}
			}

			report := diagnostics.Run(cmd.Context(), cfg, opts)

			if asJSON {
				if err := diagnostics.WriteJSON(cmd.OutOrStdout(), report); err != nil {
					return commandError("write doctor report", err)
				}
			} else {
				printHumanReport(cmd, cfg, report)
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
}

func (e *exitCodeError) Error() string {
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

func printHumanReport(cmd *cobra.Command, cfg *config.Config, report diagnostics.Report) {
	cmd.Println("config: ok")
	cmd.Printf("env: %s\n", cfg.Env)
	cmd.Printf("application: %s\n", cfg.App.Name)
	cmd.Printf("listen: %s\n", net.JoinHostPort(cfg.App.Host, cfg.App.Port))
	cmd.Printf("pprof: %s\n", enabledLabel(cfg.Pprof.Enabled))
	cmd.Printf("deep: %s\n", enabledLabel(report.Deep))

	for _, c := range report.Checks {
		cmd.Printf("%-18s %-9s %-22s %s\n", c.Name, c.Status, c.ErrorCode, c.Detail)
	}
}

// buildRuntimeProbes 建立 deep 检查所需的真实连接。
//
// 复用 bootstrap 的资源装配，而不是在诊断代码里另写一份建连逻辑——
// 另写一份就意味着 doctor 检查的连接与服务实际使用的连接可能采用不同的
// 版本校验、会话语义与超时，于是"doctor 通过"不再说明服务能起来。
func buildRuntimeProbes(ctx context.Context) (diagnostics.Probes, func() error, error) {
	rt, err := bootstrap.NewRuntime(ctx, bootstrap.Options{})
	if err != nil {
		return diagnostics.Probes{}, func() error { return nil }, err
	}

	resources := rt.Resources()
	return diagnostics.Probes{
		PingDB:    resources.PingDB,
		PingRedis: resources.PingRedis,
	}, rt.Close, nil
}

// enabledLabel 把布尔开关渲染为人类可读的 "enabled"/"disabled" 文案，用于自检输出。
func enabledLabel(enabled bool) string {
	if enabled {
		return "enabled"
	}

	return "disabled"
}
