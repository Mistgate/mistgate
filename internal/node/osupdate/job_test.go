package osupdate

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/node/secupdates"
)

const (
	cleanTable = "ii \tlibc6\t2.40-1\nii \tunattended-upgrades\t2.9\nii \tamneziawg-dkms\t3.1\nii \tlinux-generic\t7.0.0.38\n"
	// The owner's real case: Ubuntu 26.04, linux-generic and the headers of the new kernel were interrupted.
	brokenTable = "ii \tlibc6\t2.40-1\nii \tunattended-upgrades\t2.9\nii \tamneziawg-dkms\t3.1\niF \tlinux-generic\t7.0.0.38\niH \tlinux-headers-7.0.0-38-generic\t7.0.0-38.38\n"

	oldKernel = "7.0.0-37-generic"
	newKernel = "7.0.0-38-generic"
)

var (
	configureAll = commandKey("dpkg", "--force-confdef", "--force-confold", "--configure", "-a")
	dkmsStatus   = commandKey("dkms", "status", "-m", "amneziawg", "-k", newKernel)
	installHdrs  = commandKey("apt-get", "-o", aptLockOption, "install", "-y", "--no-install-recommends", "linux-headers-"+newKernel)
	dkmsAuto     = commandKey("dkms", "autoinstall", "-k", newKernel)
	allUpgrade   = commandKey("apt-get", "-y", "-o", aptLockOption, "-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold", "--with-new-pkgs", "upgrade")
)

// ownerMachine is the host the owner met: a new kernel is installed (reboot required), AmneziaWG runs on the kernel module,
// the module is not built for the new kernel, and the installs of linux-generic and the new headers were interrupted.
func ownerMachine(t *testing.T) *fakeMachine {
	m := newMachine(t)
	m.setKernels(oldKernel, newKernel)
	m.bootFiles(newKernel)
	m.bootFiles(oldKernel)
	m.setDpkg(brokenTable)
	m.files["/sys/module/amneziawg/version"] = "3.1" // loaded; a use count says nothing about its tunnels
	m.files["/run/reboot-required"] = "*** System restart required ***"
	m.ok(configureAll, "")
	m.ok(dkmsStatus, "")
	m.ok(installHdrs, "")
	m.ok(dkmsAuto, "")
	return m
}

// healthy makes the new kernel fully usable: clean dpkg, the AmneziaWG module built for it.
func healthy(m *fakeMachine) {
	m.setDpkg(cleanTable)
	m.sizes["/lib/modules/"+newKernel+"/updates/dkms/amneziawg.ko.zst"] = 1 << 20
	m.sizes["/lib/modules/"+oldKernel+"/updates/dkms/amneziawg.ko.zst"] = 1 << 20
	m.ok(dkmsStatus, "amneziawg/3.1, "+newKernel+", x86_64: installed\n")
}

func noReboot(t *testing.T, m *fakeMachine) {
	t.Helper()
	if m.ran("systemctl reboot") {
		t.Fatalf("the host was rebooted: %v", m.calls)
	}
}

func hasCode(f *Failure, code string) bool { return slices.Contains(f.Reasons, code) }

func TestOwnerCaseRebootOnlyIsRefusedWithReasons(t *testing.T) {
	m := ownerMachine(t)
	p := statusPath(t)
	err := m.job(Spec{Action: ActionReboot, Reboot: RebootIfRequired}, p).Do(context.Background())
	f := asFailure(t, err)
	if f.Code != CodeDpkgInterrupted || !hasCode(f, CodeDpkgInterrupted) || !hasCode(f, CodeAwgModuleMissing) {
		t.Fatalf("failure = %+v, want dpkg_interrupted with awg_module_missing among the reasons", f)
	}
	for _, want := range []string{"linux-generic", "linux-headers-7.0.0-38-generic", "AmneziaWG", newKernel} {
		if !strings.Contains(f.Detail, want) {
			t.Errorf("reason %q does not name %q", f.Detail, want)
		}
	}
	noReboot(t, m)
	if m.ran("apt-get") || m.ran("dpkg --") {
		t.Fatalf("a reboot-only job changed packages: %v", m.calls)
	}
	st := mustStatus(t, p)
	if st.State != StateFailed || st.Reboot != RebootHeld || st.HeldReason != CodeDpkgInterrupted || st.Package != "linux-generic" || st.KernelTo != newKernel {
		t.Fatalf("status = %+v", st)
	}
}

