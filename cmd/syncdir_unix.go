//go:build !windows

package cmd

import (
	"fmt"
	"os"
)

// syncDir fsync 目录项。
//
// 私钥不可重建，所以要的不是"写过了"而是"目录里确实有这个名字了"：
// 文件内容的 fsync 只保证数据块落盘，崩溃后目录项仍可能缺失。
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
