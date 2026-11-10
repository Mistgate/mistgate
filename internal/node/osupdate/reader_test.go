package osupdate

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	agentv1 "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
)

func TestReadDistroFixtures(t *testing.T) {
	securityUpdates := map[string]uint32{"ubuntu-22.04": 2, "ubuntu-24.04": 1, "ubuntu-26.04": 1, "debian-12": 2, "debian-13": 1}
	// The jammy fixture also carries a kernel meta-package whose version is a dotted quad, as Ubuntu 22.04 ships them.
	pendingUpdates := map[string]uint32{"ubuntu-22.04": 4, "ubuntu-24.04": 3, "ubuntu-26.04": 3, "debian-12": 3, "debian-13": 3}
	for _, distro := range []string{"ubuntu-22.04", "ubuntu-24.04", "ubuntu-26.04", "debian-12", "debian-13"} {
		t.Run(distro, func(t *testing.T) {
			fixture := readFixture(t, distro)
			root := t.TempDir()
			writeRoot(t, root, "/etc/os-release", fixture["os-release"])
			writeRoot(t, root, "/proc/sys/kernel/osrelease", fixture["uname"])
			writeRoot(t, root, "/proc/locks", "")
			writeRoot(t, root, "/var/lib/dpkg/status", fixture["dpkg-status"])
			writeRoot(t, root, "/var/lib/apt/lists/deb.example_dists_suite_InRelease", "fixture lists")
			writeRoot(t, root, "/run/reboot-required", fixture["reboot-required"])
			writeRoot(t, root, "/run/reboot-required.pkgs", fixture["reboot-required.pkgs"])
			for _, entry := range strings.Fields(fixture["boot-listing"]) {
				writeRoot(t, root, filepath.Join("/boot", entry), "fixture kernel file")
			}

			var calls [][]string
			r := &Reader{Root: root, Now: func() time.Time { return time.Unix(1794000000, 0) }}
			r.Run = func(_ context.Context, name string, args ...string) ([]byte, error) {
				calls = append(calls, append([]string{name}, args...))
				switch name {
				case "apt-get":
					return []byte(fixture["apt-simulation"]), nil
				case "dpkg-query":
					if len(args) > 2 && args[2] == "unattended-upgrades" {
						return []byte("install ok installed\n"), nil
					}
					return []byte(fixture["dpkg-status"]), nil
				case "linux-version":
					if len(args) > 0 && args[0] == "list" {
						return []byte(fixture["linux-version-list"]), nil
					}
					if len(args) > 1 && args[0] == "sort" && args[1] == "--reverse" {
						return []byte(fixture["linux-version-sort"]), nil
					}
				case "apt-config":
					return []byte(fixture["apt-config"]), nil
				case "systemctl":
					if len(args) > 0 && args[0] == "is-enabled" {
						return []byte("enabled\nenabled\n"), nil
					}
					if len(args) > 0 && args[0] == "is-active" {
						return []byte("active\nactive\n"), nil
					}
				}
				return nil, nil
			}

			status, err := r.Read(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			wantDistro, wantVersion := "ubuntu", "24.04"
			if strings.HasPrefix(distro, "debian") {
				wantDistro, wantVersion = "debian", strings.TrimPrefix(distro, "debian-")
			} else {
				wantVersion = strings.TrimPrefix(distro, "ubuntu-")
			}
			if status.GetDistroId() != wantDistro || status.GetDistroVersion() != wantVersion {
				t.Fatalf("distro = %s %s", status.GetDistroId(), status.GetDistroVersion())
			}
			if status.GetPending() != pendingUpdates[distro] || status.GetSecurity() != securityUpdates[distro] || len(status.GetNotable()) != int(pendingUpdates[distro]) {
				t.Fatalf("apt simulation = pending %d, security %d, notable %v", status.GetPending(), status.GetSecurity(), status.GetNotable())
			}
			if status.GetKernelRunning() != strings.TrimSpace(fixture["uname"]) || status.GetKernelNewest() != strings.Fields(fixture["linux-version-sort"])[0] {
				t.Fatalf("kernel status = running %q newest %q", status.GetKernelRunning(), status.GetKernelNewest())
			}
			if !status.GetRebootRequired() || !slices.Contains(status.GetRebootReasons(), "kernel") || !slices.Contains(status.GetRebootReasons(), "libc6") {
				t.Fatalf("reboot status = %v %v", status.GetRebootRequired(), status.GetRebootReasons())
			}
			heldPackage := ""
			for _, row := range strings.Split(fixture["dpkg-status"], "\n") {
				if strings.HasPrefix(row, "hi\t") {
					heldPackage = strings.TrimPrefix(row, "hi\t")
					break
				}
			}
			if !slices.Contains(status.GetHeld(), heldPackage) {
				t.Fatalf("held package list = %v", status.GetHeld())
			}
			if !slices.Contains(status.GetHalfConfigured(), "fixture-config") || status.GetApt() != "half_configured" {
				t.Fatalf("dpkg health = apt %q half-configured %v", status.GetApt(), status.GetHalfConfigured())
			}
			if status.GetListsUnix() == 0 || status.GetDpkgUnix() == 0 || status.GetAutoUpdates() != "on" {
				t.Fatalf("cheap status fields = %+v", status)
			}
			wantAutoReboot := strings.Contains(fixture["apt-config"], "R='true'")
			if status.GetAutoReboot() != wantAutoReboot {
				t.Fatalf("auto reboot = %v, want %v", status.GetAutoReboot(), wantAutoReboot)
			}
			aptCalls := 0
			for _, call := range calls {
				if call[0] == "apt-get" {
					aptCalls++
					if !slices.Equal(call[1:], aptSimulationArgs) {
						t.Errorf("apt-get args = %q, want simulation args %q", call[1:], aptSimulationArgs)
					}
					if slices.Contains(call[1:], "update") {
						t.Error("reader attempted apt-get update")
					}
				}
			}
			if aptCalls != 1 {
				t.Fatalf("apt-get simulation calls = %d, want one", aptCalls)
			}
			if distro == "ubuntu-24.04" {
				linuxCalls := 0
				for _, call := range calls {
					if call[0] == "linux-version" {
						linuxCalls++
					}
				}
				writeRoot(t, root, "/run/systemd/container", "docker")
				containerStatus, err := r.Read(context.Background())
				if err != nil {
					t.Fatal(err)
				}
				afterLinuxCalls := 0
				for _, call := range calls {
					if call[0] == "linux-version" {
						afterLinuxCalls++
					}
				}
				if containerStatus.GetContainer() != "docker" || containerStatus.GetKernelNewest() != "" || slices.Contains(containerStatus.GetRebootReasons(), "kernel") || afterLinuxCalls != linuxCalls {
					t.Fatalf("container kernel state = %+v; linux-version calls %d -> %d", containerStatus, linuxCalls, afterLinuxCalls)
				}
			}
		})
	}
}

func TestFailedSimulationIsNotRetriedOutsideTheSandbox(t *testing.T) {
	root := t.TempDir()
	writeRoot(t, root, "/etc/os-release", "ID=ubuntu\nVERSION_ID=\"24.04\"\n")
	writeRoot(t, root, "/proc/locks", "")
	writeRoot(t, root, "/var/lib/dpkg/status", "x")
	writeRoot(t, root, "/var/lib/apt/lists/x_InRelease", "x")
	var calls []string
	r := &Reader{Root: root, Now: time.Now, Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls = append(calls, name)
		if name == "apt-get" {
			return []byte("\nE: Could not open file /var/lib/apt/x - open (30: Read-only file system)\nE: more\n"), errors.New("exit status 100")
		}
		return nil, nil
	}}
	status, err := r.Read(context.Background())
	if status.GetError() != "read_failed" || status.GetPending() != 0 {
		t.Fatalf("status after a failed simulation = %+v", status)
	}
	if err == nil || !strings.Contains(err.Error(), "Read-only file system") || strings.Contains(err.Error(), "E: more") {
		t.Fatalf("read error = %v, want the first line of apt's reason", err)
	}
	if slices.Contains(calls, "systemd-run") || strings.Count(strings.Join(calls, " "), "apt-get") != 1 {
		t.Fatalf("commands = %q: the simulation must run once, in the agent's own sandbox", calls)
	}
}