func TestOwnerCaseRepairedButModuleMissingHoldsTheReboot(t *testing.T) {
	m := ownerMachine(t)
	// The repair works this time, but the module is still not built for the new kernel.
	m.on(configureAll, func() (string, error) { m.setDpkg(cleanTable); return "", nil })
	p := statusPath(t)
	err := m.job(Spec{Action: ActionUpgrade, Mode: ModeSecurity, Reboot: RebootIfRequired}, p).Do(context.Background())
	f := asFailure(t, err)
	if f.Code != CodeAwgModuleMissing || len(f.Reasons) != 1 {
		t.Fatalf("failure = %+v, want awg_module_missing only", f)
	}
	noReboot(t, m)
	if !m.ran("unattended-upgrade") || m.ran(installHdrs) || !m.ran(dkmsAuto) { // the job installs no package for the module: it asks DKMS only
		t.Fatalf("repair, install and module steps did not all run: %v", m.calls)
	}
	st := mustStatus(t, p)
	if st.State != StateFailed || st.Reboot != RebootHeld || st.HeldReason != CodeAwgModuleMissing {
		t.Fatalf("status = %+v", st)
	}
}

func TestHealthyUpgradeRebootsAsTheLastStep(t *testing.T) {
	m := ownerMachine(t)
	healthy(m)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.on("systemctl reboot", func() (string, error) { cancel(); return "", nil }) // the shutdown stops the unit
	p := statusPath(t)
	if err := m.job(Spec{Action: ActionUpgrade, Mode: ModeAll, Reboot: RebootIfRequired}, p).Do(ctx); err != nil {
		t.Fatal(err)
	}
	if got := m.calls[len(m.calls)-1]; got != "systemctl reboot" || m.index("systemctl reboot") != len(m.calls)-1 {
		t.Fatalf("the reboot is not the last command: %v", m.calls)
	}
	if !m.ran(allUpgrade) || m.ran("unattended-upgrade") {
		t.Fatalf("mode all must use apt-get upgrade: %v", m.calls)
	}
	st := mustStatus(t, p)
	if st.State != StateRunning || st.Phase != PhaseRebooting || st.KernelTo != newKernel || st.KernelFrom != oldKernel || st.BootID != "boot-one" || !st.AwgRequired {
		t.Fatalf("the status a rebooted host finds = %+v", st)
	}
}

func TestSecurityModeUsesUnattendedUpgradeAndInstallsItWhenMissing(t *testing.T) {
	m := newMachine(t)
	m.fail(commandKey("dpkg-query", "-W", "-f=${Status}", "unattended-upgrades"), "")
	install := commandKey(append([]string{"apt-get"}, secupdates.InstallArgs()...)...)
	m.ok(install, "")
	if err := m.job(Spec{Action: ActionUpgrade, Mode: ModeSecurity, Reboot: RebootNever}, "").Do(context.Background()); err != nil {
		t.Fatal(err)
	}
	if i, u := m.index(install), m.index("unattended-upgrade"); i < 0 || u < i {
		t.Fatalf("unattended-upgrades was not installed before it ran: %v", m.calls)
	}
	noReboot(t, m)
}

