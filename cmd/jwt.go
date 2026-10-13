package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gtkit/gin-api/internal/pkg/jwtauth"

	"github.com/gtkit/gojwt"
	"github.com/spf13/cobra"
)

func init() {
	RegisterRootCommand(newJWTCmd)
}

// newJWTCmd 构造 jwt 子命令树。
func newJWTCmd(_ *rootOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "jwt",
		Short: "Manage JWT key material",
		// 旧的无子命令用法必须**失败**而不是静默生成默认文件。
		//
		// 旧行为是直接覆盖 config/pem/jwtpri.pem，在轮换语义下那是破坏性的：
		// 覆盖 active 私钥会让全部存量令牌立刻失效。
		RunE: func(_ *cobra.Command, args []string) error {
			if len(args) == 0 {
				return errors.New(
					"`jwt` no longer generates keys directly: use `jwt generate --kid <kid> --out-dir <dir>`. " +
						"Keys are now identified by a kid, and rotation means creating a new kid — " +
						"never overwriting an existing one")
			}
			return fmt.Errorf("unknown jwt subcommand %q", args[0])
		},
	}
	cmd.AddCommand(newJWTGenerateCmd())
	return cmd
}

// newJWTGenerateCmd 构造 `jwt generate`。
func newJWTGenerateCmd() *cobra.Command {
	var (
		kid    string
		outDir string
	)

	cmd := &cobra.Command{
		Use:   "generate",
		Short: "Generate an Ed25519 key pair (PKCS#8 private / PKIX public) identified by a kid",
		Args:  cobra.NoArgs,
		// 退出码必须反映失败：旧实现用 Run + fmt.Printf，密钥生成失败时
		// 退出码仍是 0，于是自动化脚本会当作成功继续往下走。
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := jwtauth.ValidateKID(kid); err != nil {
				return commandError("generate jwt key", err)
			}
			result, err := generateKeyPair(kid, outDir)
			if err != nil {
				return commandError("generate jwt key", err)
			}

			// stdout **绝不输出私钥内容**：这条命令的输出经常被直接粘进
			// 工单、聊天窗口或 CI 日志。
			cmd.Printf("kid: %s\n", result.kid)
			cmd.Printf("private key: %s (mode 0600)\n", result.privatePath)
			cmd.Printf("public key:  %s\n", result.publicPath)
			cmd.Println()
			cmd.Println("config snippet:")
			cmd.Printf("  jwt:\n    active:\n      kid: %s\n      private_key_path: %s\n      public_key_path: %s\n",
				result.kid, result.privatePath, result.publicPath)
			return nil
		},
	}

	cmd.Flags().StringVar(&kid, "kid", "", "key id (required); also used as the key file name prefix")
	cmd.Flags().StringVar(&outDir, "out-dir", "config/pem", "directory to write the key pair into")
	_ = cmd.MarkFlagRequired("kid")

	return cmd
}

// generateResult 是一次生成的产物路径。
type generateResult struct {
	kid         string
	privatePath string
	publicPath  string
}

// generateKeyPair 使用 gojwt 生成 PKCS#8 私钥与 PKIX 公钥。
// gojwt 通过 O_EXCL 拒绝覆盖，并在写入失败时清理本次创建的文件。
// 本命令额外校验输出目录、固定权限并同步目录项；轮换须使用新 kid。
func generateKeyPair(kid, outDir string) (generateResult, error) {
	dir, err := resolveOutDir(outDir)
	if err != nil {
		return generateResult{}, err
	}

	privatePath := filepath.Join(dir, kid+".private.pem")
	publicPath := filepath.Join(dir, kid+".public.pem")

	// 友好报错：先看目标是否已存在。这不是安全边界（O_EXCL 才是），
	// 但它能在真正动手之前给出一条清楚的信息。
	for _, path := range []string{privatePath, publicPath} {
		if _, statErr := os.Lstat(path); statErr == nil {
			return generateResult{}, fmt.Errorf(
				"%s already exists: a kid identifies one immutable key pair, so rotation means "+
					"generating a new kid rather than replacing this one", path)
		}
	}

	if err := gojwt.GenerateEd25519Keys(privatePath, publicPath); err != nil {
		return generateResult{}, fmt.Errorf("generate ed25519 keys: %w", err)
	}
	cleanup := func() {
		_ = os.Remove(privatePath)
		_ = os.Remove(publicPath)
	}
	// 创建权限受 umask 影响；保留命令原有的精确权限约定。
	for _, file := range []struct {
		path string
		mode os.FileMode
	}{{privatePath, 0o600}, {publicPath, 0o644}} {
		if err := os.Chmod(file.path, file.mode); err != nil {
			cleanup()
			return generateResult{}, fmt.Errorf("chmod key file: %w", err)
		}
	}

	// fsync 父目录，保证目录项本身落盘——否则崩溃后可能出现
	// "文件内容在但目录里看不到"的状态。
	if err := syncDir(dir); err != nil {
		cleanup()
		return generateResult{}, err
	}

	return generateResult{kid: kid, privatePath: privatePath, publicPath: publicPath}, nil
}

// resolveOutDir 校验输出目录是真实目录且不是符号链接。
//
// 拒绝符号链接：`--out-dir` 指向一个链接时，密钥会落到链接目标处，
// 而调用者以为写在了自己指定的位置——那对密钥材料是不可接受的歧义。
func resolveOutDir(outDir string) (string, error) {
	if outDir == "" {
		return "", errors.New("out-dir must not be empty")
	}

	info, err := os.Lstat(outDir)
	if err != nil {
		return "", fmt.Errorf("stat out-dir %s: %w", outDir, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf(
			"out-dir %s is a symlink: key material must be written to the path you named, "+
				"not to wherever a link happens to point", outDir)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("out-dir %s is not a directory", outDir)
	}
	return outDir, nil
}
