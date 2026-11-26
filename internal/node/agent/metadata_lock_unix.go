//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package agent

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func lockMetaFile(f *os.File) error {
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX)
		if !errors.Is(err, unix.EINTR) {
			return err
		}
	}
}

func unlockMetaFile(f *os.File) {
	_ = unix.Flock(int(f.Fd()), unix.LOCK_UN)
}
