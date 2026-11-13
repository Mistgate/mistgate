//go:build linux

package osupdate

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

func TestReaderParsesAndValidatesAptLockHolder(t *testing.T) {
	for _, tc := range []struct {
		name, comm, wantHolder string
	}{
		{"valid process", "apt-get", "apt-get"},
		{"invalid process name", "apt;get", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			lock := filepath.Join(root, "var", "lib", "dpkg", "lock")
			if err := os.MkdirAll(filepath.Dir(lock), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(lock, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			info, err := os.Stat(lock)
			if err != nil {
				t.Fatal(err)
			}
			key, ok := deviceInode(info)
			if !ok {
				t.Fatal("could not read lock device and inode")
			}
			const pid = 4312
			writeRoot(t, root, "/proc/locks", fmt.Sprintf("1: POSIX ADVISORY WRITE %d %s 0 EOF\n", pid, key))
			writeRoot(t, root, fmt.Sprintf("/proc/%d/comm", pid), tc.comm+"\n")
			statFields := make([]string, 20) // fields 3 through 22
			for i := range statFields {
				statFields[i] = "0"
			}
			statFields[0], statFields[19] = "S", "450"
			writeRoot(t, root, fmt.Sprintf("/proc/%d/stat", pid), fmt.Sprintf("%d (apt-get) %s\n", pid, strings.Join(statFields, " ")))
			writeRoot(t, root, "/proc/stat", "cpu 1 2 3\nbtime 1800000000\n")
			reader := &Reader{Root: root}
			locked, holder, since := reader.aptLockStatus()
			if !locked || holder != tc.wantHolder {
				t.Fatalf("lock = %v, holder = %q, want locked and holder %q", locked, holder, tc.wantHolder)
			}
			if tc.wantHolder != "" && since != 1800000004 {
				t.Fatalf("lock process start time = %d, want 1800000004", since)
			}
			if tc.wantHolder == "" && since != 0 {
				t.Fatalf("invalid holder yielded a start time: %d", since)
			}
		})
	}
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
