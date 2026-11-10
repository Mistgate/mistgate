package osupdate

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"google.golang.org/protobuf/proto"
)

// Capability advertises the read-only OS snapshot stream of step S1. It is deliberately not "os-update/1": that name is
// reserved for the write steps, so an agent of this build is never taken for one that can change the operating system.
const Capability = "os-status/1"

// Monitor runs the expensive read only when apt/dpkg/boot inputs change, and at least every six hours.
// Fingerprinting itself is filesystem metadata only.
type Monitor struct {
	reader *Reader
	// readMu lets one Poll at a time run a read. mu only guards the stored result and is never held across a read, so
	// Snapshot answers at once while the apt simulation runs (it takes up to ~45 s).
	readMu sync.Mutex
	mu     sync.Mutex
	lastFP string
	lastAt time.Time
	status *agentv1.OsStatus
}

func NewMonitor(reader *Reader) *Monitor {
	if reader == nil {
		reader = NewReader()
	}
	reader.defaults()
	return &Monitor{reader: reader}
}

// Snapshot returns the latest completed result; it never starts a read.
func (m *Monitor) Snapshot() *agentv1.OsStatus {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.status == nil {
		return nil
	}
	return proto.Clone(m.status).(*agentv1.OsStatus)
}

// Poll checks filesystem metadata and reads only when forced, changed, or six hours have passed.
// It returns the newest result, whether the public snapshot changed, and whether a full read ran.
func (m *Monitor) Poll(ctx context.Context, force bool) (*agentv1.OsStatus, bool, bool, error) {
	m.readMu.Lock()
	defer m.readMu.Unlock()
	fp := m.fingerprint()
	now := m.reader.Now()
	m.mu.Lock()
	previous := m.status
	if !force && previous != nil && fp == m.lastFP && now.Sub(m.lastAt) < FullReadInterval {
		unchanged := proto.Clone(previous).(*agentv1.OsStatus)
		m.mu.Unlock()
		return unchanged, false, false, nil
	}
	m.mu.Unlock()
	status, err := m.reader.Read(ctx)
	m.mu.Lock()
	m.lastFP, m.lastAt, m.status = fp, now, status
	m.mu.Unlock()
	changed := previous == nil || !proto.Equal(previous, status)
	return proto.Clone(status).(*agentv1.OsStatus), changed, true, err
}

// Run keeps the panel-host snapshot current. It does not call out to a node or refresh package lists.
func (m *Monitor) Run(ctx context.Context, onChange func(*agentv1.OsStatus)) {
	if status, changed, _, err := m.Poll(ctx, false); err == nil && changed && onChange != nil {
		onChange(status)
	}
	ticker := time.NewTicker(FingerprintInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			status, changed, _, err := m.Poll(ctx, false)
			if err == nil && changed && onChange != nil {
				onChange(status)
			}
		}
	}
}

func (m *Monitor) fingerprint() string {
	var parts []string
	// /proc/sys/kernel/osrelease is left out on purpose: procfs reports the current time as its mtime, so it would
	// change on every poll and force a full read each time. A reboot is picked up by the six-hour read at the latest.
	paths := []string{"/var/lib/dpkg/status", "/run/reboot-required", "/run/reboot-required.pkgs", listsStampPath}
	for _, p := range paths {
		if info, err := m.reader.stat(p); err == nil {
			parts = append(parts, fmt.Sprintf("%s:%d:%d", p, info.ModTime().UnixNano(), info.Size()))
		} else {
			parts = append(parts, p+":-")
		}
	}
	for _, dir := range []string{"/var/lib/apt/lists", "/boot"} {
		entries, err := m.reader.readDir(dir)
		if err != nil {
			parts = append(parts, dir+":-")
			continue
		}
		var rows []string
		for _, entry := range entries {
			if dir == "/var/lib/apt/lists" && !strings.HasSuffix(entry.Name(), "InRelease") {
				continue
			}
			if info, err := entry.Info(); err == nil {
				rows = append(rows, fmt.Sprintf("%s:%d:%d", entry.Name(), info.ModTime().UnixNano(), info.Size()))
			}
		}
		sort.Strings(rows)
		parts = append(parts, dir+":"+strings.Join(rows, ","))
	}
	return strings.Join(parts, "\n")
}

// CapabilityAvailable is deliberately strict: S1 reads are offered only from the supported root systemd agent unit.
func CapabilityAvailable() bool {
	if !CanAdvertiseCapability(runtime.GOOS, effectiveUID(), fileExists("/run/systemd/system"), commandAvailable("systemd-run")) {
		return false
	}
	return true
}

// IsRoot reports whether the current process has the privileges required for panel-host reads in v1.
func IsRoot() bool { return effectiveUID() == 0 }

// CanAdvertiseCapability is the pure part of capability detection, exposed for platform-matrix tests.
func CanAdvertiseCapability(goos string, uid int, underSystemd, hasSystemdRun bool) bool {
	return goos == "linux" && uid == 0 && underSystemd && hasSystemdRun
}

var (
	effectiveUID     = currentUID
	fileExists       = func(path string) bool { _, err := os.Stat(path); return err == nil }
	commandAvailable = func(name string) bool { _, err := exec.LookPath(name); return err == nil }
)
