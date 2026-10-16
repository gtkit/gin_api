package jwtauth

import (
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/gtkit/gojwt"
)

// ReencodeEd25519Keys 将现有密钥转换为 PKCS#8 / PKIX，不生成新密钥。
// outDir 必须不存在；输出 jwtpri.pem（0600）和 jwtpub.pem（0644），原文件只读。
func ReencodeEd25519Keys(privatePath, publicPath, outDir string) error {
	if strings.TrimSpace(privatePath) == "" || strings.TrimSpace(publicPath) == "" || strings.TrimSpace(outDir) == "" {
		return errors.New("private-key, public-key and out-dir must not be empty")
	}
	privatePEM, err := os.ReadFile(privatePath)
	if err != nil {
		return fmt.Errorf("read private key: %w", err)
	}
	publicPEM, err := os.ReadFile(publicPath)
	if err != nil {
		return fmt.Errorf("read public key: %w", err)
	}
	privateKey, err := gojwt.ParseEd25519PrivateKeyPEM(privatePEM)
	if err != nil {
		return fmt.Errorf("parse private key: %w", err)
	}
	publicKey, err := gojwt.ParseEd25519PublicKeyPEM(publicPEM)
	if err != nil {
		return fmt.Errorf("parse public key: %w", err)
	}
	// 从 seed 校验完整私钥，并确认给定公钥配对；错误必须在写文件前发现。
	if _, validateErr := gojwt.NewJwtEd25519FromKeys(privateKey, publicKey); validateErr != nil {
		return fmt.Errorf("validate key pair: %w", validateErr)
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(privateKey)
	if err != nil {
		return fmt.Errorf("encode PKCS#8 private key: %w", err)
	}
	publicDER, err := x509.MarshalPKIXPublicKey(publicKey)
	if err != nil {
		return fmt.Errorf("encode PKIX public key: %w", err)
	}
	return writeReencodedKeys(outDir, privateDER, publicDER)
}

func writeReencodedKeys(outDir string, privateDER, publicDER []byte) error {
	// Mkdir 拒绝任何已存在路径（含符号链接），不能使用 MkdirAll。
	if err := os.Mkdir(outDir, 0o700); err != nil {
		return fmt.Errorf("create new out-dir (must not exist): %w", err)
	}
	root, err := os.OpenRoot(outDir)
	if err != nil {
		return errors.Join(fmt.Errorf("open out-dir: %w", err), os.Remove(outDir))
	}
	for _, file := range []struct {
		name, label string
		der         []byte
		mode        os.FileMode
	}{
		{"jwtpri.pem", "PRIVATE KEY", privateDER, 0o600},
		{"jwtpub.pem", "PUBLIC KEY", publicDER, 0o644},
	} {
		data := pem.EncodeToMemory(&pem.Block{Type: file.label, Bytes: file.der})
		if err := writeReencodedFile(root, file.name, data, file.mode); err != nil {
			// 仅清理本次创建的新目录中的固定文件，绝不清理输入文件。
			privateErr := root.Remove("jwtpri.pem")
			publicErr := root.Remove("jwtpub.pem")
			if errors.Is(privateErr, os.ErrNotExist) {
				privateErr = nil
			}
			if errors.Is(publicErr, os.ErrNotExist) {
				publicErr = nil
			}
			return errors.Join(err, privateErr, publicErr, root.Close(), os.Remove(outDir))
		}
	}
	if err := root.Close(); err != nil {
		return fmt.Errorf("close out-dir: %w", err)
	}
	return nil
}

func writeReencodedFile(root *os.Root, name string, data []byte, mode os.FileMode) error {
	file, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return fmt.Errorf("create %s: %w", name, err)
	}
	// Chmod 确保产物权限明确，不被调用进程的 umask 改成不同值。
	if err := file.Chmod(mode); err != nil {
		return errors.Join(fmt.Errorf("chmod %s: %w", name, err), file.Close())
	}
	if _, err := file.Write(data); err != nil {
		return errors.Join(fmt.Errorf("write %s: %w", name, err), file.Close())
	}
	if err := file.Sync(); err != nil {
		return errors.Join(fmt.Errorf("sync %s: %w", name, err), file.Close())
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", name, err)
	}
	return nil
}
