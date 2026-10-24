package cmd

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/gtkit/gin-api/internal/bootstrap"
	"github.com/gtkit/gin-api/internal/models"

	migrate "github.com/gtkit/migratex"
	"github.com/spf13/cobra"
)

// buildMigrationCommands 初始化运行时并构建 migratex 命令集，仅在执行迁移子命令时调用。
//
// ctx 由调用方（cobra 命令）传入而不是用背景上下文：中断信号因此能覆盖数据库
// 初始化——否则 Ctrl+C 之后进程还会停在建连上，直到它自己的超时到点。
//
// 迁移凭据：迁移是唯一需要 DDL 权限的路径，配置了迁移专用账号时用它，
// 未配置则回退运行凭据并告警（不失败，避免破坏尚未配置迁移凭据的存量部署）。
func buildMigrationCommands(ctx context.Context, shutdown *bootstrap.ShutdownCoordinator) (*migrate.Commands, func() error, error) {
	// module path 必须先解析：解析失败时不该先把数据库连上再报错。
	modulePath, err := resolveModulePath()
	if err != nil {
		return nil, nil, err
	}

	// 最小装配：只要配置 + 日志 + 数据库。
	// 此前走完整 NewRuntime，而 Redis 连不上会直接 panic——于是"Redis 挂了 ⇒ 数据库迁移也跑不了"，
	// 两个无关依赖被绑死，而迁移恰恰是故障恢复时最需要能执行的操作。
	rt, err := bootstrap.NewMigrationRuntime(ctx, bootstrap.MigrationOptions{Shutdown: shutdown})
	if err != nil {
		return nil, nil, err
	}

	closeRuntime := func() error {
		stopCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		return rt.Close(stopCtx)
	}
	cmds, err := migrate.NewCommands(
		rt.DB(),
		// 必须是**当前项目自己的** module path：它是生成代码的 import 路径，
		// 硬编码母版路径会让派生项目静默生成 import 母版的代码。
		migrate.WithProjectName(modulePath),
		migrate.WithMigrationDir("database/migrations"),
		// 新增持久化模型时同步登记，供 --from-model 和 DDL 差异检查使用。
		migrate.WithDDLModels(
			&models.User{},
			&models.UserRole{},
			&models.OperationAudit{},
		),
		migrate.WithTimeout(5*time.Minute),
	)
	if err != nil {
		return nil, nil, errors.Join(err, closeRuntime())
	}

	return cmds, closeRuntime, nil
}

// firstToken 返回第一个非 flag 参数（用于判断子命令名，如 up/down/status/make）；
// 因 migrate/make 关闭了 cobra 的 flag 解析，需自行从原始 args 里取出该 token。
func firstToken(args []string) string {
	for _, a := range args {
		if !strings.HasPrefix(a, "-") {
			return a
		}
	}
	return ""
}

// runMigratexSubtree 把参数转发给 migratex 内层命令；帮助或无参时不连库。
func runMigratexSubtree(
	cmd *cobra.Command, args []string, shutdown *bootstrap.ShutdownCoordinator, pick func(*migrate.Commands) *cobra.Command, action string,
) (err error) {
	if tok := firstToken(args); tok == "" || tok == "help" {
		return cmd.Help()
	}

	cmds, closeRuntime, err := buildMigrationCommands(cmd.Context(), shutdown)
	if err != nil {
		return commandError("bootstrap runtime", err)
	}
	return executeMigrationCommand(cmd.Context(), pick(cmds), args, closeRuntime, action)
}

// executeMigrationCommand keeps command cancellation and both execution/cleanup
// errors. It performs no default selection or additional runtime construction.
func executeMigrationCommand(ctx context.Context, sub *cobra.Command, args []string, closeRuntime func() error, action string) (err error) {
	defer func() { err = errors.Join(err, commandError("close migration runtime", closeRuntime())) }()
	sub.SilenceUsage = true
	sub.SilenceErrors = true
	sub.SetArgs(args)
	return commandError(action, sub.ExecuteContext(ctx))
}

// newMigrateCmd 构造 migrate 子命令：执行数据库迁移（up/down/status/lint/pretend）。
// 关闭 cobra flag 解析，把全部参数透传给内层 migratex 的 Migrate 子树；
// 仅在真正执行迁移子命令时才 bootstrap 运行时连库（help / 无参不连库）。
func newMigrateCmd(opts *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:                "migrate",
		Short:              "Run database migrations (up/down/status/lint/pretend)",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMigratexSubtree(cmd, args, migrationShutdown(opts),
				func(c *migrate.Commands) *cobra.Command { return c.Migrate }, "run migrate")
		},
	}
}

// newMakeCmd 构造 make 子命令：根据 GORM model 生成迁移文件（DDL 草稿）。
// 与 migrate 一样关闭 flag 解析并透传给 migratex 的 Make 子树，写入 database/migrations。
func newMakeCmd(opts *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:                "make",
		Short:              "Generate migration files from GORM models",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runMigratexSubtree(cmd, args, migrationShutdown(opts),
				func(c *migrate.Commands) *cobra.Command { return c.Make }, "run make")
		},
	}
}

func migrationShutdown(opts *rootOptions) *bootstrap.ShutdownCoordinator {
	if opts != nil && opts.signals != nil {
		return opts.signals.shutdown
	}
	return nil
}
