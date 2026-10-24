//go:build windows

package diagnostics

import "os"

// keyPermissionTooOpen 在 Windows 上恒为 false：该平台没有可供判定的权限位。
//
// NTFS 的访问控制是 ACL，不映射到 os.FileMode 的 Unix 权限位。Go 在 Windows 上
// 对普通文件恒报 0666（只按只读属性区分 0444/0666），于是 &0o077 的判据**恒为真**——
// 它报告的不是"密钥权限过宽"，而是"这里是 Windows"。
//
// 恒假比误报更安全，但只限于**权限**这一项：调用方保留的存在性判定在本平台完全有效，
// 一并跳过会让 doctor 在密钥缺失时报 pass，重新引入 checkJWTKeys 注释里记下的
// 那种虚假肯定——那比不检查更糟。
//
// 若将来需要在 Windows 上真正判定密钥保护，得走 golang.org/x/sys/windows 读 DACL，
// 那是一套独立的语义，不应伪装成 Unix 权限位。
func keyPermissionTooOpen(os.FileInfo, bool) bool {
	return false
}
