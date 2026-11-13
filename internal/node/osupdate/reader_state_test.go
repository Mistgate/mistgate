package osupdate

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
)

// apt prints " [pkg:arch ]" (or just " []") after the closing parenthesis of an Inst line when the simulation has
// broken packages (pkgSimulate::ShortBreaks). Those lines are still pending upgrades.
func TestParseSimulationKeepsInstLinesWithBrokenPackageTail(t *testing.T) {
	out := strings.Join([]string{
		"NOTE: This is only a simulation!",
		"Inst libsystemd0 [249.11-0ubuntu3.12] (249.11-0ubuntu3.16 Ubuntu:22.04/jammy-updates, Ubuntu:22.04/jammy-security [amd64]) [systemd:amd64 ]",
		"Inst systemd [249.11-0ubuntu3.12] (249.11-0ubuntu3.16 Ubuntu:22.04/jammy-updates, Ubuntu:22.04/jammy-security [amd64]) []",
		"Inst libssl3 [3.0.2-0ubuntu1.18] (3.0.2-0ubuntu1.19 Ubuntu:22.04/jammy-security [amd64])",
	}, "\n")
	pending, security, notable := parseSimulation(out)
	if pending != 3 || security != 3 || len(notable) != 3 {
		t.Fatalf("pending=%d security=%d notable=%q, want 3/3/3", pending, security, notable)
	}
	if want := "systemd 249.11-0ubuntu3.12 249.11-0ubuntu3.16 s"; !slices.Contains(notable, want) {
		t.Fatalf("notable = %q, want it to hold %q", notable, want)
	}
	// Anything else after the parenthesis is not apt's broken-package marker, and the line is not trusted.
	if p, _, _ := parseSimulation("Inst systemd [1] (2 Ubuntu:22.04/jammy-security [amd64]) trailing words"); p != 0 {
		t.Fatalf("a line with unknown text after the parenthesis counted as pending (%d)", p)
	}
}

func TestSecurityPendingNamesAreValidatedAndCapped(t *testing.T) {
	var lines []string
	for i := 0; i < 33; i++ {
		lines = append(lines, fmt.Sprintf("Inst libfoo1:arch%02d [1] (2 Ubuntu:24.04/noble-security [amd64])", i))
	}
	lines = append(lines,
		"Inst libbar-common;evil [1] (2 Ubuntu:24.04/noble-security [amd64])",
		"Inst libbar-common [1] (2 Ubuntu:24.04/noble-updates [amd64])")
	r := securityTestReader(t, func() time.Time { return time.Unix(1_800_000_000, 0) }, strings.Join(lines, "\n"))
	status, err := r.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(status.GetSecurityPending()) != maxSecurityPending || status.GetSecurityPending()[0] != "libfoo1:arch00" || status.GetSecurityPending()[31] != "libfoo1:arch31" {
		t.Fatalf("security_pending = %q, want the first 32 validated names", status.GetSecurityPending())
	}
	if status.GetSecurity() != 33 || slices.Contains(status.GetSecurityPending(), "libbar-common") || slices.Contains(status.GetSecurityPending(), "libbar-common;evil") {
		t.Fatalf("security count or names included a non-security or invalid name: %+v", status)
	}
}

