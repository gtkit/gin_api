package cmd

import (
	"fmt"
	"path/filepath"

	"github.com/gtkit/gin-api/internal/pkg/jwtauth"

	"github.com/spf13/cobra"
)

// newJWTReencodeCmd 注册 jwt reencode，用于将已有的 Ed25519 密钥对转换为标准 PEM 编码。
// 命令校验公私钥配对后，在新目录中写入 jwtpri.pem（PKCS#8 私钥）和 jwtpub.pem（PKIX 公钥）。
// 三个参数均为必填，输出目录必须尚未创建，且父目录已存在。
//
//	./bin/gin_api jwt reencode --private-key old/private.pem --public-key old/public.pem --out-dir pem-standard
//
// 转换保留原密钥材料。启用新文件时，手动更新配置中的密钥路径并沿用原 kid，仍有效的旧令牌可继续验签。
func newJWTReencodeCmd() *cobra.Command {
	var privatePath, publicPath, outDir string
	command := &cobra.Command{
		Use:   "reencode",
		Short: "Convert existing Ed25519 keys to PKCS#8 / PKIX without changing key material",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := jwtauth.ReencodeEd25519Keys(privatePath, publicPath, outDir); err != nil {
				return commandError("reencode JWT keys", err)
			}
			_, err := fmt.Fprintf(cmd.OutOrStdout(), "private key: %s (PKCS#8, mode 0600)\npublic key:  %s (PKIX, mode 0644)\nKey material preserved. Point jwt.active key paths to these files; keep the existing kid and verification policy.\n", filepath.Join(outDir, "jwtpri.pem"), filepath.Join(outDir, "jwtpub.pem"))
			return commandError("write reencoded key summary", err)
		},
	}
	command.Flags().StringVar(&privatePath, "private-key", "", "existing Ed25519 private PEM (required; read only)")
	command.Flags().StringVar(&publicPath, "public-key", "", "matching Ed25519 public PEM (required; read only)")
	command.Flags().StringVar(&outDir, "out-dir", "", "new output directory (required; parent must exist; refuses existing paths)")
	for _, name := range []string{"private-key", "public-key", "out-dir"} {
		if err := command.MarkFlagRequired(name); err != nil {
			panic(fmt.Errorf("register required JWT flag %s: %w", name, err))
		}
	}
	return command
}
