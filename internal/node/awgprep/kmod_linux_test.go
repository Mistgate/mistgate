//go:build linux

package awgprep

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const kmodTestKernel = "7.0.0-38-generic"

type kmodTestHost struct {
	root               string
	files              map[string][]byte
	heads              map[string]bool
	modules            map[string]bool
	dkmsStatus         string
	dkmsErr            error
	packages           string
	aptBusy            bool
	aptBusyAfterRemove bool
	releaseAptAfter    int
	sleeps             int
	lockAcquired       bool
	releaseLockAfter   int
	lockErr            error
	modprobeErr        string
	buildErrKernel     string
	blockBuild         bool
	commands           [][]string
	configureCalls     int
	dpkgWhileAptBusy   bool
}

func newKmodTestHost(t *testing.T) *kmodTestHost {
	t.Helper()
	root := t.TempDir()
	source := filepath.Join(root, "amneziawg-1.0.0")
	if err := os.MkdirAll(filepath.Join(source, "compat"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "compat", "compat.h"), []byte("test compat source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "src.c"), []byte("test module source\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return &kmodTestHost{
		root:         root,
		files:        map[string][]byte{},
		heads:        map[string]bool{kmodTestKernel: true},
		modules:      map[string]bool{},
		dkmsStatus:   "ii",
		packages:     "ii\tlinux-image-" + kmodTestKernel + "\nii\tlinux-headers-" + kmodTestKernel + "\n",
		lockAcquired: true,
	}
}

func (h *kmodTestHost) config(t *testing.T, mode KmodMode, status string) KmodConfig {
	t.Helper()
	stateDir := filepath.Join(h.root, "state")
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		t.Fatal(err)
	}
	sys := Sys{
		ReadFile: func(path string) ([]byte, error) {
			if b, ok := h.files[path]; ok {
				return append([]byte(nil), b...), nil
			}
			return os.ReadFile(path)
		},
		WriteFile: func(path, body string) error {
			h.files[path] = []byte(body)
			return nil
		},
		RemoveFile: func(path string) error {
			delete(h.files, path)
			return nil
		},
		SourceDirs: func() []string { return []string{filepath.Join(h.root, "amneziawg-1.0.0")} },
		Exists: func(path string) bool {
			if strings.HasPrefix(path, "/usr/src/linux-headers-") {
				return h.headers()[strings.TrimPrefix(path, "/usr/src/linux-headers-")]
			}
			return false
		},
		AptBusy: func() bool { return h.aptBusy },
		Sleep: func(context.Context, time.Duration) {
			h.sleeps++
			if h.releaseAptAfter > 0 && h.sleeps >= h.releaseAptAfter {
				h.aptBusy = false
			}
			if h.releaseLockAfter > 0 && h.sleeps >= h.releaseLockAfter {
				h.lockAcquired = true
			}
		},
		TryBuilderLock: func() (func(), bool, error) {
			return func() {}, h.lockAcquired, h.lockErr
		},
		KernelRelease: func() string { return kmodTestKernel },
	}
	if status == "" {
		status = filepath.Join(stateDir, KmodStatusFileName)
	}
	return KmodConfig{
		Mode: mode, StatusPath: status, NodePath: "/usr/local/bin/mistgate-node", StateDir: stateDir,
		Out: io.Discard, Run: h.run, Sys: sys, Heartbeat: time.Hour,
		Budget: time.Second, LockWait: 30 * time.Second,
	}
}

func (h *kmodTestHost) headers() map[string]bool { return h.heads }

func (h *kmodTestHost) run(ctx context.Context, _ io.Writer, _ string, cmd ...string) (string, error) {
	args := append([]string(nil), cmd...)
	h.commands = append(h.commands, args)
	if err := ctx.Err(); err != nil {
		return "", err
	}
	switch cmd[0] {
	case "dpkg-query":
		if strings.Contains(strings.Join(cmd, " "), "${Package}") {
			return h.packages, nil
		}
		if h.dkmsErr != nil {
			return "no packages found matching amneziawg-dkms", h.dkmsErr
		}
		return h.dkmsStatus, nil
	case "modinfo":
		if len(cmd) >= 3 && h.modules[cmd[2]] {
			return "/lib/modules/" + cmd[2] + "/updates/dkms/amneziawg.ko\n", nil
		}
		return "", errors.New("module file not found")
	case "modprobe":
		if h.modprobeErr != "" {
			return h.modprobeErr, errors.New("exit status 1")
		}
		if !h.modules[kmodTestKernel] { // like the real modprobe: there is no module file for the running kernel
			return "modprobe: FATAL: Module amneziawg not found", errors.New("exit status 1")
		}
		return "", nil
	case "dkms":
		if len(cmd) > 1 && cmd[1] == "remove" && h.aptBusyAfterRemove {
			h.aptBusy = true
		}
		if len(cmd) > 1 && cmd[1] == "autoinstall" {
			if kernel := cmd[len(cmd)-1]; kernel == h.buildErrKernel {
				return "fake build failure", errors.New("exit status 1")
			}
			if h.blockBuild {
				<-ctx.Done()
				return "", ctx.Err()
			}
			h.modules[cmd[len(cmd)-1]] = true
			return "", nil
		}
		if len(cmd) > 1 && cmd[1] == "install" && len(cmd) >= 6 {
			h.modules[cmd[len(cmd)-1]] = true
		}
		return "", nil
	case "dpkg":
		if h.aptBusy {
			h.dpkgWhileAptBusy = true
			return "apt lock still held", errors.New("unexpected dpkg while locked")
		}
		if strings.Contains(strings.Join(cmd, " "), "--configure -a") {
			h.configureCalls++
			h.dkmsStatus = "ii"
			h.packages = strings.ReplaceAll(h.packages, "iU\t", "ii\t")
		}
		return "", nil
	default:
		return "", errors.New("unexpected fake command: " + strings.Join(cmd, " "))
	}
}

func countKmodCommand(h *kmodTestHost, want ...string) int {
	n := 0
	for _, cmd := range h.commands {
		if len(cmd) < len(want) {
			continue
		}
		matches := true
		for i := range want {
			matches = matches && cmd[i] == want[i]
		}
		if matches {
			n++
		}
	}
	return n
}

func TestKmodBootBuildsAndLoadsMissingModuleAndWritesHook(t *testing.T) {
	h := newKmodTestHost(t)
	cfg := h.config(t, KmodBoot, "")
	if err := RunKmodSync(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if !h.modules[kmodTestKernel] || countKmodCommand(h, "dkms", "autoinstall", "-k", kmodTestKernel) != 1 || countKmodCommand(h, "modprobe", "amneziawg") != 1 {
		t.Fatalf("module not built and loaded; commands=%v modules=%v", h.commands, h.modules)
	}
	want, err := KmodHookBody(cfg.NodePath, cfg.StateDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := string(h.files[KmodHookPath]); got != want {
		t.Fatalf("hook content = %q, want %q", got, want)
	}
	if !strings.Contains(want, `timeout -k 5 20 /usr/local/bin/mistgate-node awg kmod-sync --patch-only || true`) ||
		!strings.Contains(want, `timeout 10 systemd-run --no-block --quiet --collect --unit=mistgate-awg-kmod --property=RuntimeMaxSec=3600`) || !strings.HasSuffix(strings.TrimSpace(want), `|| true; fi"; };`) {
		t.Fatalf("unexpected apt hook commands: %s", want)
	}
}

func TestKmodBootSecureBootDoesNotRebuild(t *testing.T) {
	h := newKmodTestHost(t)
	h.modules[kmodTestKernel] = true
	h.modprobeErr = "modprobe: ERROR: Key was rejected by service"
	if err := RunKmodSync(context.Background(), h.config(t, KmodBoot, "")); err != nil {
		t.Fatal(err)
	}
	if countKmodCommand(h, "dkms") != 0 {
		t.Fatalf("Secure Boot rejection attempted a DKMS build: %v", h.commands)
	}
}

func TestKmodBootUsesSecureBootStateForLockdownFailure(t *testing.T) {
	h := newKmodTestHost(t)
	h.modules[kmodTestKernel] = true
	h.modprobeErr = "modprobe: Operation not permitted"
	cfg := h.config(t, KmodBoot, "")
	cfg.SecureBoot = func(context.Context) bool { return true }
	if err := RunKmodSync(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if countKmodCommand(h, "dkms") != 0 {
		t.Fatalf("Secure Boot state did not suppress the rebuild: %v", h.commands)
	}
}

func TestKmodBootSkipsBuildWhileAptIsBusyButLoadsBuiltModule(t *testing.T) {
	h := newKmodTestHost(t)
	h.aptBusy = true
	h.modules[kmodTestKernel] = true
	cfg := h.config(t, KmodBoot, "")
	if err := RunKmodSync(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if countKmodCommand(h, "dkms") != 0 || countKmodCommand(h, "modprobe", "amneziawg") != 1 {
		t.Fatalf("apt-busy boot commands = %v", h.commands)
	}
	want, _ := KmodHookBody(cfg.NodePath, cfg.StateDir)
	if got := string(h.files[KmodHookPath]); got != want {
		t.Fatalf("apt-busy boot did not rewrite hook: %q", got)
	}
}

func TestKmodBootRecordsFailedBuildAndAfterAptRetries(t *testing.T) {
	h := newKmodTestHost(t)
	h.buildErrKernel = kmodTestKernel
	boot := h.config(t, KmodBoot, "")
	if err := RunKmodSync(context.Background(), boot); err == nil {
		t.Fatal("expected boot build failure")
	}
	if err := RunKmodSync(context.Background(), boot); err == nil {
		t.Fatal("expected the unbuilt module to remain unavailable")
	}
	if got := countKmodCommand(h, "dkms", "autoinstall"); got != 1 {
		t.Fatalf("boot retried the same failed source: %d calls; %v", got, h.commands)
	}
	afterApt := h.config(t, KmodAfterApt, boot.StatusPath)
	if err := RunKmodSync(context.Background(), afterApt); err == nil {
		t.Fatal("expected after-apt retry to report the build failure")
	}
	if got := countKmodCommand(h, "dkms", "autoinstall"); got != 2 {
		t.Fatalf("after-apt did not retry failed source: %d calls; %v", got, h.commands)
	}
}

func TestKmodBootRebuildsPresentModuleOnlyOncePerSource(t *testing.T) {
	h := newKmodTestHost(t)
	h.modules[kmodTestKernel] = true
	h.modprobeErr = "modprobe: ERROR: unknown symbol in amneziawg"
	cfg := h.config(t, KmodBoot, "")
	if err := RunKmodSync(context.Background(), cfg); err == nil {
		t.Fatal("first failed rebuild unexpectedly succeeded")
	}
	first := countKmodCommand(h, "dkms", "remove", "-m", "amneziawg", "-v", "1.0.0", "-k", kmodTestKernel)
	install := countKmodCommand(h, "dkms", "install", "-m", "amneziawg", "-v", "1.0.0", "-k", kmodTestKernel)
	if first != 1 || install != 1 {
		t.Fatalf("DKMS repair commands = remove:%d install:%d; all=%v", first, install, h.commands)
	}
	if err := RunKmodSync(context.Background(), cfg); err == nil {
		t.Fatal("second failed load unexpectedly succeeded")
	}
	if got := countKmodCommand(h, "dkms", "remove"); got != 1 {
		t.Fatalf("rebuilt the same kernel/source more than once: %d, commands=%v", got, h.commands)
	}
	st, ok, err := ReadKmodStatus(cfg.StatusPath)
	if err != nil || !ok || len(st.Rebuilt) != 1 || st.Rebuilt[0].Kernel != kmodTestKernel || len(st.Rebuilt[0].SourceSHA) != 64 {
		t.Fatalf("persisted rebuild record = %+v, ok=%v err=%v", st.Rebuilt, ok, err)
	}
}

func TestKmodBootRechecksAptBeforeDKMSInstall(t *testing.T) {
	h := newKmodTestHost(t)
	h.modules[kmodTestKernel] = true
	h.modprobeErr = "unknown symbol in amneziawg"
	h.aptBusyAfterRemove = true
	err := RunKmodSync(context.Background(), h.config(t, KmodBoot, ""))
	if err == nil || countKmodCommand(h, "dkms", "remove") != 1 || countKmodCommand(h, "dkms", "install") != 0 {
		t.Fatalf("apt lock recheck error=%v commands=%v", err, h.commands)
	}
}

func TestKmodAfterAptWaitsThenConfiguresAllowedPackages(t *testing.T) {
	h := newKmodTestHost(t)
	h.aptBusy, h.releaseAptAfter = true, 1
	h.dkmsStatus = "iU"
	h.packages = "iU\tlinux-image-" + kmodTestKernel + "\nii\tlinux-headers-" + kmodTestKernel + "\niU\tamneziawg-dkms\n"
	cfg := h.config(t, KmodAfterApt, "")
	if err := RunKmodSync(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if h.sleeps == 0 || h.configureCalls != 1 || h.dpkgWhileAptBusy {
		t.Fatalf("wait/configure = sleeps:%d configure:%d while-locked:%v; commands=%v", h.sleeps, h.configureCalls, h.dpkgWhileAptBusy, h.commands)
	}
	if !h.modules[kmodTestKernel] {
		t.Fatalf("kernel was not built after package configuration: %v", h.commands)
	}
}

func TestKmodAfterAptSkipsUnrelatedHalfConfiguredPackage(t *testing.T) {
	h := newKmodTestHost(t)
	h.packages = "iU\tlibfoo1\nii\tlinux-image-" + kmodTestKernel + "\nii\tlinux-headers-" + kmodTestKernel + "\n"
	if err := RunKmodSync(context.Background(), h.config(t, KmodAfterApt, "")); err != nil {
		t.Fatal(err)
	}
	if h.configureCalls != 0 || countKmodCommand(h, "dpkg", "--force-confdef") != 0 {
		t.Fatalf("configured with unrelated half-installed package: %v", h.commands)
	}
}

func TestKmodConfigureAllowsKernelPrefixesAndIgnoresTriggeredStates(t *testing.T) {
	for _, tc := range []struct {
		name, status, pkg string
		want              int
	}{
		{name: "linux metapackage", status: "iU", pkg: "linux-generic", want: 1},
		{name: "amneziawg package", status: "iF", pkg: "amneziawg-dkms", want: 1},
		{name: "awaited state", status: "iW", pkg: "man-db", want: 0},
		{name: "triggered state", status: "it", pkg: "initramfs-tools", want: 0},
		{name: "half-installed", status: "iH", pkg: "linux-generic", want: 0},
		{name: "unrelated half-configured", status: "iU", pkg: "libfoo1", want: 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newKmodTestHost(t)
			h.packages = tc.status + "\t" + tc.pkg + "\n"
			cfg := h.config(t, KmodAfterApt, "")
			if err := kmodConfigureIfSafe(context.Background(), cfg); err != nil {
				t.Fatal(err)
			}
			if h.configureCalls != tc.want {
				t.Fatalf("configure calls=%d want=%d; commands=%v", h.configureCalls, tc.want, h.commands)
			}
		})
	}
}

func TestKmodDKMSInstalledUsesInstalledStatusLetter(t *testing.T) {
	for _, tc := range []struct {
		status string
		want   bool
	}{{"hi", true}, {"iU", true}, {"ii", true}, {"rc", false}, {"un", false}} {
		h := newKmodTestHost(t)
		h.dkmsStatus = tc.status
		got, err := KmodDKMSInstalled(context.Background(), h.run)
		if err != nil || got != tc.want {
			t.Errorf("status %q: installed=%v err=%v, want %v", tc.status, got, err, tc.want)
		}
	}
}

func TestKmodAfterAptDoesNotConfigureWhileAptLockIsHeld(t *testing.T) {
	h := newKmodTestHost(t)
	h.aptBusy = true
	cfg := h.config(t, KmodAfterApt, "")
	cfg.LockWait = time.Millisecond
	if err := RunKmodSync(context.Background(), cfg); err == nil {
		t.Fatal("expected apt-lock timeout")
	}
	if h.configureCalls != 0 || h.dpkgWhileAptBusy || countKmodCommand(h, "dpkg", "--force-confdef") != 0 {
		t.Fatalf("dpkg ran while apt lock was held: %v", h.commands)
	}
}

func TestKmodAfterAptWaitsForBuilderLock(t *testing.T) {
	h := newKmodTestHost(t)
	h.lockAcquired, h.releaseLockAfter = false, 1
	cfg := h.config(t, KmodAfterApt, "")
	if err := RunKmodSync(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if h.sleeps == 0 || countKmodCommand(h, "dkms", "autoinstall") != 1 {
		t.Fatalf("after-apt did not wait and build: sleeps=%d commands=%v", h.sleeps, h.commands)
	}
}

func TestKmodAfterAptBuilderLockWaitIsBounded(t *testing.T) {
	h := newKmodTestHost(t)
	h.lockAcquired = false
	cfg := h.config(t, KmodAfterApt, "")
	cfg.LockWait = 2 * time.Second
	err := RunKmodSync(context.Background(), cfg)
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != CodeBusy || countKmodCommand(h, "dpkg", "--force-confdef") != 0 {
		t.Fatalf("lock wait error=%v commands=%v", err, h.commands)
	}
}

func TestKmodRepairLoadsRunningKernelEvenIfAnotherKernelBuildFails(t *testing.T) {
	h := newKmodTestHost(t)
	other := "7.0.0-39-generic"
	h.heads[other] = true
	h.packages += "ii\tlinux-image-" + other + "\nii\tlinux-headers-" + other + "\n"
	h.buildErrKernel = other
	err := RunKmodSync(context.Background(), h.config(t, KmodRepair, ""))
	if err == nil {
		t.Fatal("expected failure from the other kernel build")
	}
	if countKmodCommand(h, "modprobe", "amneziawg") != 1 {
		t.Fatalf("repair skipped loading the running kernel: %v", h.commands)
	}
}

func TestKmodBootBudgetCancelsBuild(t *testing.T) {
	h := newKmodTestHost(t)
	h.blockBuild = true
	cfg := h.config(t, KmodBoot, "")
	cfg.Budget = 15 * time.Millisecond
	started := time.Now()
	err := RunKmodSync(context.Background(), cfg)
	if err == nil || time.Since(started) > time.Second || countKmodCommand(h, "dkms", "autoinstall") != 1 {
		t.Fatalf("budget result err=%v elapsed=%s commands=%v", err, time.Since(started), h.commands)
	}
}

func TestKmodAfterAptHonorsOverallContextDeadline(t *testing.T) {
	h := newKmodTestHost(t)
	h.blockBuild = true
	cfg := h.config(t, KmodAfterApt, "")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Millisecond)
	defer cancel()
	err := RunKmodSync(ctx, cfg)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("after-apt deadline error = %v", err)
	}
}

func TestKmodPatchOnlyDoesNotPrintOrQueryPackages(t *testing.T) {
	h := newKmodTestHost(t)
	var output bytes.Buffer
	cfg := h.config(t, KmodPatchOnly, "")
	cfg.Out = &output
	if err := RunKmodSync(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 || len(h.commands) != 0 {
		t.Fatalf("patch-only output=%q commands=%v", output.String(), h.commands)
	}
}

func TestKmodVersionSelectsNewestDKMSSource(t *testing.T) {
	got, err := kmodVersion(Sys{SourceDirs: func() []string { return []string{"/usr/src/amneziawg-1.9.0", "/usr/src/amneziawg-1.10.0"} }})
	if err != nil || got != "1.10.0" {
		t.Fatalf("version=%q err=%v", got, err)
	}
}

func TestKmodSyncExitsWhenAnotherBuilderHeartbeatIsLive(t *testing.T) {
	h := newKmodTestHost(t)
	cfg := h.config(t, KmodBoot, "")
	other := filepath.Join(cfg.StateDir, "awg-prepare.json")
	now := time.Now().Unix()
	if err := os.WriteFile(other, []byte(`{"state":"running","updated":`+strconv.FormatInt(now, 10)+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RunKmodSync(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if len(h.commands) != 0 {
		t.Fatalf("kmod-sync ran host commands beside a prepare job: %v", h.commands)
	}
}

func TestKmodSyncDoesNotTreatOSRebootingAsLiveBuilder(t *testing.T) {
	h := newKmodTestHost(t)
	cfg := h.config(t, KmodBoot, "")
	osUpdate := filepath.Join(cfg.StateDir, "os-update.json")
	now := time.Now().Unix()
	if err := os.WriteFile(osUpdate, []byte(`{"state":"running","phase":"rebooting","updated":`+strconv.FormatInt(now, 10)+`}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RunKmodSync(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if countKmodCommand(h, "dkms", "autoinstall", "-k", kmodTestKernel) != 1 {
		t.Fatalf("rebooting OS status blocked module sync: %v", h.commands)
	}
}

func TestKmodHookIsRemovedWhenDKMSIsGone(t *testing.T) {
	h := newKmodTestHost(t)
	h.dkmsStatus, h.dkmsErr = "", errors.New("exit status 1")
	h.files[KmodHookPath] = []byte("stale hook")
	// The standard dpkg-query diagnostic is the one treated as an absent package.
	cfg := h.config(t, KmodBoot, "")
	if err := RunKmodSync(context.Background(), cfg); err != nil {
		t.Fatal(err)
	}
	if _, exists := h.files[KmodHookPath]; exists {
		t.Fatal("stale apt hook was not removed")
	}
}