func TestSecurityStuckAppearsAfterTwoDaysOfAutomaticUpdates(t *testing.T) {
	current := time.Unix(1_800_000_000, 0)
	r := securityTestReader(t, func() time.Time { return current }, "Inst libfoo1 [1] (2 Ubuntu:24.04/noble-security [amd64])")
	read := func(want bool) {
		t.Helper()
		status, err := r.Read(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		got := slices.Contains(status.GetSecurityStuck(), "libfoo1")
		if got != want || status.GetAutoUpdates() != "on" {
			t.Fatalf("security_stuck=%q auto_updates=%q, want stuck=%v and automatic updates on", status.GetSecurityStuck(), status.GetAutoUpdates(), want)
		}
	}
	read(false)
	current = current.Add(48 * time.Hour)
	read(false)
	current = current.Add(time.Second)
	read(true)
}

func TestSecurityPendingStatePrunesAndToleratesCorruptFile(t *testing.T) {
	stateDir := t.TempDir()
	r := &Reader{StateDir: stateDir}
	first := time.Unix(1_800_000_000, 0).Unix()
	r.rememberSecurityPending([]string{"libfoo1", "libbar-common"}, first)
	current := r.rememberSecurityPending([]string{"libfoo1"}, first+10)
	if len(current) != 1 || current["libfoo1"] != first {
		t.Fatalf("first-seen after pruning = %v", current)
	}
	path := filepath.Join(stateDir, "os-pending.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var stored osPendingState
	if err := json.Unmarshal(data, &stored); err != nil || len(stored.FirstSeen) != 1 || stored.FirstSeen["libfoo1"] != first {
		t.Fatalf("pruned state file = %s, err=%v", data, err)
	}
	if err := os.WriteFile(path, []byte("{corrupt"), 0o600); err != nil {
		t.Fatal(err)
	}
	current = r.rememberSecurityPending([]string{"libbar-common"}, first+20)
	if len(current) != 1 || current["libbar-common"] != first+20 {
		t.Fatalf("first-seen after corrupt file = %v", current)
	}
	data, err = os.ReadFile(path)
	if err != nil || !json.Valid(data) {
		t.Fatalf("corrupt state was not replaced atomically with valid JSON: %s, err=%v", data, err)
	}
}

func securityTestReader(t *testing.T, now func() time.Time, simulation string) *Reader {
	t.Helper()
	root, stateDir := t.TempDir(), t.TempDir()
	writeRoot(t, root, "/etc/os-release", "ID=ubuntu\nVERSION_ID=24.04\n")
	writeRoot(t, root, "/proc/locks", "")
	writeRoot(t, root, "/var/lib/dpkg/status", "fixture")
	writeRoot(t, root, "/var/lib/apt/lists/example_InRelease", "fixture")
	return &Reader{Root: root, StateDir: stateDir, Now: now, Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "apt-get":
			return []byte(simulation), nil
		case "dpkg-query":
			if slices.Contains(args, "unattended-upgrades") {
				return []byte("install ok installed"), nil
			}
			return []byte("ii \tlibfoo1\t1\n"), nil
		case "apt-config":
			return []byte("L='1'; U='1'; R='false';\n"), nil
		case "systemctl":
			if args[0] == "is-enabled" {
				return []byte("enabled\nenabled\n"), nil
			}
			return []byte("active\nactive\n"), nil
		case "linux-version":
			return nil, fmt.Errorf("no kernel fixture")
		default:
			return nil, nil
		}
	}}
}

type fakeInfo struct{ sys any }

func (f fakeInfo) Name() string       { return "lock" }
func (f fakeInfo) Size() int64        { return 0 }
func (f fakeInfo) Mode() os.FileMode  { return 0 }
func (f fakeInfo) ModTime() time.Time { return time.Time{} }
func (f fakeInfo) IsDir() bool        { return false }
func (f fakeInfo) Sys() any           { return f.sys }

type linuxStat struct {
	Dev uint64
	Ino uint64
}

// /proc/locks prints the device as "%02x:%02x:inode" (lock_get_status in fs/locks.c), so the key built from a stat has to
// zero-pad the minor and major numbers the same way.
func TestDeviceInodeKeyMatchesProcLocksFormat(t *testing.T) {
	for _, tc := range []struct {
		major, minor uint64
		procLocks    string
	}{
		{8, 1, "1: POSIX  ADVISORY  WRITE 1131 08:01:1048698 0 EOF"},
		{253, 1, "1: POSIX  ADVISORY  WRITE 1131 fd:01:1048698 0 EOF"},
		{259, 2, "1: POSIX  ADVISORY  WRITE 1131 103:02:1048698 0 EOF"},
		{0, 0x1a, "1: POSIX  ADVISORY  WRITE 1131 00:1a:1048698 0 EOF"},
		{0xfd, 0x10, "1: POSIX  ADVISORY  WRITE 1131 fd:10:1048698 0 EOF"},
	} {
		dev := (tc.major << 8) | tc.minor // glibc makedev for these small numbers
		key, ok := deviceInode(fakeInfo{&linuxStat{Dev: dev, Ino: 1048698}})
		if !ok || !slices.Contains(strings.Fields(tc.procLocks), key) {
			t.Errorf("lock on %x:%x not found: key %q (ok=%v) vs %q", tc.major, tc.minor, key, ok, tc.procLocks)
		}
	}
}

