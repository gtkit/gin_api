package cmd

import (
	"github.com/gtkit/gin-api/internal/appmeta"
	"github.com/gtkit/gin-api/internal/scaffold"

	"github.com/spf13/cobra"
)

func init() {
	RegisterRootCommand(newModuleCmd)
}

// newModuleCmd 构造 module 子命令：按分层约定一次性生成业务模块脚手架
// （module 的 application/dto/transport/http + repository + model + 迁移草稿），
// 并默认把新模块注册进 internal/bootstrap/providers.go。
//
// 参数与 flag：
//   - <name>          必填，模块名（ExactArgs(1)）。
//   - --field n:t     可重复，声明模型字段（如 --field nickname:string），支持 string/bool/
//     int 系/uint 系/float/time.Time 等类型。
//   - --force         覆盖已存在的生成文件。
//   - --dry-run       只预览将创建/更新的文件，不落盘。
//   - --no-bootstrap  跳过改写 providers.go 的自动注册（生成后需手动接线，见输出提示）。
func newModuleCmd(_ *rootOptions) *cobra.Command {
	var force bool
	var dryRun bool
	var noBootstrap bool
	var fields []string

	cmd := &cobra.Command{
		Use:   "module <name>",
		Short: "Generate module, repository, model, and route scaffolding",
		Long: "Generate module, repository, model, dto, migration scaffold, and bootstrap registration.\n" +
			"Supported field types: string, bool, int/int8/int16/int32/int64, uint/uint8/uint16/uint32/uint64,\n" +
			"float32, float64, time.Time, *time.Time.",
		Example: "" +
			"  " + appmeta.CommandName + " module admin\n" +
			"  " + appmeta.CommandName + " module staff --field nickname:string --field status:uint8\n" +
			"  " + appmeta.CommandName + " module article_tag --field title:string --field sort:int --field enabled:bool\n" +
			"  " + appmeta.CommandName + " module draft_feature --no-bootstrap --dry-run",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			generator, err := scaffold.NewModuleGenerator("")
			if err != nil {
				return commandError("create module generator", err)
			}

			result, err := generator.GenerateWithOptions(args[0], scaffold.GenerateOptions{
				Force:     force,
				DryRun:    dryRun,
				Bootstrap: !noBootstrap,
				Fields:    fields,
			})
			if err != nil {
				return commandError("generate module scaffold", err)
			}

			printGenerationSummary(cmd, result)
			return nil
		},
	}

	cmd.Flags().BoolVar(&force, "force", false, "overwrite existing generated files")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "preview scaffold changes without writing files")
	cmd.Flags().BoolVar(&noBootstrap, "no-bootstrap", false, "skip updating internal/bootstrap/providers.go to register the generated module")
	cmd.Flags().StringSliceVar(&fields, "field", nil, "repeatable field definition in name:type format, e.g. --field nickname:string")

	return cmd
}

// printGenerationSummary 打印一次脚手架生成的结果：逐条列出创建/更新/跳过的文件，
// 再输出计数汇总。dry-run 模式下动词改为 "would create/update" 并提示未落盘。
func printGenerationSummary(cmd *cobra.Command, result *scaffold.GenerateResult) {
	actionCreated := "created"
	actionUpdated := "updated"
	if result != nil && result.DryRun {
		actionCreated = "would create"
		actionUpdated = "would update"
	}

	for _, path := range result.Created {
		cmd.Printf("%s %s\n", actionCreated, path)
	}
	for _, path := range result.Updated {
		cmd.Printf("%s %s\n", actionUpdated, path)
	}
	for _, path := range result.Skipped {
		cmd.Printf("skipped %s\n", path)
	}

	cmd.Printf(
		"summary created=%d updated=%d skipped=%d\n",
		len(result.Created),
		len(result.Updated),
		len(result.Skipped),
	)

	if len(result.Created)+len(result.Updated) == 0 {
		cmd.Println("no scaffold changes were applied")
	} else if result != nil && result.DryRun {
		cmd.Println("dry-run mode: no files were written")
	}
}
