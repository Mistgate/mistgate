package osupdate

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Regression tests of the security review of S2. Each one reproduces a way the job used to reboot a node into a kernel
// without its AmneziaWG module, or to remove the module without saying so, and expects that it no longer does.

var errExit = errors.New("exit status 1")

// Realistic output of `dpkg --configure -a` on the owner's host: the DKMS header hook builds the module for the new kernel
// and the build fails.
const realDkmsConfigureOut = `Setting up linux-headers-7.0.0-38-generic (7.0.0-38.38) ...
/etc/kernel/header_postinst.d/dkms:
 * dkms: running auto installation service for kernel 7.0.0-38-generic
Building module(s)...(bad exit status: 2)
Error! Bad return status for module build on kernel: 7.0.0-38-generic (x86_64)
Consult /var/lib/dkms/amneziawg/1.0.0/build/make.log for more information.
run-parts: /etc/kernel/header_postinst.d/dkms exited with return code 11
dpkg: error processing package linux-headers-7.0.0-38-generic (--configure):
 installed linux-headers-7.0.0-38-generic package post-installation script subprocess returned error exit status 1
dpkg: error processing package linux-generic (--configure):
 dependency problems - leaving unconfigured
`

const repairedTable = "ii \tlibc6\t2.40-1\nii \tunattended-upgrades\t2.9\nii \tlinux-generic\t7.0.0.38\nii \tlinux-headers-7.0.0-38-generic\t7.0.0-38.38\n"

// realRepair makes `dpkg --configure -a` behave as on the owner's host: it fails in the DKMS build of the module, every time.
func realRepair(m *fakeMachine) {
	m.fail(configureAll, realDkmsConfigureOut)
}

// ranPurge: any command removed packages. The job removes none, ever.
func ranPurge(m *fakeMachine) bool {
	for _, c := range m.calls {
		if strings.Contains(c, " purge ") || strings.Contains(c, " remove ") || strings.Contains(c, "autoremove") || strings.Contains(c, "dpkg -r ") {
			return true
		}
	}
	return false
}

func rebootCtx(m *fakeMachine) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())
	m.on("systemctl reboot", func() (string, error) { cancel(); return "", nil })
	return ctx, cancel
}

func TestRepairThatFailsOutsideTheModuleIsDpkgBroken(t *testing.T) {
	m := ownerMachine(t)
	m.fail(configureAll, "dpkg: error processing package foo (--configure):\n subprocess returned error exit status 1\n")
	err := m.job(Spec{Action: ActionUpgrade, Mode: ModeSecurity, Reboot: RebootIfRequired}, "").Do(context.Background())
	if asFailure(t, err).Code != CodeDpkgBroken || ranPurge(m) {
		t.Fatalf("err = %v calls = %v", err, m.calls)
	}
	noReboot(t, m)
}

// A DKMS failure of another module must not take AmneziaWG with it, even though the output mentions amneziawg.
func TestAnotherModulesDkmsFailureDoesNotPurgeAwg(t *testing.T) {
	m := ownerMachine(t)
	healthy(m)
	out := "Building module: amneziawg/1.0.0 ... done.\nInstalling /lib/modules/7.0.0-38-generic/updates/dkms/amneziawg.ko.zst\n" +
		"Error! Bad return status for module build on kernel: 7.0.0-38-generic (x86_64)\nConsult /var/lib/dkms/xtables-addons/3.25/build/make.log for more information.\n" +
		"dpkg: error processing package linux-headers-7.0.0-38-generic (--configure):\n"
	m.fail(allUpgrade, out)
	err := m.job(Spec{Action: ActionUpgrade, Mode: ModeAll, Reboot: RebootNever}, statusPath(t)).Do(context.Background())
	if ranPurge(m) || asFailure(t, err).Code != CodeInstallFailed {
		t.Fatalf("err = %v calls = %v", err, m.calls)
	}
}

func TestOwnDkmsFailureIsRecognizedNarrowly(t *testing.T) {
	for out, want := range map[string]bool{
		realDkmsConfigureOut: true,
		"dpkg: error processing package amneziawg-dkms (--configure):\n":                                                                 true,
		"dpkg: error processing package amneziawg:amd64 (--configure):\n":                                                                true,
		"Consult /var/lib/dkms/amneziawg/3.1.20260906/build/make.log for more info\n":                                                    true,
		"Building module: amneziawg/1.0.0 ... done.\nBad return status for module build\nConsult /var/lib/dkms/zfs/2.2/build/make.log\n": false,
		"dpkg: error processing package amneziawg-tools-extra (--configure):\n":                                                          false,
		"": false,
	} {
		if got := ownDKMSFailure(out); got != want {
			t.Errorf("ownDKMSFailure(%q) = %v, want %v", out, got, want)
		}
	}
}

