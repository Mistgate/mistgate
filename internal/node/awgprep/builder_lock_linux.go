//go:build linux

package awgprep

import (
	"errors"
	"os"
	"syscall"
)

const BuilderLockPath = "/run/mistgate-awg-kmod.lock"

// TryBuilderLock takes the one non-blocking lock shared by every AmneziaWG module builder.
func TryBuilderLock() (func(), bool, error) {
	f, err := os.OpenFile(BuilderLockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, false, err
	}
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		_ = f.Close()
		return nil, false, nil
	}
	if err != nil {
		_ = f.Close()
		return nil, false, err
	}
	return func() {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		_ = f.Close()
	}, true, nil
}