func TestSimulationSwitchesOffEveryWriteApt(t *testing.T) {
	for _, want := range []string{"Dir::Cache::pkgcache=", "Dir::Cache::srcpkgcache=", "Dir::Log::Planner=", "Debug::NoLocking=1"} {
		if !slices.Contains(aptSimulationArgs, want) {
			t.Errorf("apt simulation args %q lack -o %s", aptSimulationArgs, want)
		}
	}
	for _, arg := range aptSimulationArgs {
		if arg == "update" || arg == "install" || arg == "dist-upgrade" {
			t.Errorf("apt simulation args hold %q", arg)
		}
	}
}

func TestAutoUpdatesReadsDisabledTimersEvenWhenSystemctlReturnsNonzero(t *testing.T) {
	r := &Reader{Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		switch name {
		case "dpkg-query":
			return []byte("install ok installed\n"), nil
		case "apt-config":
			return []byte("E='1'; L='1'; U='1'; R='false';\n"), nil
		case "systemctl":
			if args[0] == "is-enabled" {
				return []byte("enabled\ndisabled\n"), errors.New("one timer is disabled")
			}
			return []byte("active\ninactive\n"), errors.New("one timer is inactive")
		default:
			return nil, errors.New("unexpected command")
		}
	}}
	s := &agentv1.OsStatus{}
	r.autoUpdates(context.Background(), s)
	if s.GetAutoUpdates() != "off" {
		t.Fatalf("disabled timer state = %q, want off", s.GetAutoUpdates())
	}
	if _, known := timersOff("enabled\n", "active\n"); known {
		t.Fatal("incomplete timer output was treated as known")
	}
}