// Automatic-Reboot: apt answers true or false (the /b form) whatever spelling the configuration uses; a failure to read it
// or an answer that is neither refuses the run.
func TestAutoRebootSettingIsReadAsABooleanAndAFailureRefuses(t *testing.T) {
	key := commandKey("apt-config", "shell", "R", "Unattended-Upgrade::Automatic-Reboot/b")
	for name, set := range map[string]func(*fakeMachine){
		"true":    func(m *fakeMachine) { m.ok(key, "R='true'\n") },
		"error":   func(m *fakeMachine) { m.fail(key, "") },
		"garbage": func(m *fakeMachine) { m.ok(key, "R='on'\n") },
	} {
		m := newMachine(t)
		set(m)
		err := m.job(Spec{Action: ActionUpgrade, Mode: ModeSecurity, Reboot: RebootNever}, statusPath(t)).Do(context.Background())
		if asFailure(t, err).Code != CodeAutoReboot || m.ran("unattended-upgrade") {
			t.Errorf("%s: err = %v calls = %v", name, err, m.calls)
		}
	}
	// A key that is not set: apt-config prints nothing and exits 0. That is false, the default.
	m0 := newMachine(t)
	m0.ok(key, "")
	if err := m0.job(Spec{Action: ActionUpgrade, Mode: ModeSecurity, Reboot: RebootNever}, "").Do(context.Background()); err != nil {
		t.Fatalf("an unset Automatic-Reboot must not refuse: %v", err)
	}
	// All-updates mode does not use unattended-upgrade and does not look.
	m := newMachine(t)
	m.fail(key, "")
	if err := m.job(Spec{Action: ActionUpgrade, Mode: ModeAll, Reboot: RebootNever}, "").Do(context.Background()); err != nil {
		t.Fatal(err)
	}
	if m.ran("apt-config") {
		t.Fatalf("apt-config was asked in all mode: %v", m.calls)
	}
}

// The module counts as needed when it is loaded; a use count (zero for tunnels) is not consulted.
func TestLoadedModuleIsNeededWithoutAnyFlag(t *testing.T) {
	build := func(loaded bool) *fakeMachine {
		m := ownerMachine(t)
		healthy(m)
		delete(m.sizes, "/lib/modules/"+newKernel+"/updates/dkms/amneziawg.ko.zst")
		delete(m.files, "/sys/module/amneziawg/version")
		if loaded {
			m.files["/sys/module/amneziawg/version"] = "3.1"
			m.files["/sys/module/amneziawg/refcnt"] = "0" // live tunnels hold no reference
		}
		return m
	}
	m := build(true)
	err := m.job(Spec{Action: ActionReboot, Reboot: RebootIfRequired}, "").Do(context.Background())
	if asFailure(t, err).Code != CodeAwgModuleMissing {
		t.Fatalf("a loaded module without the flag: err = %v", err)
	}
	noReboot(t, m)

	m = build(false) // not loaded, no flag: nothing to protect
	ctx, cancel := rebootCtx(m)
	defer cancel()
	if err := m.job(Spec{Action: ActionReboot, Reboot: RebootIfRequired}, "").Do(ctx); err != nil {
		t.Fatalf("a module that is not loaded must not hold the reboot: %v", err)
	}
	m = build(false) // not loaded but the agent says the kernel backend is in use
	err = m.job(Spec{Action: ActionReboot, Reboot: RebootIfRequired, AwgRequired: true}, "").Do(context.Background())
	if asFailure(t, err).Code != CodeAwgModuleMissing {
		t.Fatalf("the flag: err = %v", err)
	}
}

