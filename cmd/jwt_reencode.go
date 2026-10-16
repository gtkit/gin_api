package cmd

import (
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
			cmd.Printf("private key: %s (PKCS#8, mode 0600)\n", filepath.Join(outDir, "jwtpri.pem"))
			cmd.Printf("public key:  %s (PKIX, mode 0644)\n", filepath.Join(outDir, "jwtpub.pem"))
			cmd.Println("Key material preserved. Point jwt.active key paths to these files; keep the existing kid and verification policy.")
			return nil
		},
	}
	command.Flags().StringVar(&privatePath, "private-key", "", "existing Ed25519 private PEM (required; read only)")
	command.Flags().StringVar(&publicPath, "public-key", "", "matching Ed25519 public PEM (required; read only)")
	command.Flags().StringVar(&outDir, "out-dir", "", "new output directory (required; parent must exist; refuses existing paths)")
	for _, name := range []string{"private-key", "public-key", "out-dir"} {
		_ = command.MarkFlagRequired(name)
	}
	return command
}
