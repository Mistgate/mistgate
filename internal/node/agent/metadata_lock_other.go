//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly && !windows

package agent

import (
	"errors"
	"os"
)

func lockMetaFile(*os.File) error {
	return errors.New("agent metadata locking is unsupported on this platform")
}

func unlockMetaFile(*os.File) {}
