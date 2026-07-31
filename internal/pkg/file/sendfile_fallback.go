//go:build !linux

package file

import "os"

func trySendfile(_ *os.File, _ *os.File, _ int64) error {
	return errZeroCopyUnavailable
}
