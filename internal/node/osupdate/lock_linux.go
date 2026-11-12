//go:build linux

package osupdate

import (
	"os"

	"golang.org/x/sys/unix"
)

// dpkgFrontendLock is the first lock apt and dpkg ask for.
const dpkgFrontendLock = "/var/lib/dpkg/lock-frontend"

func holdAptLock() (func(), error) { return holdLock(dpkgFrontendLock) }

// holdLock takes a write lock on the file and keeps it until release is called (or the process ends). It is an open file
// description lock (F_OFD_SETLK), not a POSIX record lock: it conflicts with the POSIX locks apt and dpkg take, and it belongs
// to the open file, so that another part of this process opening and closing the same file does not drop it (a POSIX lock is
// dropped by any close of any descriptor of the file in the process).
func holdLock(path string) (func(), error) {
	f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0o640)
	if err != nil {
		return nil, err
	}
	lk := unix.Flock_t{Type: unix.F_WRLCK, Whence: 0, Start: 0, Len: 0} // Pid must be 0 for an OFD lock
	if err := unix.FcntlFlock(f.Fd(), unix.F_OFD_SETLK, &lk); err != nil {
		_ = f.Close()
		return nil, err
	}
	return func() { _ = f.Close() }, nil
}