// Snapshot is called when a session starts and when the panel asks, while a read can take up to ~45 s. It must answer
// from the stored result at once.
func TestSnapshotDoesNotWaitForARunningRead(t *testing.T) {
	root := t.TempDir()
	writeRoot(t, root, "/etc/os-release", "ID=ubuntu\nVERSION_ID=24.04\n")
	writeRoot(t, root, "/proc/locks", "")
	writeRoot(t, root, "/var/lib/dpkg/status", "x")
	writeRoot(t, root, "/var/lib/apt/lists/x_InRelease", "x")
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	var block atomic.Bool
	r := &Reader{Root: root, Now: time.Now, Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "apt-get" && block.Load() {
			started <- struct{}{}
			<-release
		}
		return nil, nil
	}}
	m := NewMonitor(r)
	if _, _, _, err := m.Poll(context.Background(), true); err != nil { // the first snapshot exists
		t.Fatal(err)
	}
	block.Store(true)
	done := make(chan struct{})
	go func() { _, _, _, _ = m.Poll(context.Background(), true); close(done) }()
	<-started
	got := make(chan *agentv1.OsStatus, 1)
	go func() { got <- m.Snapshot() }()
	select {
	case s := <-got:
		if s == nil || s.GetDistroId() != "ubuntu" {
			t.Errorf("snapshot during a read = %+v, want the previous result", s)
		}
	case <-time.After(2 * time.Second):
		t.Error("Snapshot blocked while Poll was inside Read")
	}
	close(release)
	<-done
	if s := m.Snapshot(); s == nil || s.GetMeasuredUnix() == 0 {
		t.Fatalf("snapshot after the read = %+v", s)
	}
}

// A second Poll waits for the first one instead of starting a second apt simulation next to it.
func TestPollsDoNotOverlapReads(t *testing.T) {
	root := t.TempDir()
	writeRoot(t, root, "/etc/os-release", "ID=debian\nVERSION_ID=12\n")
	writeRoot(t, root, "/proc/locks", "")
	writeRoot(t, root, "/var/lib/dpkg/status", "x")
	writeRoot(t, root, "/var/lib/apt/lists/x_InRelease", "x")
	var running, overlaps atomic.Int32
	r := &Reader{Root: root, Now: time.Now, Run: func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "apt-get" {
			if running.Add(1) > 1 {
				overlaps.Add(1)
			}
			time.Sleep(50 * time.Millisecond)
			running.Add(-1)
		}
		return nil, nil
	}}
	m := NewMonitor(r)
	done := make(chan struct{})
	for range 3 {
		go func() { _, _, _, _ = m.Poll(context.Background(), true); done <- struct{}{} }()
	}
	for range 3 {
		<-done
	}
	if overlaps.Load() != 0 {
		t.Fatalf("%d apt simulations ran side by side", overlaps.Load())
	}
}

func TestAutoUpdatesTreatsUnsetPeriodicEnableAsOn(t *testing.T) {
	run := func(config string) string {
		r := &Reader{Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
			switch name {
			case "dpkg-query":
				return []byte("install ok installed"), nil
			case "apt-config":
				return []byte(config), nil
			case "systemctl":
				if args[0] == "is-enabled" {
					return []byte("enabled\nenabled\n"), nil
				}
				return []byte("active\nactive\n"), nil
			}
			return nil, nil
		}}
		s := &agentv1.OsStatus{}
		r.autoUpdates(context.Background(), s)
		return s.GetAutoUpdates()
	}
	// A stock Ubuntu or Debian host sets no APT::Periodic::Enable at all, and apt-config prints no E assignment.
	if got := run("L='1'; U='1'; R='false';\n"); got != "on" {
		t.Errorf("stock host: auto_updates = %q, want on", got)
	}
	if got := run("E='0'; L='1'; U='1'; R='false';\n"); got != "off" {
		t.Errorf("Enable=0: auto_updates = %q, want off", got)
	}
	if got := run("L='1'; R='false';\n"); got != "off" {
		t.Errorf("no Unattended-Upgrade: auto_updates = %q, want off", got)
	}
}

