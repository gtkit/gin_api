//go:build linux

package file

import (
	"os"

	"golang.org/x/sys/unix"
)

func trySendfile(dst, src *os.File, size int64) error {
	_, err := unix.Sendfile(int(dst.Fd()), int(src.Fd()), nil, int(size))
	return err
}
