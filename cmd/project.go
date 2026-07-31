package cmd

import (
	"github.com/gtkit/gin-api/internal/appmeta"
	"github.com/gtkit/gin-api/internal/projectinit"

	"github.com/spf13/cobra"
)

func init() {
	RegisterRootCommand(newProjectCmd)
}

// newProjectCmd 构造 project 命令组：脚手架母版专用的项目级工具集合。
// 自身不做事，仅挂载子命令（当前为 init）。
func newProjectCmd(_ *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "project",
		Short: "Scaffold-mother project tooling",
	}
	cmd.AddCommand(newProjectInitCmd())
	return cmd
}

// newProjectInitCmd 构造 project init 子命令：把由脚手架母版（经 gonew 复制）实例化出的
// 新项目落地为独立项目。动作包括：按 --name 重写 appmeta 身份常量、重置覆盖率地板、
// 重新初始化 git。
//
// 有安全护栏：工作区不干净时拒绝执行，且拒绝在脚手架母版自身上运行；--dry-run 只预览不落盘。
// --name 为必填 flag。
func newProjectInitCmd() *cobra.Command {
	var name string
	var dryRun bool

	cmd := &cobra.Command{
		Use:   "init --name <name>",
		Short: "Finalize a project instantiated from the scaffold mother (run after gonew)",
		Long: "Rewrite appmeta identity, reset the coverage floor, and reinitialize git.\n" +
			"Guarded: refuses a dirty tree and the scaffold mother itself;\n" +
			"--dry-run previews without writing.",
		Example: "" +
			"  " + appmeta.CommandName + " project init --name my_service\n" +
			"  " + appmeta.CommandName + " project init --name my_service --dry-run",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			initializer, err := projectinit.New("")
			if err != nil {
				return commandError("create project initializer", err)
			}

			result, err := initializer.Run(projectinit.Options{Name: name, DryRun: dryRun})
			if err != nil {
				return commandError("run project init", err)
			}

			printProjectInitResult(cmd, result)
			return nil
		},
	}

	cmd.Flags().StringVar(&name, "name", "", "new project identity name (rewrites appmeta constants)")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview changes without writing files or reinitializing git")
	_ = cmd.MarkFlagRequired("name")

	return cmd
}

// printProjectInitResult 打印 project init 的执行结果：逐条列出被重写/删除的文件与提示信息，
// dry-run 模式下动词改为 "would rewrite/delete" 并说明未落盘、未重建 git。
func printProjectInitResult(cmd *cobra.Command, result *projectinit.Result) {
	rewriteAction := "rewrote"
	deleteAction := "deleted"
	if result.DryRun {
		rewriteAction = "would rewrite"
		deleteAction = "would delete"
	}

	for _, path := range result.Rewritten {
		cmd.Printf("%s %s\n", rewriteAction, path)
	}
	for _, path := range result.Deleted {
		cmd.Printf("%s %s\n", deleteAction, path)
	}
	for _, note := range result.Notes {
		cmd.Printf("note: %s\n", note)
	}

	if result.DryRun {
		cmd.Println("dry-run mode: no files were written and git was not reinitialized")
	}
}
