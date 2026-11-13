package osupdate

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/mistgate/mistgate/internal/node/awgprep"
)

// Host is the part of the machine a job looks at besides the commands it runs. The real one reads the live system; a test
// builds a fake, so no test touches the host it runs on.
type Host struct {
	ReadFile func(path string) ([]byte, error)
	// WriteFile replaces a file atomically (the module source's compat.h is repaired through it).
	WriteFile func(path, body string) error
	// Size is the size of a file, ok = false when there is none.
	Size func(path string) (int64, bool)
	// Glob lists the paths that match a pattern.
	Glob func(pattern string) []string
	// FreeMB is the free space of the filesystem holding path; ok = false when it cannot be read.
	FreeMB func(path string) (int64, bool)
	// AptBusy says whether dpkg, apt or unattended-upgrades hold a lock.
	AptBusy func() bool
	// AptLockInfo returns the validated process name holding an apt lock, when readable.
	AptLockInfo func() (string, int64)
	// HoldAptLock takes the dpkg frontend lock and keeps it until release is called (or the process ends), so that no other
	// package manager starts between the last check before a reboot and the reboot. An error means somebody holds it.
	HoldAptLock func() (release func(), err error)
	// BuilderLock is the same non-blocking AmneziaWG lock as prepare-kernel and kmod-sync.
	BuilderLock func() (release func(), acquired bool, err error)
	// Sleep waits d or until ctx ends.
	Sleep func(ctx context.Context, d time.Duration)
	Now   func() time.Time
}

// RealHost is the live machine. The lock probe needs root (it opens the lock files for writing).
func RealHost() Host {
	return Host{
		ReadFile:  os.ReadFile,
		WriteFile: awgprep.RealSys().WriteFile,
		Size: func(p string) (int64, bool) {
			info, err := os.Stat(p)
			if err != nil {
				return 0, false
			}
			return info.Size(), true
		},
		Glob:   func(pattern string) []string { m, _ := filepath.Glob(pattern); return m },
		FreeMB: freeMB,
		AptBusy: func() bool {
			return awgprep.RealSys().AptBusy() || awgprep.FileLocked("/var/run/unattended-upgrades.lock")
		},
		AptLockInfo: func() (string, int64) {
			_, holder, since := (&Reader{}).aptLockStatus()
			return holder, since
		},
		HoldAptLock: holdAptLock,
		BuilderLock: awgprep.TryBuilderLock,
		Sleep:       sleepCtx,
		Now:         time.Now,
	}
}

func sleepCtx(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}

func (h Host) defaults() Host {
	real := RealHost()
	if h.ReadFile == nil {
		h.ReadFile = real.ReadFile
	}
	if h.WriteFile == nil {
		h.WriteFile = real.WriteFile
	}
	if h.Size == nil {
		h.Size = real.Size
	}
	if h.Glob == nil {
		h.Glob = real.Glob
	}
	if h.FreeMB == nil {
		h.FreeMB = real.FreeMB
	}
	if h.AptBusy == nil {
		h.AptBusy = real.AptBusy
	}
	if h.AptLockInfo == nil {
		h.AptLockInfo = real.AptLockInfo
	}
	if h.HoldAptLock == nil {
		h.HoldAptLock = real.HoldAptLock
	}
	if h.BuilderLock == nil {
		h.BuilderLock = real.BuilderLock
	}
	if h.Sleep == nil {
		h.Sleep = real.Sleep
	}
	if h.Now == nil {
		h.Now = real.Now
	}
	return h
}

func (h Host) text(path string) string {
	b, err := h.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(b))
}

func (h Host) exists(path string) bool {
	_, ok := h.Size(path)
	return ok
}

// BootID is the kernel's id of this boot: it changes when, and only when, the host restarts.
func (h Host) BootID() string { return h.text("/proc/sys/kernel/random/boot_id") }

// KernelRunning is the release of the running kernel.
func (h Host) KernelRunning() string { return h.text("/proc/sys/kernel/osrelease") }

// ModuleLoaded: the AmneziaWG kernel module is loaded now.
func (h Host) ModuleLoaded() bool { return len(h.Glob("/sys/module/amneziawg")) > 0 }

// Uptime is how long the host has been up; ok = false when it cannot be read.
func (h Host) Uptime() (time.Duration, bool) {
	f := strings.Fields(h.text("/proc/uptime"))
	if len(f) == 0 {
		return 0, false
	}
	secs, err := strconv.ParseFloat(f[0], 64)
	if err != nil || secs < 0 {
		return 0, false
	}
	return time.Duration(secs * float64(time.Second)), true
}