func TestAwgModuleNextKernel(t *testing.T) {
	const running, newest = "6.8.0-56-generic", "6.8.0-57-generic"
	installed, hold, removed := "ii \tamneziawg-dkms\n", "hi \tamneziawg-dkms\n", "rc \tamneziawg-dkms\n"
	for _, tc := range []struct {
		name  string
		rows  string
		files []string
		dirs  []string
		want  string
	}{
		{"removed package with config left", removed, nil, []string{"/lib/modules/" + newest}, ""},
		{"not installed, nothing loaded", "ii \tbase-files\n", nil, nil, ""},
		{"installed, module in the kernel's tree", installed, []string{"/lib/modules/" + newest + "/updates/dkms/amneziawg.ko.zst"}, nil, "ready"},
		{"installed and held", hold, []string{"/lib/modules/" + newest + "/updates/dkms/amneziawg.ko"}, nil, "ready"},
		{"installed, tree readable, no module", installed, nil, []string{"/lib/modules/" + newest}, "missing"},
		// ProtectKernelModules=yes hides /usr/lib/modules from the agent: only the DKMS tree is left to look at.
		{"module only in the DKMS tree", installed, []string{"/var/lib/dkms/amneziawg/1.0/" + newest + "/x86_64/module/amneziawg.ko.zst"}, nil, "ready"},
		{"DKMS tree without this kernel", installed, nil, []string{"/var/lib/dkms/amneziawg/1.0/" + running + "/x86_64/module"}, "missing"},
		{"nothing readable at all", installed, nil, nil, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			for _, f := range tc.files {
				writeRoot(t, root, f, "module")
			}
			for _, d := range tc.dirs {
				if err := os.MkdirAll(filepath.Join(root, filepath.FromSlash(d)), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			s := &agentv1.OsStatus{KernelRunning: running, KernelNewest: newest}
			(&Reader{Root: root}).awgModule(s, tc.rows)
			if s.GetAwgModuleNext() != tc.want {
				t.Fatalf("awg_module_next = %q, want %q", s.GetAwgModuleNext(), tc.want)
			}
		})
	}
}

func TestRebootReasonsListKernelFirstBeforeTheCut(t *testing.T) {
	root := t.TempDir()
	var names []string
	for i := 0; i < 20; i++ {
		names = append(names, fmt.Sprintf("aaa-pkg%02d", i))
	}
	writeRoot(t, root, "/run/reboot-required.pkgs", strings.Join(names, "\n")+"\n")
	r := &Reader{Root: root}
	needed, reasons := r.rebootRequired(&agentv1.OsStatus{KernelRunning: "6.8.0-56-generic", KernelNewest: "6.8.0-57-generic"})
	if !needed || len(reasons) != maxPackages || reasons[0] != "kernel" || reasons[1] != "aaa-pkg00" {
		t.Fatalf("reasons = %q (reboot %v), want kernel first and %d entries", reasons, needed, maxPackages)
	}
	_, reasons = r.rebootRequired(&agentv1.OsStatus{KernelRunning: "6.8.0-57-generic", KernelNewest: "6.8.0-57-generic"})
	if slices.Contains(reasons, "kernel") || len(reasons) != maxPackages {
		t.Fatalf("reasons without a kernel change = %q", reasons)
	}
}

func TestListsTimePrefersTheUpdateSuccessStamp(t *testing.T) {
	root := t.TempDir()
	r := &Reader{Root: root}
	old, fresh := time.Unix(1790000000, 0), time.Unix(1793000000, 0)
	set := func(p string, at time.Time) {
		t.Helper()
		if err := os.Chtimes(filepath.Join(root, filepath.FromSlash(p)), at, at); err != nil {
			t.Fatal(err)
		}
	}
	writeRoot(t, root, "/var/lib/apt/lists/deb.example_dists_suite_InRelease", "lists")
	set("/var/lib/apt/lists/deb.example_dists_suite_InRelease", old)
	if got, ok := r.listsTime(); !ok || !got.Equal(old) {
		t.Fatalf("without a stamp the newest InRelease file decides: %v %v", got, ok)
	}
	// An "apt-get update" that found nothing new leaves the list files alone but touches the stamp.
	writeRoot(t, root, listsStampPath, "")
	set(listsStampPath, fresh)
	if got, ok := r.listsTime(); !ok || !got.Equal(fresh) {
		t.Fatalf("with a stamp it decides: %v %v", got, ok)
	}
	if err := os.Remove(filepath.Join(root, "var", "lib", "apt", "lists", "deb.example_dists_suite_InRelease")); err != nil {
		t.Fatal(err)
	}
	if _, ok := r.listsTime(); ok {
		t.Fatal("a stamp without any list file was taken for lists")
	}
}

// The fingerprint decides when the expensive read runs. procfs gives /proc/sys/kernel/osrelease a fresh mtime all the
// time, so it must not be part of it; the update stamp must be, or "lists checked" would lag by up to six hours.
func TestFingerprintIgnoresOsreleaseMtimeAndWatchesTheStamp(t *testing.T) {
	root := t.TempDir()
	writeRoot(t, root, "/var/lib/dpkg/status", "x")
	writeRoot(t, root, "/proc/sys/kernel/osrelease", "6.8.0-56-generic\n")
	writeRoot(t, root, listsStampPath, "")
	m := NewMonitor(&Reader{Root: root})
	before := m.fingerprint()
	later := time.Unix(1793000000, 0)
	if err := os.Chtimes(filepath.Join(root, "proc", "sys", "kernel", "osrelease"), later, later); err != nil {
		t.Fatal(err)
	}
	if got := m.fingerprint(); got != before {
		t.Fatal("the osrelease mtime changed the fingerprint")
	}
	if err := os.Chtimes(filepath.Join(root, filepath.FromSlash(listsStampPath)), later, later); err != nil {
		t.Fatal(err)
	}
	if got := m.fingerprint(); got == before {
		t.Fatal("a new update-success stamp did not change the fingerprint")
	}
}