func TestMonitorReadsOnlyOnChangedInputsOrSixHours(t *testing.T) {
	fixture := readFixture(t, "ubuntu-24.04")
	root := t.TempDir()
	writeRoot(t, root, "/etc/os-release", fixture["os-release"])
	writeRoot(t, root, "/proc/sys/kernel/osrelease", fixture["uname"])
	writeRoot(t, root, "/proc/locks", "")
	writeRoot(t, root, "/var/lib/dpkg/status", fixture["dpkg-status"])
	writeRoot(t, root, "/var/lib/apt/lists/deb.example_dists_suite_InRelease", "fixture lists")
	writeRoot(t, root, "/run/reboot-required", fixture["reboot-required"])
	writeRoot(t, root, "/run/reboot-required.pkgs", fixture["reboot-required.pkgs"])
	for _, entry := range strings.Fields(fixture["boot-listing"]) {
		writeRoot(t, root, filepath.Join("/boot", entry), "fixture kernel file")
	}
	reads := 0
	current := time.Unix(1794000000, 0)
	r := fixtureRunner(root, fixture, func() time.Time { return current }, &reads)
	m := NewMonitor(r)
	if _, _, didRead, err := m.Poll(context.Background(), false); err != nil || !didRead || reads != 1 {
		t.Fatalf("first poll didRead=%v reads=%d err=%v", didRead, reads, err)
	}
	if _, changed, didRead, err := m.Poll(context.Background(), false); err != nil || didRead || changed || reads != 1 {
		t.Fatalf("unchanged poll changed=%v didRead=%v reads=%d err=%v", changed, didRead, reads, err)
	}
	listPath := filepath.Join(root, "var", "lib", "apt", "lists", "deb.example_dists_suite_InRelease")
	if err := os.WriteFile(listPath, []byte("changed fixture lists"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, didRead, err := m.Poll(context.Background(), false); err != nil || !didRead || reads != 2 {
		t.Fatalf("changed-input poll didRead=%v reads=%d err=%v", didRead, reads, err)
	}
	bootPath := filepath.Join(root, "boot", "vmlinuz-6.8.0-57-generic")
	if err := os.WriteFile(bootPath, []byte("changed boot listing"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, didRead, err := m.Poll(context.Background(), false); err != nil || !didRead || reads != 3 {
		t.Fatalf("changed-/boot poll didRead=%v reads=%d err=%v", didRead, reads, err)
	}
	current = current.Add(FullReadInterval)
	if _, _, didRead, err := m.Poll(context.Background(), false); err != nil || !didRead || reads != 4 {
		t.Fatalf("six-hour poll didRead=%v reads=%d err=%v", didRead, reads, err)
	}
}

func TestCapabilityRequiresLinuxRootSystemdAndTransientRunner(t *testing.T) {
	for _, tc := range []struct {
		name                     string
		goos                     string
		uid                      int
		underSystemd, systemdRun bool
		want                     bool
	}{
		{"supported", "linux", 0, true, true, true},
		{"non-linux", "windows", 0, true, true, false},
		{"non-root", "linux", 1000, true, true, false},
		{"no-systemd", "linux", 0, false, true, false},
		{"no-systemd-run", "linux", 0, true, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := CanAdvertiseCapability(tc.goos, tc.uid, tc.underSystemd, tc.systemdRun); got != tc.want {
				t.Fatalf("CanAdvertiseCapability = %v, want %v", got, tc.want)
			}
		})
	}
}

func fixtureRunner(root string, fixture map[string]string, now func() time.Time, reads *int) *Reader {
	r := &Reader{Root: root, Now: now}
	r.Run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		if name == "apt-get" {
			*reads++
			return []byte(fixture["apt-simulation"]), nil
		}
		if name == "dpkg-query" {
			if len(args) > 2 && args[2] == "unattended-upgrades" {
				return []byte("install ok installed\n"), nil
			}
			return []byte(fixture["dpkg-status"]), nil
		}
		if name == "linux-version" {
			if len(args) > 0 && args[0] == "list" {
				return []byte(fixture["linux-version-list"]), nil
			}
			return []byte(fixture["linux-version-sort"]), nil
		}
		if name == "apt-config" {
			return []byte(fixture["apt-config"]), nil
		}
		if name == "systemctl" && len(args) > 0 {
			if args[0] == "is-enabled" {
				return []byte("enabled\nenabled\n"), nil
			}
			if args[0] == "is-active" {
				return []byte("active\nactive\n"), nil
			}
		}
		return nil, nil
	}
	return r
}

func readFixture(t *testing.T, distro string) map[string]string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", distro+".txt"))
	if err != nil {
		t.Fatal(err)
	}
	sections := map[string]string{}
	var key string
	var lines []string
	flush := func() {
		if key != "" {
			sections[key] = strings.Join(lines, "\n")
		}
	}
	for _, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		if strings.HasPrefix(line, "### ") {
			flush()
			key, lines = strings.TrimPrefix(line, "### "), nil
			continue
		}
		lines = append(lines, line)
	}
	flush()
	return sections
}

func writeRoot(t *testing.T, root, p, body string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(strings.TrimPrefix(p, "/")))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}