// If the newest kernel cannot be told, nothing can be checked about it and GRUB would still start it: refuse.
func TestKernelThatCannotBeToldRefusesTheReboot(t *testing.T) {
	for name, set := range map[string]func(*fakeMachine){
		"list fails": func(m *fakeMachine) { m.kernelFail = true },
		"sort fails": func(m *fakeMachine) { m.kernelSortFail = true },
		"empty list": func(m *fakeMachine) { m.installed = nil },
		"only noise": func(m *fakeMachine) { m.installed = []string{"bad;version"} },
	} {
		t.Run(name, func(t *testing.T) {
			m := ownerMachine(t)
			healthy(m)
			delete(m.sizes, "/lib/modules/"+newKernel+"/updates/dkms/amneziawg.ko.zst") // the module is NOT built for the newest kernel
			set(m)
			p := statusPath(t)
			err := m.job(Spec{Action: ActionReboot, Reboot: RebootIfRequired, AwgRequired: true}, p).Do(context.Background())
			f := asFailure(t, err)
			if f.Code != CodeKernelUnknown {
				t.Fatalf("failure = %+v, want kernel_unknown", f)
			}
			noReboot(t, m)
			if st := mustStatus(t, p); st.Reboot != RebootHeld || st.HeldReason != CodeKernelUnknown {
				t.Fatalf("status = %+v", st)
			}
		})
	}
}

// A container's kernel is the provider's: it is not asked about.
func TestKernelUnknownDoesNotApplyInAContainer(t *testing.T) {
	m := newMachine(t)
	m.files["/run/systemd/container"] = "lxc\n"
	m.files["/run/reboot-required"] = "x"
	m.kernelFail = true
	ctx, cancel := rebootCtx(m)
	defer cancel()
	if err := m.job(Spec{Action: ActionReboot, Reboot: RebootIfRequired}, "").Do(ctx); err != nil {
		t.Fatal(err)
	}
}

// The checks look at the kernel the next boot starts also when it is the running one: a module removed for it, or boot files
// that are gone, hold the reboot ("reboot always" on the same kernel).
func TestChecksApplyToTheBootKernelEvenWhenItIsTheRunningOne(t *testing.T) {
	m := newMachine(t) // same kernel running and installed; the module is loaded
	m.bootFiles(oldKernel)
	m.files["/sys/module/amneziawg/version"] = "3.1"
	err := m.job(Spec{Action: ActionReboot, Reboot: RebootAlways}, statusPath(t)).Do(context.Background())
	if f := asFailure(t, err); f.Code != CodeAwgModuleMissing {
		t.Fatalf("no module file for the running kernel: failure = %+v", f)
	}
	noReboot(t, m)

	m = newMachine(t) // no boot files for it at all
	err = m.job(Spec{Action: ActionReboot, Reboot: RebootAlways}, statusPath(t)).Do(context.Background())
	if f := asFailure(t, err); f.Code != CodeInitramfsMissing {
		t.Fatalf("no boot files: failure = %+v", f)
	}
	noReboot(t, m)
}

