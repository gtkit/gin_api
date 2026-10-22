//go:build !windows

package diagnostics

import "os"

// keyPermissionTooOpen protects private-key confidentiality and public-key integrity.
func keyPermissionTooOpen(info os.FileInfo, private bool) bool {
	if private {
		return info.Mode().Perm()&0o077 != 0
	}
	return info.Mode().Perm()&0o022 != 0
}