func TestRefreshOnlyStopsAfterUpdate(t *testing.T) {
	m := newMachine(t)
	p := statusPath(t)
	if err := m.job(Spec{Action: ActionRefresh, Reboot: RebootNever}, p).Do(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m.ran("unattended-upgrade") || m.ran("apt-get -y") || m.ran("systemctl") {
		t.Fatalf("refresh did more than update: %v", m.calls)
	}
	if st := mustStatus(t, p); st.State != StateDone || st.Reboot != RebootNotAsked || st.Finished == 0 {
		t.Fatalf("status = %+v", st)
	}
}

func TestRebootNotRequiredAndNotAsked(t *testing.T) {
	for _, tc := range []struct{ reboot, want string }{{RebootNever, RebootNotAsked}, {RebootIfRequired, RebootNotRequired}} {
		m := newMachine(t)
		p := statusPath(t)
		if err := m.job(Spec{Action: ActionUpgrade, Mode: ModeSecurity, Reboot: tc.reboot}, p).Do(context.Background()); err != nil {
			t.Fatal(err)
		}
		noReboot(t, m)
		if st := mustStatus(t, p); st.State != StateDone || st.Reboot != tc.want {
			t.Fatalf("%s: status = %+v", tc.reboot, st)
		}
	}
}

func TestRebootAlwaysSkipsOnlyTheNeedTest(t *testing.T) {
	m := newMachine(t) // same kernel, no marker: a reboot is not needed, yet "always" asks for it
	m.bootFiles(oldKernel)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.on("systemctl reboot", func() (string, error) { cancel(); return "", nil })
	if err := m.job(Spec{Action: ActionReboot, Reboot: RebootAlways}, statusPath(t)).Do(ctx); err != nil {
		t.Fatal(err)
	}
	if !m.ran("systemctl reboot") {
		t.Fatal("always did not reboot")
	}
	// ... but never past a refusal.
	m = ownerMachine(t)
	err := m.job(Spec{Action: ActionReboot, Reboot: RebootAlways}, "").Do(context.Background())
	if asFailure(t, err).Code != CodeDpkgInterrupted {
		t.Fatalf("err = %v", err)
	}
	noReboot(t, m)
}

func TestRebootRefusalsEachHoldTheReboot(t *testing.T) {
	cases := map[string]struct {
		prepare func(*fakeMachine)
		code    string
	}{
		"dpkg interrupted":    {func(m *fakeMachine) { m.setDpkg(brokenTable) }, CodeDpkgInterrupted},
		"lock busy":           {func(m *fakeMachine) { m.busy = func() bool { return true } }, CodeAptLock},
		"module missing":      {func(m *fakeMachine) { delete(m.sizes, "/lib/modules/"+newKernel+"/updates/dkms/amneziawg.ko.zst") }, CodeAwgModuleMissing},
		"no initramfs":        {func(m *fakeMachine) { delete(m.sizes, "/boot/initrd.img-"+newKernel) }, CodeInitramfsMissing},
		"truncated initramfs": {func(m *fakeMachine) { m.sizes["/boot/initrd.img-"+newKernel] = 4096 }, CodeInitramfsMissing},
		"no kernel image":     {func(m *fakeMachine) { delete(m.sizes, "/boot/vmlinuz-"+newKernel) }, CodeInitramfsMissing},
		"boot nearly full":    {func(m *fakeMachine) { m.free["/boot"] = 12 }, CodeBootFull},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m := ownerMachine(t)
			healthy(m)
			tc.prepare(m)
			p := statusPath(t)
			err := m.job(Spec{Action: ActionReboot, Reboot: RebootIfRequired}, p).Do(context.Background())
			f := asFailure(t, err)
			if f.Code != tc.code {
				t.Fatalf("code = %s (%+v), want %s", f.Code, f, tc.code)
			}
			noReboot(t, m)
			if st := mustStatus(t, p); st.State != StateFailed || st.Reboot != RebootHeld || st.HeldReason != tc.code {
				t.Fatalf("status = %+v", st)
			}
		})
	}
}

func TestInstallFailureNamesThePackage(t *testing.T) {
	m := newMachine(t)
	m.fail(allUpgrade, "Setting up foo-bar (1.0) ...\ndpkg: error processing package foo-bar (--configure):\n")
	err := m.job(Spec{Action: ActionUpgrade, Mode: ModeAll, Reboot: RebootIfRequired}, "").Do(context.Background())
	f := asFailure(t, err)
	if f.Code != CodeInstallFailed || f.Package != "foo-bar" {
		t.Fatalf("failure = %+v", f)
	}
	noReboot(t, m)
}

func TestInstallLockOutputIsAptLock(t *testing.T) {
	m := newMachine(t)
	m.fail(allUpgrade, "E: Could not get lock /var/lib/dpkg/lock-frontend. It is held by process 123 (apt)\n")
	err := m.job(Spec{Action: ActionUpgrade, Mode: ModeAll, Reboot: RebootNever}, "").Do(context.Background())
	if asFailure(t, err).Code != CodeAptLock {
		t.Fatalf("err = %v", err)
	}
}