// "Strictly newer": when the running kernel is above every installed one, the next boot goes down, and the job does not do it.
func TestRebootIntoAnOlderKernelIsRefused(t *testing.T) {
	m := newMachine(t)
	m.files["/proc/sys/kernel/osrelease"] = "7.0.0-40-generic\n"
	m.installed = []string{"7.0.0-36-generic", "7.0.0-38-generic"}
	m.files["/run/reboot-required"] = "x"
	m.bootFiles("7.0.0-38-generic")
	err := m.job(Spec{Action: ActionReboot, Reboot: RebootIfRequired}, statusPath(t)).Do(context.Background())
	f := asFailure(t, err)
	if f.Code != CodeKernelOlder {
		t.Fatalf("failure = %+v, want kernel_older", f)
	}
	noReboot(t, m)

	// A running kernel that is installed and is not the highest is a normal switch to a newer one.
	m = ownerMachine(t)
	healthy(m)
	m.setDpkg(cleanTable)
	ctx, cancel := rebootCtx(m)
	defer cancel()
	if err := m.job(Spec{Action: ActionReboot, Reboot: RebootIfRequired}, "").Do(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestRebootNeedsTenMinutesOfUptime(t *testing.T) {
	for name, up := range map[string]string{"young": "120.50 100.00\n", "unreadable": "", "garbage": "soon\n"} {
		m := ownerMachine(t)
		healthy(m)
		if up == "" {
			delete(m.files, "/proc/uptime")
		} else {
			m.files["/proc/uptime"] = up
		}
		err := m.job(Spec{Action: ActionReboot, Reboot: RebootIfRequired}, statusPath(t)).Do(context.Background())
		if f := asFailure(t, err); f.Code != CodeTooSoon {
			t.Errorf("%s: failure = %+v", name, f)
		}
		noReboot(t, m)
	}
	m := ownerMachine(t)
	healthy(m)
	m.files["/proc/uptime"] = "601.00 500.00\n"
	ctx, cancel := rebootCtx(m)
	defer cancel()
	if err := m.job(Spec{Action: ActionReboot, Reboot: RebootIfRequired}, "").Do(ctx); err != nil {
		t.Fatalf("just over ten minutes: %v", err)
	}
}

// The dpkg lock is taken after the checks pass and kept to the reboot; a refusal lets it go; a lock somebody holds refuses.
func TestDpkgLockIsHeldFromTheChecksToTheReboot(t *testing.T) {
	m := ownerMachine(t)
	healthy(m)
	ctx, cancel := rebootCtx(m)
	defer cancel()
	m.on("systemctl reboot", func() (string, error) {
		if m.locks != 1 || m.unlocks != 0 {
			t.Errorf("at the reboot command locks=%d unlocks=%d, want the lock held", m.locks, m.unlocks)
		}
		cancel()
		return "", nil
	})
	if err := m.job(Spec{Action: ActionReboot, Reboot: RebootIfRequired}, "").Do(ctx); err != nil {
		t.Fatal(err)
	}

	m = ownerMachine(t) // refused: the lock is let go
	_ = m.job(Spec{Action: ActionReboot, Reboot: RebootIfRequired}, "").Do(context.Background())
	if m.locks != m.unlocks {
		t.Fatalf("a refused reboot kept the lock: locks=%d unlocks=%d", m.locks, m.unlocks)
	}

	m = ownerMachine(t)
	healthy(m)
	m.lockErr = errors.New("resource temporarily unavailable")
	err := m.job(Spec{Action: ActionReboot, Reboot: RebootIfRequired}, "").Do(context.Background())
	if f := asFailure(t, err); f.Code != CodeAptLock {
		t.Fatalf("failure = %+v", f)
	}
	noReboot(t, m)
}

// KillMode=mixed: a stop signals the job only. The apt it started runs on; the job waits for it and then does nothing more.
func TestStopDuringInstallWaitsForAptAndThenStops(t *testing.T) {
	m := ownerMachine(t)
	healthy(m)
	ctx, cancel := context.WithCancel(context.Background())
	var aptSawCancel bool
	m.on(allUpgrade, func() (string, error) {
		cancel() // systemctl stop: SIGTERM reaches the job while apt is running
		aptSawCancel = m.curCtx.Err() != nil
		return "", nil
	})
	err := m.job(Spec{Action: ActionUpgrade, Mode: ModeAll, Reboot: RebootIfRequired}, statusPath(t)).Do(ctx)
	if aptSawCancel {
		t.Fatal("the running apt-get was handed a cancelled context: it would be killed in mid-transaction")
	}
	if asFailure(t, err).Code != CodeCancelled {
		t.Fatalf("err = %v", err)
	}
	noReboot(t, m)
}

// The phase says what the job is doing: after the install it is "checking" (then "rebooting"), not a stale installing or
// waiting_lock.
func TestPhaseAfterTheInstallIsHonest(t *testing.T) {
	m := ownerMachine(t)
	healthy(m)
	delete(m.sizes, "/boot/initrd.img-"+newKernel)
	p := statusPath(t)
	_ = m.job(Spec{Action: ActionUpgrade, Mode: ModeAll, Reboot: RebootIfRequired}, p).Do(context.Background())
	if st := mustStatus(t, p); st.Phase != PhaseChecking {
		t.Fatalf("phase = %q", st.Phase)
	}
	m = ownerMachine(t)
	healthy(m)
	ctx, cancel := rebootCtx(m)
	defer cancel()
	_ = m.job(Spec{Action: ActionReboot, Reboot: RebootIfRequired}, p).Do(ctx)
	if st := mustStatus(t, p); st.Phase != PhaseRebooting {
		t.Fatalf("phase = %q", st.Phase)
	}
}

// The package table is read in full: the interrupted packages at its head are not cut off by a size limit.
func TestLargePackageTableKeepsTheInterruptedPackagesAtItsHead(t *testing.T) {
	var b strings.Builder
	b.WriteString("iF \taaa-first\t1\n")
	for i := 0; i < 30000; i++ {
		b.WriteString("ii \tpackage-with-a-fairly-long-name-" + strings.Repeat("x", 10) + "\t1.2.3-4ubuntu5.6\n")
	}
	if b.Len() < 256<<10 {
		t.Fatal("the fixture is too small to prove anything")
	}
	m := ownerMachine(t)
	m.setDpkg(b.String())
	err := m.job(Spec{Action: ActionReboot, Reboot: RebootIfRequired}, statusPath(t)).Do(context.Background())
	f := asFailure(t, err)
	if !hasCode(f, CodeDpkgInterrupted) || f.Package != "aaa-first" {
		t.Fatalf("failure = %+v", f)
	}
}
