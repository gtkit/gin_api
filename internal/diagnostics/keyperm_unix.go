//go:build !windows

package diagnostics

import "os"

// keyPermissionTooOpen 报告密钥文件的权限是否对 group 或 other 开放。
//
// 私钥对 group/other 可读即为过宽。公钥不适用，但一并收紧无害——
// 调用方对活跃私钥、活跃公钥与全部退役公钥用的是同一条判据。
func keyPermissionTooOpen(info os.FileInfo) bool {
	return info.Mode().Perm()&0o077 != 0
}