func TestUpgradedCountsChangedPackages(t *testing.T) {
	m := newMachine(t)
	m.on(allUpgrade, func() (string, error) {
		m.setDpkg("ii \tlibc6\t2.40-2\nii \tunattended-upgrades\t2.9\nii \tnewpkg\t1\nii \tlinux-image-7.0.0-37-generic\t7.0.0-37.37\n")
		return "", nil
	})
	p := statusPath(t)
	if err := m.job(Spec{Action: ActionUpgrade, Mode: ModeAll, Reboot: RebootNever}, p).Do(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := mustStatus(t, p); st.Upgraded != 2 {
		t.Fatalf("upgraded = %d, want 2 (libc6 changed, newpkg new)", st.Upgraded)
	}
}

func TestWaitsForTheLockThenRuns(t *testing.T) {
	m := newMachine(t)
	polls := 0
	m.busy = func() bool { polls++; return polls < 5 }
	if err := m.job(Spec{Action: ActionUpgrade, Mode: ModeSecurity, Reboot: RebootNever}, "").Do(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !m.ran("unattended-upgrade") || polls < 5 {
		t.Fatalf("polls=%d calls=%v", polls, m.calls)
	}
}

func TestLockNeverFreeFailsBeforeAnyApt(t *testing.T) {
	m := newMachine(t)
	m.busy = func() bool { return true }
	p := statusPath(t)
	err := m.job(Spec{Action: ActionUpgrade, Mode: ModeSecurity, Reboot: RebootIfRequired}, p).Do(context.Background())
	f := asFailure(t, err)
	if f.Code != CodeAptLock || f.AptLockHolder != "apt-get" || f.AptLockMinutes != 15 {
		t.Fatalf("err = %v", err)
	}
	if m.ran("apt-get -o") || m.ran("unattended-upgrade") {
		t.Fatalf("apt ran with the lock held: %v", m.calls)
	}
	if st := mustStatus(t, p); st.State != StateFailed || st.Code != CodeAptLock || st.AptLockHolder != "apt-get" || st.AptLockMinutes != 15 {
		t.Fatalf("status = %+v", st)
	}
}

func TestSecurityModeInstallsOnlySecuritySimulationLeftovers(t *testing.T) {
	m := newMachine(t)
	simulation := commandKey(append([]string{"apt-get"}, aptSimulationArgs...)...)
	leftover := "Inst libfoo1 [1] (2 Ubuntu:24.04/noble-security [amd64])\n" +
		"Inst libbar-common [1] (2 Ubuntu:24.04/noble-updates [amd64])\n"
	m.on(simulation, func() (string, error) {
		if m.ran("unattended-upgrade") {
			return leftover, nil
		}
		return "", nil
	})
	m.ok(commandKey("apt-mark", "showauto"), "libfoo1\n")
	m.ok(commandKey("apt-mark", "auto", "libfoo1"), "")
	install := commandKey("apt-get", "-y", "-o", aptLockOption, "-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold",
		"--no-remove", "install", "libfoo1")
	m.ok(install, "")
	p := statusPath(t)
	if err := m.job(Spec{Action: ActionUpgrade, Mode: ModeSecurity, Reboot: RebootNever}, p).Do(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(m.calls, install) || !slices.Contains(m.calls, "apt-mark auto libfoo1") || slices.Contains(m.calls, "apt-get -y -o "+aptLockOption+" -o Dpkg::Options::=--force-confdef -o Dpkg::Options::=--force-confold --no-remove install libbar-common") {
		t.Fatalf("leftover install did not target only the simulated security package and restore its automatic mark: %v", m.calls)
	}
	if st := mustStatus(t, p); st.State != StateDone || st.NoteCode != "" {
		t.Fatalf("successful leftover install status = %+v", st)
	}
}

func TestSecurityLeftoverFailureIsANonFatalResultNote(t *testing.T) {
	m := newMachine(t)
	simulation := commandKey(append([]string{"apt-get"}, aptSimulationArgs...)...)
	m.on(simulation, func() (string, error) {
		if m.ran("unattended-upgrade") {
			return "Inst libfoo1 [1] (2 Ubuntu:24.04/noble-security [amd64])\n", nil
		}
		return "", nil
	})
	m.ok(commandKey("apt-mark", "showauto"), "")
	install := commandKey("apt-get", "-y", "-o", aptLockOption, "-o", "Dpkg::Options::=--force-confdef", "-o", "Dpkg::Options::=--force-confold",
		"--no-remove", "install", "libfoo1")
	m.fail(install, "E: package could not be unpacked\n")
	p := statusPath(t)
	if err := m.job(Spec{Action: ActionUpgrade, Mode: ModeSecurity, Reboot: RebootNever}, p).Do(context.Background()); err != nil {
		t.Fatalf("a failed leftover install failed the job: %v", err)
	}
	st := mustStatus(t, p)
	if st.State != StateDone || st.NoteCode != NoteSecurityLeftoverFailed || st.Note == "" {
		t.Fatalf("leftover failure result = %+v", st)
	}
}

func TestRepairsInterruptedPackagesBeforeInstalling(t *testing.T) {
	m := newMachine(t)
	m.setDpkg("ii \tlibc6\t1\niF \tfoo\t1\n")
	m.on(configureAll, func() (string, error) { m.setDpkg(cleanTable); return "", nil })
	p := statusPath(t)
	if err := m.job(Spec{Action: ActionUpgrade, Mode: ModeSecurity, Reboot: RebootNever}, p).Do(context.Background()); err != nil {
		t.Fatal(err)
	}
	if c, u := m.index(configureAll), m.index("unattended-upgrade"); c < 0 || u < c {
		t.Fatalf("repair must come first: %v", m.calls)
	}
}

func TestSecuritySourceFailureStopsAndOtherSourcesDoNot(t *testing.T) {
	update := commandKey("apt-get", "-o", aptLockOption, "update")
	m := newMachine(t)
	m.fail(update, "Err:3 http://security.ubuntu.example/ubuntu resolute-security InRelease\n  Temporary failure resolving\n")
	err := m.job(Spec{Action: ActionUpgrade, Mode: ModeSecurity, Reboot: RebootNever}, "").Do(context.Background())
	if asFailure(t, err).Code != CodeSecuritySource || m.ran("unattended-upgrade") {
		t.Fatalf("err = %v calls = %v", err, m.calls)
	}

	m = newMachine(t)
	m.fail(update, "Err:5 https://ppa.example.org/amnezia resolute InRelease\n  403 Forbidden\nE: The repository is not signed.\n")
	p := statusPath(t)
	if err := m.job(Spec{Action: ActionUpgrade, Mode: ModeSecurity, Reboot: RebootNever}, p).Do(context.Background()); err != nil {
		t.Fatal(err)
	}
	if st := mustStatus(t, p); !slices.Equal(st.SourcesFailed, []string{"ppa.example.org"}) || st.State != StateDone {
		t.Fatalf("status = %+v", st)
	}
}

func TestUpdateFailureWithoutErrLines(t *testing.T) {
	update := commandKey("apt-get", "-o", aptLockOption, "update")
	m := newMachine(t)
	m.fail(update, "E: something odd\n")
	if err := m.job(Spec{Action: ActionRefresh, Reboot: RebootNever}, "").Do(context.Background()); asFailure(t, err).Code != CodeRefreshFailed {
		t.Fatalf("err = %v", err)
	}
}

func TestPreflightRefusals(t *testing.T) {
	cases := map[string]struct {
		prepare func(*fakeMachine)
		spec    Spec
		code    string
	}{
		"old ubuntu":    {func(m *fakeMachine) { m.files["/etc/os-release"] = "ID=ubuntu\nVERSION_ID=\"20.04\"\n" }, Spec{Action: ActionRefresh, Reboot: RebootNever}, CodeUnsupportedOS},
		"other distro":  {func(m *fakeMachine) { m.files["/etc/os-release"] = "ID=alpine\nVERSION_ID=3.20\n" }, Spec{Action: ActionReboot, Reboot: RebootAlways}, CodeUnsupportedOS},
		"debian 11":     {func(m *fakeMachine) { m.files["/etc/os-release"] = "ID=debian\nVERSION_ID=\"11\"\n" }, Spec{Action: ActionRefresh, Reboot: RebootNever}, CodeUnsupportedOS},
		"no os-release": {func(m *fakeMachine) { delete(m.files, "/etc/os-release") }, Spec{Action: ActionRefresh, Reboot: RebootNever}, CodeUnsupportedOS},
		"no systemd":    {func(m *fakeMachine) { m.files["/proc/1/comm"] = "init\n" }, Spec{Action: ActionRefresh, Reboot: RebootNever}, CodeNoSystemd},
		"var full":      {func(m *fakeMachine) { m.free["/var"] = 100 }, Spec{Action: ActionUpgrade, Mode: ModeAll, Reboot: RebootNever}, CodeDiskFull},
		"auto reboot": {func(m *fakeMachine) {
			m.ok(commandKey("apt-config", "shell", "R", "Unattended-Upgrade::Automatic-Reboot/b"), "R='true'\n")
		}, Spec{Action: ActionUpgrade, Mode: ModeSecurity, Reboot: RebootIfRequired}, CodeAutoReboot},
		"boot too small for a kernel": {func(m *fakeMachine) {
			m.free["/boot"] = 90
			m.ok(commandKey(append([]string{"apt-get"}, aptSimulationArgs...)...), "Inst linux-image-7.0.0-38-generic (7.0.0-38.38 Ubuntu:26.04/resolute-security [amd64])\n")
		}, Spec{Action: ActionUpgrade, Mode: ModeAll, Reboot: RebootNever}, CodeDiskFull},
	}
	for _, supported := range []string{"ubuntu 22.04", "ubuntu 24.04", "ubuntu 26.04", "debian 12", "debian 13"} {
		id, ver, _ := strings.Cut(supported, " ")
		if !supportedSystem(id, ver) {
			t.Errorf("%s must be supported", supported)
		}
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m := newMachine(t)
			tc.prepare(m)
			err := m.job(tc.spec, statusPath(t)).Do(context.Background())
			if asFailure(t, err).Code != tc.code {
				t.Fatalf("err = %v, want %s", err, tc.code)
			}
			if m.ran("unattended-upgrade") || m.ran("apt-get -y") || m.ran("systemctl") || m.ran("dpkg --") {
				t.Fatalf("a refused job changed the host: %v", m.calls)
			}
		})
	}
}

func TestContainerSkipsKernelChecks(t *testing.T) {
	m := newMachine(t)
	m.files["/run/systemd/container"] = "lxc\n"
	m.files["/run/reboot-required"] = "*** System restart required ***"
	m.free["/boot"] = 1
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.on("systemctl reboot", func() (string, error) { cancel(); return "", nil })
	if err := m.job(Spec{Action: ActionReboot, Reboot: RebootIfRequired}, "").Do(ctx); err != nil {
		t.Fatalf("a container with a reboot marker must reboot: %v", err)
	}
	if m.ran("linux-version") {
		t.Fatalf("kernel tools ran in a container: %v", m.calls)
	}
}

func TestCancelBeforeInstallRunsNothing(t *testing.T) {
	update := commandKey("apt-get", "-o", aptLockOption, "update")
	m := newMachine(t)
	ctx, cancel := context.WithCancel(context.Background())
	m.on(update, func() (string, error) { cancel(); return "", context.Canceled })
	p := statusPath(t)
	err := m.job(Spec{Action: ActionUpgrade, Mode: ModeSecurity, Reboot: RebootIfRequired}, p).Do(ctx)
	if asFailure(t, err).Code != CodeCancelled {
		t.Fatalf("err = %v", err)
	}
	if m.ran("unattended-upgrade") {
		t.Fatalf("installed after a cancel: %v", m.calls)
	}
	noReboot(t, m)
	if st := mustStatus(t, p); st.State != StateFailed || st.Code != CodeCancelled {
		t.Fatalf("status = %+v", st)
	}
}

func TestCancelDuringInstallLetsAptFinishAndDoesNotReboot(t *testing.T) {
	m := ownerMachine(t)
	healthy(m)
	ctx, cancel := context.WithCancel(context.Background())
	m.on(commandKey("unattended-upgrade", "-v"), func() (string, error) { cancel(); return "done\n", nil })
	err := m.job(Spec{Action: ActionUpgrade, Mode: ModeSecurity, Reboot: RebootIfRequired}, "").Do(ctx)
	if asFailure(t, err).Code != CodeCancelled {
		t.Fatalf("err = %v", err)
	}
	noReboot(t, m)
}

func TestCancelledBeforeTheRebootCommandIsStillACancel(t *testing.T) {
	m := ownerMachine(t)
	healthy(m)
	ctx, cancel := context.WithCancel(context.Background())
	m.onSleep = cancel // the 10 s pause before the reboot is the last chance
	p := statusPath(t)
	err := m.job(Spec{Action: ActionReboot, Reboot: RebootIfRequired}, p).Do(ctx)
	if asFailure(t, err).Code != CodeCancelled {
		t.Fatalf("err = %v", err)
	}
	noReboot(t, m)
}

func TestRebootCommandRefusedAndHostThatDoesNotGoDown(t *testing.T) {
	m := ownerMachine(t)
	healthy(m)
	m.fail("systemctl reboot", "Operation inhibited\n")
	p := statusPath(t)
	err := m.job(Spec{Action: ActionReboot, Reboot: RebootIfRequired}, p).Do(context.Background())
	if asFailure(t, err).Code != CodeRebootFailed {
		t.Fatalf("err = %v", err)
	}
	if st := mustStatus(t, p); st.State != StateFailed || st.Reboot != RebootHeld || st.HeldReason != CodeRebootFailed {
		t.Fatalf("status = %+v", st)
	}

	m = ownerMachine(t)
	healthy(m) // the command is accepted and the host never goes down
	err = m.job(Spec{Action: ActionReboot, Reboot: RebootIfRequired}, "").Do(context.Background())
	if asFailure(t, err).Code != CodeRebootFailed {
		t.Fatalf("err = %v", err)
	}
}

func TestAnotherLiveJobOwnsTheStatusFile(t *testing.T) {
	m := newMachine(t)
	p := statusPath(t)
	now := m.host().Now().Unix()
	if err := WriteStatus(p, Status{JobID: "osj_other", Action: ActionUpgrade, State: StateRunning, Started: now, Updated: now, BootID: "boot-one"}); err != nil {
		t.Fatal(err)
	}
	err := m.job(Spec{Action: ActionRefresh, Reboot: RebootNever}, p).Do(context.Background())
	if asFailure(t, err).Code != CodeBusy {
		t.Fatalf("err = %v", err)
	}
	if st := mustStatus(t, p); st.JobID != "osj_other" || st.State != StateRunning {
		t.Fatalf("the other job's status was overwritten: %+v", st)
	}
	// The agent writes "running" for this job id before the launch: that is not a conflict.
	if err := WriteStatus(p, Status{JobID: "osj_test1", Action: ActionRefresh, State: StateRunning, Started: now, Updated: now, BootID: "boot-one"}); err != nil {
		t.Fatal(err)
	}
	if err := m.job(Spec{Action: ActionRefresh, Reboot: RebootNever}, p).Do(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestJobRejectsABadSpecItself(t *testing.T) {
	m := newMachine(t)
	for _, spec := range []Spec{
		{Action: "rm -rf /", Reboot: RebootNever},
		{Action: ActionUpgrade, Mode: "dist-upgrade", Reboot: RebootNever},
		{Action: ActionRefresh, Reboot: RebootAlways},
		{Action: ActionReboot, Reboot: RebootNever},
		{Action: ActionRefresh, Reboot: RebootNever, JobID: "../../etc"},
	} {
		j := m.job(spec, "")
		j.Spec.JobID = spec.JobID
		if j.Spec.JobID == "" {
			j.Spec.JobID = "osj_x"
		}
		if err := j.Do(context.Background()); asFailure(t, err).Code != CodeBadRequest {
			t.Errorf("%+v: err = %v", spec, err)
		}
	}
	if len(m.calls) != 0 {
		t.Fatalf("a bad spec ran commands: %v", m.calls)
	}
}

func TestStatusAndJournalCarryNoPackageManagerOutput(t *testing.T) {
	m := newMachine(t)
	secret := "Err:9 http://203.0.113.7/ubuntu resolute InRelease server said 198.51.100.4 refused"
	m.fail(commandKey("apt-get", "-o", aptLockOption, "update"), secret+"\n")
	var out strings.Builder
	j := m.job(Spec{Action: ActionRefresh, Reboot: RebootNever}, statusPath(t))
	j.Out = &out
	_ = j.Do(context.Background())
	if strings.Contains(out.String(), "203.0.113.7") || strings.Contains(out.String(), "198.51.100.4") {
		t.Fatalf("an address reached the journal: %q", out.String())
	}
	st := mustStatus(t, j.StatusPath)
	blob := st.Reason + st.Code + strings.Join(st.SourcesFailed, ",")
	if strings.Contains(blob, "203.0.113.7") || strings.Contains(blob, "server said") {
		t.Fatalf("status leaked output: %+v", st)
	}
}

func TestJournalIsBounded(t *testing.T) {
	var out strings.Builder
	j := newJournal(&out)
	line := strings.Repeat("x", 300) + "\n"
	for range 2000 {
		_, _ = j.Write([]byte(line))
	}
	j.flush()
	if out.Len() > journalBudget+journalTail*(journalLine+1)+200 {
		t.Fatalf("journal wrote %d bytes", out.Len())
	}
	if !strings.Contains(out.String(), "left out") {
		t.Fatal("a cut stream must say so")
	}
}
