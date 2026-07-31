package cmd

import (
	"fmt"

	"github.com/gtkit/gin-api/internal/runtime/resource"

	"github.com/gtkit/gin-api/internal/pkg/console"

	"github.com/gtkit/gojwt"
	"github.com/spf13/cobra"
)

func init() {
	RegisterRootCommand(newJWTCmd)
}

// newJWTCmd 构造 jwt 子命令：为 JWT 生成 Ed25519 密钥材料。
// 直接执行密钥生成（无二级子命令），产物落到 config/pem/ 下供运行时鉴权使用。
func newJWTCmd(_ *rootOptions) *cobra.Command {
	return &cobra.Command{
		Use:   "jwt",
		Short: "Generate JWT key material",
		Run: func(_ *cobra.Command, _ []string) {
			generateEd25519Keys()
		},
	}
}

// generateEd25519Keys 生成一对 Ed25519 公私钥并写入 config/pem/jwtpri.pem、jwtpub.pem
// （路径经 resource.ResolvePath 解析，可执行文件同级优先）。
// 生成失败仅打印错误、不中断进程（该命令为一次性运维动作）；私钥文件属敏感物料，不进 git。
func generateEd25519Keys() {
	console.Success("Generate ed25519 keys start")
	if err := gojwt.GenerateEd25519Keys(
		resource.ResolvePath("./config/pem/jwtpri.pem"),
		resource.ResolvePath("./config/pem/jwtpub.pem"),
	); err != nil {
		fmt.Printf("generate ed25519 keys failed: %v\n", err)

		return
	}
	console.Success("Generate ed25519 keys success")
}
