//go:build linux

package awgprep

import (
	"os"
	"syscall"
)

// aptBusy says whether another process holds one of the locks a package install needs. apt and dpkg take POSIX record
// locks (fcntl), not flock, so that is what is asked: F_GETLK for a write lock on the whole file reports a holder
// without taking anything. The job runs as root and only looks.
func aptBusy() bool {
	for _, p := range []string{"/var/lib/dpkg/lock-frontend", "/var/lib/dpkg/lock", "/var/lib/apt/lists/lock"} {
		f, err := os.OpenFile(p, os.O_RDWR, 0)
		if err != nil {
			continue // not there (not a dpkg host) or unreadable: apt itself will say
		}
		lk := syscall.Flock_t{Type: syscall.F_WRLCK}
		err = syscall.FcntlFlock(f.Fd(), syscall.F_GETLK, &lk)
		f.Close()
		if err == nil && lk.Type != syscall.F_UNLCK {
			return true
		}
	}
	return false
}
