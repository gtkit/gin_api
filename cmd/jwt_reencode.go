package cmd

import (
	"fmt"
	"path/filepath"

	"github.com/gtkit/gin-api/internal/pkg/jwtauth"

	"github.com/spf13/cobra"
)

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
