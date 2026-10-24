//go:build !windows

package cmd

import (
	"errors"
	"fmt"
	"os"
)

// syncDir fsync 目录项。
//
// 私钥不可重建，所以要的不是"写过了"而是"目录里确实有这个名字了"：
// 文件内容的 fsync 只保证数据块落盘，崩溃后目录项仍可能缺失。
func syncDir(dir string) (err error) {
	f, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open dir %s for sync: %w", dir, err)
	}
	defer func() {
		if closeErr := f.Close(); closeErr != nil {
			err = errors.Join(err, fmt.Errorf("close synced dir %s: %w", dir, closeErr))
		}
	}()

	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync dir %s: %w", dir, err)
	}
	return nil
}
