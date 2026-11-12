//go:build linux

package osupdate

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

// tryOFD asks for a write lock on the file through a descriptor of its own: an open file description lock of another description
// conflicts with a held one even inside the same process.
func tryOFD(t *testing.T, path string) error {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	lk := unix.Flock_t{Type: unix.F_WRLCK}
	return unix.FcntlFlock(f.Fd(), unix.F_OFD_SETLK, &lk)
}

func TestHeldLockSurvivesAnotherOpenAndCloseOfTheSameFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock-frontend")
	release, err := holdLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := tryOFD(t, path); !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EACCES) {
		t.Fatalf("the lock is not held: second lock = %v", err)
	}
	// Another part of the process (a library, an exec helper) opens and closes the same file. A POSIX record lock would be gone
	// now: closing any descriptor of the file drops all the process's locks on it. An OFD lock belongs to the open description.
	for range 3 {
		other, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		other.Close()
	}
	if err := tryOFD(t, path); !errors.Is(err, unix.EAGAIN) && !errors.Is(err, unix.EACCES) {
		t.Fatalf("the lock was dropped by an open and close of the same file: second lock = %v", err)
	}
	release()
	if err := tryOFD(t, path); err != nil {
		t.Fatalf("the lock is still held after the release: %v", err)
	}
}

func TestHeldLockIsVisibleToAProbe(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock-frontend")
	release, err := holdLock(path)
	if err != nil {
		t.Fatal(err)
	}
	defer release()
	// apt takes POSIX record locks, which an OFD lock conflicts with (the kernel treats the two kinds as one family). The probe below asks
	// through a description of its own whether somebody holds the file.
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	probe := unix.Flock_t{Type: unix.F_WRLCK}
	if err := unix.FcntlFlock(f.Fd(), unix.F_OFD_GETLK, &probe); err != nil {
		t.Fatal(err)
	}
	if probe.Type == unix.F_UNLCK {
		t.Fatal("the held lock is not visible to a probe")
	}
}

func TestHoldLockRefusesWhenSomebodyHoldsIt(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock-frontend")
	first, err := holdLock(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := holdLock(path); err == nil {
		t.Fatal("a second lock on the same file was granted")
	}
	first()
	second, err := holdLock(path)
	if err != nil {
		t.Fatalf("after the release: %v", err)
	}
	second()
}
