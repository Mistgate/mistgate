package osupdate

import (
	"context"
	"errors"
	"io"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeMachine is the host a job runs on in a test: files, sizes, free space, a clock that only moves when the job sleeps,
// and a command table. A command nobody registered fails the test, so no job can run something the test did not expect.
type fakeMachine struct {
	t     *testing.T
	mu    sync.Mutex
	files map[string]string
	sizes map[string]int64
	free  map[string]int64
	clock time.Time
	busy  func() bool

	calls    []string
	handlers map[string]func() (string, error)
	// installed are the kernels linux-version lists; kernelFail makes it fail (kernelSortFail only the sort).
	installed      []string
	kernelFail     bool
	kernelSortFail bool
	// grubOmit lists kernels the generated /boot/grub/grub.cfg does not name (a menu that was never updated).
	grubOmit map[string]bool
	// lockErr makes HoldAptLock fail; locks counts the locks taken and unlocks the ones let go.
	lockErr        error
	locks, unlocks int
	// modinfo says for which kernels modinfo finds the AmneziaWG module.
	modinfo map[string]bool
	// curCtx is the context of the command being run (a handler can look at it).
	curCtx context.Context
	// failWriteFile makes Host.WriteFile fail (the module source cannot be repaired).
	failWriteFile bool
	// onSleep runs at every Sleep of the job (to cancel it or change the machine mid-way).
	onSleep func()
}

func commandKey(args ...string) string { return strings.Join(args, " ") }

const dpkgQuery = "dpkg-query -W -f=${db:Status-Abbrev}\t${Package}\t${Version}\n"

func newMachine(t *testing.T) *fakeMachine {
	m := &fakeMachine{
		t: t, files: map[string]string{}, sizes: map[string]int64{}, free: map[string]int64{"/var": 20000, "/": 20000, "/boot": 800},
		clock: time.Unix(1_800_000_000, 0), handlers: map[string]func() (string, error){},
	}
	m.files["/etc/os-release"] = "ID=ubuntu\nVERSION_ID=\"26.04\"\nPRETTY_NAME=\"Ubuntu 26.04\"\n"
	m.files["/proc/1/comm"] = "systemd\n"
	m.files["/proc/sys/kernel/osrelease"] = "7.0.0-37-generic\n"
	m.files["/proc/sys/kernel/random/boot_id"] = "boot-one\n"
	m.files["/proc/uptime"] = "86400.00 80000.00\n"
	m.sizes["/boot/grub/grub.cfg"] = 4096 // GRUB, with the default entry 0 and nothing saved
	m.sizes["/boot/grub/grubenv"] = 1024
	m.files["/etc/default/grub"] = "GRUB_DEFAULT=0\nGRUB_TIMEOUT=5\n"
	m.ok("grub-editenv list", "")
	m.fail("systemd-detect-virt --container", "none\n")
	m.modinfo = map[string]bool{}
	m.setDpkg("ii \tlibc6\t2.40-1\nii \tunattended-upgrades\t2.9\nii \tlinux-image-7.0.0-37-generic\t7.0.0-37.37\n")
	m.setKernels("7.0.0-37-generic", "7.0.0-37-generic")
	for cmd, out := range map[string]string{
		commandKey("apt-config", "shell", "R", "Unattended-Upgrade::Automatic-Reboot/b"): "R='false'\n",
		commandKey("dpkg-query", "-W", "-f=${Status}", "unattended-upgrades"):            "install ok installed",
		commandKey("apt-mark", "showauto"):                                               "",
		commandKey("apt-get", "-o", aptLockOption, "update"):                             "Hit:1 http://archive.example.net/ubuntu resolute InRelease\nReading package lists...\n",
		commandKey("apt-get", "-o", aptLockOption, "check"):                              "",
		commandKey("unattended-upgrade", "-v"):                                           "Packages that will be upgraded: libc6\n",
		commandKey(append([]string{"apt-get"}, aptSimulationArgs...)...):                 "Inst libc6 [2.40-1] (2.40-2 Ubuntu:26.04/resolute-security [amd64])\n",
		commandKey("systemctl", "reboot"):                                                "",
	} {
		m.ok(cmd, out)
	}
	simulation := commandKey(append([]string{"apt-get"}, aptSimulationArgs...)...)
	m.on(simulation, func() (string, error) {
		if m.ran("unattended-upgrade") {
			return "", nil
		}
		return "Inst libc6 [2.40-1] (2.40-2 Ubuntu:26.04/resolute-security [amd64])\n", nil
	})
	m.ok(allUpgrade, "")
	return m
}

// setDpkg sets what dpkg-query prints.
func (m *fakeMachine) setDpkg(out string) {
	m.handlers[dpkgQuery] = func() (string, error) { return out, nil }
}

// setKernels sets the running kernel and the newest installed one; both are installed (linux-version list).
func (m *fakeMachine) setKernels(running, newest string) {
	m.files["/proc/sys/kernel/osrelease"] = running + "\n"
	m.installed = []string{running}
	if newest != running {
		m.installed = append(m.installed, newest)
	}
}

// kernelParts splits a version into numbers and text, so that 7.0.0-38-generic > 7.0.0-9-generic.
var kernelParts = regexp.MustCompile(`[0-9]+|[^0-9]+`)

func kernelLess(a, b string) bool {
	pa, pb := kernelParts.FindAllString(a, -1), kernelParts.FindAllString(b, -1)
	for i := 0; i < len(pa) && i < len(pb); i++ {
		na, ea := strconv.Atoi(pa[i])
		nb, eb := strconv.Atoi(pb[i])
		switch {
		case ea == nil && eb == nil && na != nb:
			return na < nb
		case (ea != nil || eb != nil) && pa[i] != pb[i]:
			return pa[i] < pb[i]
		}
	}
	return len(pa) < len(pb)
}

// linuxVersion answers the two linux-version calls of the job from m.installed.
func (m *fakeMachine) linuxVersion(args []string) (string, bool, error) {
	if len(args) == 0 || args[0] != "linux-version" || len(args) < 2 {
		return "", false, nil
	}
	if m.kernelFail {
		return "", true, errors.New("exit status 1")
	}
	switch args[1] {
	case "list":
		return strings.Join(m.installed, "\n") + "\n", true, nil
	case "sort":
		if m.kernelSortFail {
			return "", true, errors.New("exit status 1")
		}
		vs := slices.DeleteFunc(slices.Clone(args[2:]), func(v string) bool { return v == "--reverse" })
		sort.Slice(vs, func(i, j int) bool { return kernelLess(vs[j], vs[i]) })
		return strings.Join(vs, "\n") + "\n", true, nil
	}
	return "", false, nil
}

func (m *fakeMachine) ok(cmd, out string) {
	m.handlers[cmd] = func() (string, error) { return out, nil }
}

func (m *fakeMachine) fail(cmd, out string) {
	m.handlers[cmd] = func() (string, error) { return out, errors.New("exit status 100") }
}

func (m *fakeMachine) on(cmd string, f func() (string, error)) { m.handlers[cmd] = f }

// bootFiles makes the kernel's image and initramfs exist at full size.
func (m *fakeMachine) bootFiles(kernel string) {
	m.sizes["/boot/vmlinuz-"+kernel] = 15 << 20
	m.sizes["/boot/initrd.img-"+kernel] = 70 << 20
}

func (m *fakeMachine) run(ctx context.Context, w io.Writer, _ string, cmd ...string) (string, error) {
	m.mu.Lock()
	key := commandKey(cmd...)
	m.calls = append(m.calls, key)
	m.curCtx = ctx
	h := m.handlers[key]
	if out, ok, err := m.linuxVersion(cmd); ok {
		m.mu.Unlock()
		return out, err
	}
	if len(cmd) == 6 && cmd[0] == "modinfo" && cmd[1] == "-k" && cmd[3] == "-F" && cmd[5] == "amneziawg" {
		built := m.modinfo[cmd[2]]
		m.mu.Unlock()
		if built {
			return "/lib/modules/" + cmd[2] + "/updates/dkms/amneziawg.ko.zst\n", nil
		}
		return "modinfo: ERROR: Module amneziawg not found.\n", errors.New("exit status 1")
	}
	m.mu.Unlock()
	if h == nil {
		m.t.Errorf("unexpected command: %s", key)
		return "", errors.New("unexpected command")
	}
	out, err := h()
	if out != "" {
		_, _ = io.WriteString(w, out)
	}
	return out, err
}

// grubCfg is the menu of GRUB: one entry for the running kernel and each installed one, unless the test left it out.
func (m *fakeMachine) grubCfg() string {
	var b strings.Builder
	for _, k := range append(slices.Clone(m.installed), strings.TrimSpace(m.files["/proc/sys/kernel/osrelease"])) {
		if !m.grubOmit[k] {
			b.WriteString("menuentry 'Ubuntu, with Linux " + k + "' {\n\tlinux\t/boot/vmlinuz-" + k + " root=/dev/sda1 ro quiet\n\tinitrd\t/boot/initrd.img-" + k + "\n}\n")
		}
	}
	return b.String()
}

func (m *fakeMachine) host() Host {
	return Host{
		ReadFile: func(p string) ([]byte, error) {
			if s, ok := m.files[p]; ok {
				return []byte(s), nil
			}
			if _, ok := m.sizes[p]; ok && p == "/boot/grub/grub.cfg" {
				return []byte(m.grubCfg()), nil
			}
			return nil, errors.New("not found")
		},
		WriteFile: func(p, body string) error {
			if m.failWriteFile {
				return errors.New("read-only file system")
			}
			m.files[p] = body
			return nil
		},
		Size: func(p string) (int64, bool) {
			if n, ok := m.sizes[p]; ok {
				return n, true
			}
			s, ok := m.files[p]
			return int64(len(s)), ok
		},
		Glob: func(pattern string) []string {
			seen := map[string]bool{}
			var out []string
			consider := func(p string) {
				for ; p != "/" && p != "."; p = path.Dir(p) { // a file also makes its directories exist
					if ok, _ := path.Match(pattern, p); ok && !seen[p] {
						seen[p] = true
						out = append(out, p)
					}
				}
			}
			for p := range m.files {
				consider(p)
			}
			for p := range m.sizes {
				consider(p)
			}
			return out
		},
		FreeMB: func(p string) (int64, bool) { n, ok := m.free[p]; return n, ok },
		AptBusy: func() bool {
			return m.busy != nil && m.busy()
		},
		AptLockInfo: func() (string, int64) { return "apt-get", m.clock.Unix() },
		HoldAptLock: func() (func(), error) {
			if m.lockErr != nil {
				return nil, m.lockErr
			}
			m.locks++
			return func() { m.unlocks++ }, nil
		},
		BuilderLock: func() (func(), bool, error) { return nil, true, nil },
		Sleep: func(ctx context.Context, d time.Duration) {
			m.mu.Lock()
			m.clock = m.clock.Add(d)
			f := m.onSleep
			m.mu.Unlock()
			if f != nil {
				f()
			}
		},
		Now: func() time.Time { m.mu.Lock(); defer m.mu.Unlock(); return m.clock },
	}
}

func (m *fakeMachine) job(spec Spec, statusPath string) Job {
	if spec.JobID == "" {
		spec.JobID = "osj_test1"
	}
	return Job{Spec: spec, StatusPath: statusPath, Run: m.run, Host: m.host(), Out: io.Discard, Heartbeat: time.Hour,
		RebootLogPath: filepath.Join(m.t.TempDir(), RebootsFileName)}
}

func (m *fakeMachine) ran(prefix string) bool {
	for _, c := range m.calls {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func (m *fakeMachine) index(prefix string) int {
	for i, c := range m.calls {
		if strings.HasPrefix(c, prefix) {
			return i
		}
	}
	return -1
}

func statusPath(t *testing.T) string { return filepath.Join(t.TempDir(), StatusFileName) }

func mustStatus(t *testing.T, p string) Status {
	t.Helper()
	st, ok, err := ReadStatus(p)
	if err != nil || !ok {
		t.Fatalf("read status: ok=%v err=%v", ok, err)
	}
	return st
}

func asFailure(t *testing.T, err error) *Failure {
	t.Helper()
	var f *Failure
	if !errors.As(err, &f) {
		t.Fatalf("error = %v, want a *Failure", err)
	}
	return f
}
