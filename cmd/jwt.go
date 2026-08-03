package cmd

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gtkit/gin-api/internal/pkg/jwtauth"

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
		Short: "Generate an Ed25519 key pair identified by a kid",
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

// generateKeyPair 生成一对密钥并原子发布。
//
// 发布流程与其理由：
//
//  1. 先在内存里生成并**重新解析验证公私钥匹配**——直接往目标路径写的话，
//     第二个文件写失败会留下一对不匹配的密钥，而那种状态到启动时才暴露；
//  2. 最终发布用 `O_CREATE|O_EXCL` 而不是 `os.Rename`：Rename 会**覆盖**目标。
//     kid 必须稳定标识一组不可变的密钥材料——同 kid 原地替换会破坏缓存与
//     审计语义，也让"这个 kid 对应哪把密钥"变成一个时间相关的问题；
//  3. `Lstat` 只用于给出友好报错，`O_EXCL` 才是并发与 TOCTOU 下的最终防线；
//  4. 任一步失败删除本次新建的最终文件，不留半成品。
//
// **不提供 --force**：轮换的正确做法是生成新 kid，再把旧公钥移入 previous。
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

	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return generateResult{}, fmt.Errorf("generate ed25519 key: %w", err)
	}

	privatePEM, publicPEM, err := encodeKeyPair(private, public)
	if err != nil {
		return generateResult{}, err
	}

	// 发布**之前**验证配对：发布之后再发现不匹配，就要面对"已经写了一半"
	// 的清理问题，而清理本身也可能失败。
	if err := verifyEncodedPair(privatePEM, publicPEM); err != nil {
		return generateResult{}, err
	}

	created := make([]string, 0, 2)
	cleanup := func() {
		for _, path := range created {
			_ = os.Remove(path)
		}
	}

	if err := writeExclusive(privatePath, privatePEM, 0o600); err != nil {
		cleanup()
		return generateResult{}, err
	}
	created = append(created, privatePath)

	if err := writeExclusive(publicPath, publicPEM, 0o644); err != nil {
		cleanup()
		return generateResult{}, err
	}
	created = append(created, publicPath)

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

// encodeKeyPair 把密钥编码成 PEM。
func encodeKeyPair(private ed25519.PrivateKey, public ed25519.PublicKey) (privatePEM, publicPEM []byte, err error) {
	privateDER, err := x509.MarshalPKCS8PrivateKey(private)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal private key: %w", err)
	}
	publicDER, err := x509.MarshalPKIXPublicKey(public)
	if err != nil {
		return nil, nil, fmt.Errorf("marshal public key: %w", err)
	}

	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER}),
		pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER}),
		nil
}

// verifyEncodedPair 重新解析并确认公私钥配对。
func verifyEncodedPair(privatePEM, publicPEM []byte) error {
	privBlock, _ := pem.Decode(privatePEM)
	pubBlock, _ := pem.Decode(publicPEM)
	if privBlock == nil || pubBlock == nil {
		return errors.New("generated key material is not valid PEM")
	}

	parsedPriv, err := x509.ParsePKCS8PrivateKey(privBlock.Bytes)
	if err != nil {
		return fmt.Errorf("re-parse generated private key: %w", err)
	}
	parsedPub, err := x509.ParsePKIXPublicKey(pubBlock.Bytes)
	if err != nil {
		return fmt.Errorf("re-parse generated public key: %w", err)
	}

	priv, ok := parsedPriv.(ed25519.PrivateKey)
	if !ok {
		return errors.New("generated private key is not ed25519")
	}
	pub, ok := parsedPub.(ed25519.PublicKey)
	if !ok {
		return errors.New("generated public key is not ed25519")
	}

	derived, ok := priv.Public().(ed25519.PublicKey)
	if !ok || !derived.Equal(pub) {
		return errors.New("generated key pair does not match")
	}
	return nil
}

// writeExclusive 以 O_EXCL 创建文件并写入。
//
// O_EXCL 是并发与 TOCTOU 下的最终防线：两个进程同时生成同一个 kid 时，
// 只有一个能创建成功，另一个立刻失败——而不是两者都以为自己写成功了。
func writeExclusive(path string, data []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, perm)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()

	if _, err := f.Write(data); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	// 显式 chmod：O_CREATE 的权限会被 umask 削减，而私钥必须确保是 0600。
	if err := f.Chmod(perm); err != nil {
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync %s: %w", path, err)
	}
	return nil
}

// syncDir fsync 目录项。
func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open dir %s for sync: %w", dir, err)
	}
	defer func() { _ = f.Close() }()

	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync dir %s: %w", dir, err)
	}
	return nil
}
