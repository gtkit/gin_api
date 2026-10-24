package cmd

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/gtkit/gin-api/internal/platform/config"

	"github.com/gtkit/gin-api/internal/pkg/jwtauth"

	"github.com/spf13/cobra"
)

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
	cmd.AddCommand(newJWTGenerateCmd(), newJWTReencodeCmd())
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
			result, err := generateConfiguredKeyPair(cmd, kid, outDir)
			if err != nil {
				return commandError("generate jwt key", err)
			}

			// stdout **绝不输出私钥内容**：这条命令的输出经常被直接粘进
			// 工单、聊天窗口或 CI 日志。
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "kid: %s\nprivate key: %s (mode 0600)\npublic key:  %s\n\nconfig snippet:\n  jwt:\n    active:\n      kid: %s\n      private_key_path: %s\n      public_key_path: %s\n",
				result.kid, result.privatePath, result.publicPath, result.kid, result.privatePath, result.publicPath)
			return commandError("write generated key summary", err)
		},
	}

	cmd.Flags().StringVar(&kid, "kid", "", "key id; defaults to jwt.active.kid, required explicitly with --out-dir")
	cmd.Flags().StringVar(&outDir, "out-dir", "", "override env key paths with <kid>.private.pem/<kid>.public.pem in an existing directory; if omitted, read jwt.active from APP_ROOT/config/env/env.yml (APP_ROOT defaults to cwd)")

	return cmd
}

// generateConfiguredKeyPair 以显式目录参数优先，否则读取 env 的完整密钥路径。
func generateConfiguredKeyPair(cmd *cobra.Command, kid, outDir string) (generateResult, error) {
	if cmd.Flags().Changed("out-dir") {
		if err := jwtauth.ValidateKID(kid); err != nil {
			return generateResult{}, fmt.Errorf("--kid is required with --out-dir: %w", err)
		}
		return generateKeyPair(kid, outDir)
	}
	keys, err := config.LoadJWTKeyConfigForCommand()
	if err != nil {
		return generateResult{}, fmt.Errorf("load JWT generation paths: %w", err)
	}
	if !cmd.Flags().Changed("kid") {
		kid = keys.KID
	}
	if validateErr := jwtauth.ValidateKID(kid); validateErr != nil {
		return generateResult{}, fmt.Errorf("validate generation kid: %w", validateErr)
	}
	return generateKeyFiles(kid, keys.PrivateKeyPath, keys.PublicKeyPath)
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

	return generateKeyFiles(kid, filepath.Join(dir, kid+".private.pem"), filepath.Join(dir, kid+".public.pem"))
}

// generateKeyFiles 保留配置中的完整文件名和父目录，不重新推导密钥位置。
func generateKeyFiles(kid, privatePath, publicPath string) (_ generateResult, err error) {
	dirs := []string{filepath.Dir(privatePath)}
	if dir := filepath.Dir(publicPath); dir != dirs[0] {
		dirs = append(dirs, dir)
	}
	for _, dir := range dirs {
		if _, err := resolveOutDir(dir); err != nil {
			return generateResult{}, err
		}
	}

	// 友好报错：先看目标是否已存在。这不是安全边界（O_EXCL 才是），
	// 但它能在真正动手之前给出一条清楚的信息。
	for _, path := range []string{privatePath, publicPath} {
		if _, statErr := os.Lstat(path); statErr == nil {
			return generateResult{}, fmt.Errorf(
				"%s already exists: a kid identifies one immutable key pair, so rotation means "+
					"generating a new kid rather than replacing this one: %w", path, os.ErrExist)
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return generateResult{}, fmt.Errorf("inspect key output %s: %w", path, statErr)
		}
	}

	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return generateResult{}, fmt.Errorf("generate ed25519 keys: %w", err)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return generateResult{}, fmt.Errorf("encode private key: %w", err)
	}
	publicDER, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		return generateResult{}, fmt.Errorf("encode public key: %w", err)
	}
	outputs := []struct {
		path   string
		mode   os.FileMode
		data   []byte
		file   *os.File
		closed bool
	}{
		{path: privatePath, mode: 0o600, data: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})},
		{path: publicPath, mode: 0o644, data: pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER})},
	}
	complete := false
	defer func() {
		for i := len(outputs) - 1; i >= 0; i-- {
			output := &outputs[i]
			if output.file == nil {
				continue
			}
			if !output.closed {
				if closeErr := output.file.Close(); closeErr != nil {
					err = errors.Join(err, fmt.Errorf("close generated key %s: %w", output.path, closeErr))
				}
			}
			if !complete {
				if removeErr := os.Remove(output.path); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
					err = errors.Join(err, fmt.Errorf("remove generated key %s: %w", output.path, removeErr))
				}
			}
		}
	}()
	// Both exclusive creates precede writing either key. Only acquired files are owned.
	for i := range outputs {
		output := &outputs[i]
		file, openErr := os.OpenFile(output.path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, output.mode)
		if openErr != nil {
			return generateResult{}, fmt.Errorf("create key output %s: %w", output.path, openErr)
		}
		output.file = file
	}
	for i := range outputs {
		output := &outputs[i]
		n, writeErr := output.file.Write(output.data)
		if writeErr == nil && n != len(output.data) {
			writeErr = io.ErrShortWrite
		}
		if writeErr != nil {
			return generateResult{}, fmt.Errorf("write generated key %s: %w", output.path, writeErr)
		}
		if syncErr := output.file.Sync(); syncErr != nil {
			return generateResult{}, fmt.Errorf("sync generated key %s: %w", output.path, syncErr)
		}
		output.closed = true
		if closeErr := output.file.Close(); closeErr != nil {
			return generateResult{}, fmt.Errorf("close generated key %s: %w", output.path, closeErr)
		}
		if chmodErr := os.Chmod(output.path, output.mode); chmodErr != nil {
			return generateResult{}, fmt.Errorf("chmod generated key %s: %w", output.path, chmodErr)
		}
	}

	// fsync 父目录，保证目录项本身落盘——否则崩溃后可能出现
	// "文件内容在但目录里看不到"的状态。
	for _, dir := range dirs {
		if err := syncDir(dir); err != nil {
			return generateResult{}, err
		}
	}

	complete = true
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
