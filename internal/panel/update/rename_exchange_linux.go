//go:build linux

package update

import (
	"errors"

	"golang.org/x/sys/unix"
)

func renameExchange(oldPath, newPath string) error {
	return unix.Renameat2(unix.AT_FDCWD, oldPath, unix.AT_FDCWD, newPath, unix.RENAME_EXCHANGE)
}

func isRenameExchangeUnsupported(err error) bool {
	return errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EXDEV)
}
