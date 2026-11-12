package osupdate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The AmneziaWG module's packages are never removed by the job. A failing build of the module stops it: nothing is removed, nothing
// more is installed, the reboot is held, and the text does not send the owner to a command that would remove the module.

func noMoreWork(t *testing.T, m *fakeMachine) {
	t.Helper()
	if ranPurge(m) {
		t.Fatalf("a package was removed: %v", m.calls)
	}
	for _, c := range m.calls {
		if strings.HasPrefix(c, "unattended-upgrade") || c == allUpgrade || strings.Contains(c, " update") && strings.HasPrefix(c, "apt-get") {
			t.Fatalf("the job went on after the build failure (%q): %v", c, m.calls)
		}
	}
}

func TestOwnerCaseSecurityPlusRebootStopsOnTheModulesBuildFailure(t *testing.T) {
	for name, awg := range map[string]bool{"without the flag": false, "with the flag": true} {
		t.Run(name, func(t *testing.T) {
			m := ownerMachine(t) // the module is loaded
			realRepair(m)
			ctx, cancel := rebootCtx(m)
			defer cancel()
			p := statusPath(t)
			err := m.job(Spec{Action: ActionUpgrade, Mode: ModeSecurity, Reboot: RebootIfRequired, AwgRequired: awg}, p).Do(ctx)
			f := asFailure(t, err)
			if f.Code != CodeAwgBuildFailed || f.Package != "linux-headers-"+newKernel {
				t.Fatalf("failure = %+v, want awg_module_build_failed at the headers of %s", f, newKernel)
			}
			if !strings.Contains(f.Detail, newKernel) || strings.Contains(f.Detail, "prepare-kernel") {
				t.Fatalf("reason = %q: it must name the kernel and not send the owner to prepare-kernel", f.Detail)
			}
			noReboot(t, m)
			noMoreWork(t, m)
			st := mustStatus(t, p)
			if st.State != StateFailed || st.Code != CodeAwgBuildFailed || st.Reboot != RebootHeld || st.HeldReason != CodeAwgBuildFailed || st.Package != f.Package || !st.AwgRequired {
				t.Fatalf("status = %+v", st)
			}
		})
	}
}

func TestBuildFailureWithoutARebootIsStillAFailureAndInstallsNothingMore(t *testing.T) {
	m := ownerMachine(t)
	realRepair(m)
	p := statusPath(t)
	err := m.job(Spec{Action: ActionUpgrade, Mode: ModeSecurity, Reboot: RebootNever}, p).Do(context.Background())
	f := asFailure(t, err)
	st := mustStatus(t, p)
	if f.Code != CodeAwgBuildFailed || st.State != StateFailed || st.Reboot != "" {
		t.Fatalf("err = %v status = %+v", err, st)
	}
	noMoreWork(t, m)
}

func TestBuildFailureInTheInstallStopsAndNamesTheKernelDkmsNamed(t *testing.T) {
	m := ownerMachine(t)
	m.setDpkg(cleanTable)
	m.fail(allUpgrade, realDkmsConfigureOut)
	err := m.job(Spec{Action: ActionUpgrade, Mode: ModeAll, Reboot: RebootIfRequired}, statusPath(t)).Do(context.Background())
	f := asFailure(t, err)
	if f.Code != CodeAwgBuildFailed || f.Package != "linux-headers-"+newKernel || ranPurge(m) {
		t.Fatalf("failure = %+v calls = %v", f, m.calls)
	}
	noReboot(t, m)
}

// unattended-upgrade logs the output of dpkg to its own file, so a failure inside dpkg is not in what the job reads: the job looks
// at dpkg itself and repairs, with the output of the repair in front of it.
func TestSecurityModeFailureWithHiddenDpkgOutputIsRepairedAndRead(t *testing.T) {
	m := ownerMachine(t)
	m.setDpkg(cleanTable)
	m.on("unattended-upgrade -v", func() (string, error) {
		m.setDpkg(brokenTable) // dpkg was left half configured by the run; u-u prints nothing about it
		return "Starting unattended upgrades script\nAn error occurred: see the log\n", errExit
	})
	realRepair(m)
	err := m.job(Spec{Action: ActionUpgrade, Mode: ModeSecurity, Reboot: RebootIfRequired}, statusPath(t)).Do(context.Background())
	if f := asFailure(t, err); f.Code != CodeAwgBuildFailed {
		t.Fatalf("failure = %+v, want awg_module_build_failed (the module's DKMS build was the cause, read from the repair)", f)
	}
	if ranPurge(m) {
		t.Fatalf("a package was removed: %v", m.calls)
	}
	noReboot(t, m)

	// Not the module: the package at fault is named, dpkg is repaired, the update has to be run again.
	m = ownerMachine(t)
	m.setDpkg(cleanTable)
	m.on("unattended-upgrade -v", func() (string, error) { m.setDpkg(brokenTable); return "An error occurred\n", errExit })
	m.on(configureAll, func() (string, error) { m.setDpkg(repairedTable); return "", nil })
	err = m.job(Spec{Action: ActionUpgrade, Mode: ModeSecurity, Reboot: RebootNever}, statusPath(t)).Do(context.Background())
	if f := asFailure(t, err); f.Code != CodeInstallFailed || f.Package != "linux-generic" {
		t.Fatalf("failure = %+v", f)
	}
}

// DKMS is asked to build for the new kernel; the job installs no package for that, not even the headers. A build that fails
// stops the job; a failure that is not the module's build (the headers are not there) only leaves the module missing.
func TestModuleStepAsksDkmsOnlyAndNeverInstallsHeaders(t *testing.T) {
	m := ownerMachine(t)
	m.setDpkg(cleanTable)
	m.fail(dkmsAuto, "Error! Your kernel headers for kernel 7.0.0-38-generic cannot be found at /lib/modules/7.0.0-38-generic/build.\n")
	err := m.job(Spec{Action: ActionUpgrade, Mode: ModeSecurity, Reboot: RebootIfRequired}, statusPath(t)).Do(context.Background())
	if f := asFailure(t, err); f.Code != CodeAwgModuleMissing {
		t.Fatalf("failure = %+v, want awg_module_missing", f)
	}
	if m.ran(installHdrs) || m.ran("apt-get -o "+aptLockOption+" install") {
		t.Fatalf("a package was installed for the module: %v", m.calls)
	}
	noReboot(t, m)

	m = ownerMachine(t)
	m.setDpkg(cleanTable)
	m.fail(dkmsAuto, "Error! Bad return status for module build on kernel: 7.0.0-38-generic (x86_64)\nConsult /var/lib/dkms/amneziawg/3.1/build/make.log\n")
	err = m.job(Spec{Action: ActionUpgrade, Mode: ModeSecurity, Reboot: RebootIfRequired}, statusPath(t)).Do(context.Background())
	if f := asFailure(t, err); f.Code != CodeAwgBuildFailed || f.Package != "linux-headers-"+newKernel || ranPurge(m) {
		t.Fatalf("failure = %+v calls = %v", f, m.calls)
	}
	noReboot(t, m)

	// The module's own package left half configured by the build.
	m = ownerMachine(t)
	m.setDpkg(cleanTable)
	m.on(dkmsAuto, func() (string, error) { m.setDpkg("ii \tlibc6\t1\niF \tamneziawg-dkms\t3.1\n"); return "", nil })
	err = m.job(Spec{Action: ActionUpgrade, Mode: ModeAll, Reboot: RebootNever}, statusPath(t)).Do(context.Background())
	if f := asFailure(t, err); f.Code != CodeAwgBuildFailed {
		t.Fatalf("a half configured module package: %+v", f)
	}
}

// ---------------------------------------------------------------- every kernel the boot menu can end up on

func menuMachine(t *testing.T) *fakeMachine {
	m := ownerMachine(t)
	healthy(m)
	m.installed = []string{"7.0.0-36-generic", oldKernel, newKernel, "7.0.0-39-generic"} // running: oldKernel (7.0.0-37)
	for _, k := range []string{"7.0.0-36-generic", "7.0.0-39-generic"} {
		m.bootFiles(k)
		m.sizes["/lib/modules/"+k+"/updates/dkms/amneziawg.ko.zst"] = 1 << 20
	}
	return m
}

func TestModuleAndBootFilesAreCheckedForEveryKernelNotOlderThanTheRunningOne(t *testing.T) {
	m := menuMachine(t)
	ctx, cancel := rebootCtx(m)
	defer cancel()
	if err := m.job(Spec{Action: ActionReboot, Reboot: RebootIfRequired}, "").Do(ctx); err != nil {
		t.Fatalf("everything is in place: %v", err)
	}

	// The module is missing for the kernel in the middle (and for an older one, which does not count).
	m = menuMachine(t)
	delete(m.sizes, "/lib/modules/"+newKernel+"/updates/dkms/amneziawg.ko.zst")
	delete(m.sizes, "/lib/modules/7.0.0-36-generic/updates/dkms/amneziawg.ko.zst")
	err := m.job(Spec{Action: ActionReboot, Reboot: RebootIfRequired}, statusPath(t)).Do(context.Background())
	f := asFailure(t, err)
	if f.Code != CodeAwgModuleMissing || !strings.Contains(f.Detail, newKernel) || strings.Contains(f.Detail, "7.0.0-36") {
		t.Fatalf("failure = %+v", f)
	}
	noReboot(t, m)

	// Boot files missing for the running kernel, which GRUB falls back to; none for the older one.
	m = menuMachine(t)
	delete(m.sizes, "/boot/initrd.img-"+oldKernel)
	delete(m.sizes, "/boot/vmlinuz-7.0.0-36-generic")
	err = m.job(Spec{Action: ActionReboot, Reboot: RebootIfRequired}, statusPath(t)).Do(context.Background())
	f = asFailure(t, err)
	if f.Code != CodeInitramfsMissing || !strings.Contains(f.Detail, oldKernel) || strings.Contains(f.Detail, "7.0.0-36") {
		t.Fatalf("failure = %+v", f)
	}
	noReboot(t, m)
}

func TestGrubDefaultsThatMakeTheNextBootUnknownRefuseTheReboot(t *testing.T) {
	cases := map[string]func(*fakeMachine){
		"GRUB_DEFAULT=saved":          func(m *fakeMachine) { m.files["/etc/default/grub"] = "GRUB_DEFAULT=saved\n" },
		"GRUB_DEFAULT=1>2":            func(m *fakeMachine) { m.files["/etc/default/grub"] = "GRUB_DEFAULT=\"1>2\"\n" },
		"GRUB_DEFAULT=1 with comment": func(m *fakeMachine) { m.files["/etc/default/grub"] = "GRUB_DEFAULT=1 # second entry\n" },
		"drop-in overrides to saved": func(m *fakeMachine) {
			m.files["/etc/default/grub.d/90-custom.cfg"] = "GRUB_DEFAULT=saved\n"
		},
		"drop-in sorted after another one": func(m *fakeMachine) {
			m.files["/etc/default/grub.d/10-a.cfg"] = "GRUB_DEFAULT=saved\n"
			m.files["/etc/default/grub.d/20-b.cfg"] = "GRUB_DEFAULT=2\n"
		},
		"saved_entry": func(m *fakeMachine) { m.ok("grub-editenv list", "saved_entry=Ubuntu, with Linux 7.0.0-30-generic\n") },
		"next_entry":  func(m *fakeMachine) { m.ok("grub-editenv list", "next_entry=1\n") },
		"grub-editenv fails": func(m *fakeMachine) {
			m.fail("grub-editenv list", "grub-editenv: error: cannot open /boot/grub/grubenv\n")
		},
		"no grub.cfg": func(m *fakeMachine) { delete(m.sizes, "/boot/grub/grub.cfg") },
	}
	for name, set := range cases {
		t.Run(name, func(t *testing.T) {
			m := menuMachine(t)
			set(m)
			p := statusPath(t)
			err := m.job(Spec{Action: ActionReboot, Reboot: RebootIfRequired}, p).Do(context.Background())
			f := asFailure(t, err)
			if f.Code != CodeBootDefaultCustom {
				t.Fatalf("failure = %+v, want boot_default_custom", f)
			}
			noReboot(t, m)
			if st := mustStatus(t, p); st.Reboot != RebootHeld || st.HeldReason != CodeBootDefaultCustom {
				t.Fatalf("status = %+v", st)
			}
		})
	}
	// What is fine: 0 in any spelling, a commented line, a later drop-in that puts it back, an empty grubenv, a container.
	fine := map[string]func(*fakeMachine){
		"quoted 0":       func(m *fakeMachine) { m.files["/etc/default/grub"] = "GRUB_DEFAULT=\"0\"\n" },
		"single quoted":  func(m *fakeMachine) { m.files["/etc/default/grub"] = "GRUB_DEFAULT='0'\n" },
		"commented out":  func(m *fakeMachine) { m.files["/etc/default/grub"] = "#GRUB_DEFAULT=saved\n" },
		"not set at all": func(m *fakeMachine) { m.files["/etc/default/grub"] = "GRUB_TIMEOUT=5\n" },
		"drop-in puts it back": func(m *fakeMachine) {
			m.files["/etc/default/grub"] = "GRUB_DEFAULT=saved\n"
			m.files["/etc/default/grub.d/99.cfg"] = "GRUB_DEFAULT=0\n"
		},
		"no grubenv": func(m *fakeMachine) {
			delete(m.sizes, "/boot/grub/grubenv")
			m.fail("grub-editenv list", "grub-editenv: error: cannot open /boot/grub/grubenv\n")
		},
		"container": func(m *fakeMachine) {
			m.files["/run/systemd/container"] = "lxc\n"
			m.files["/etc/default/grub"] = "GRUB_DEFAULT=saved\n"
			delete(m.sizes, "/boot/grub/grub.cfg")
		},
	}
	for name, set := range fine {
		t.Run("fine: "+name, func(t *testing.T) {
			m := menuMachine(t)
			set(m)
			ctx, cancel := rebootCtx(m)
			defer cancel()
			if err := m.job(Spec{Action: ActionReboot, Reboot: RebootAlways}, "").Do(ctx); err != nil {
				t.Fatalf("a boot setup that is fine was refused: %v", err)
			}
		})
	}
}

func TestGrubDefaultIn(t *testing.T) {
	for in, want := range map[string]string{
		"GRUB_DEFAULT=0":                      "0",
		"  export GRUB_DEFAULT=\"saved\"":     "saved",
		"GRUB_DEFAULT='1>2' # note":           "1>2",
		"GRUB_DEFAULT=0;GRUB_TIMEOUT=3":       "0",
		"A=1\nGRUB_DEFAULT=2\nGRUB_DEFAULT=3": "3",
		"GRUB_DEFAULT=":                       "",
	} {
		if got, ok := grubDefaultIn(in); !ok || got != want {
			t.Errorf("grubDefaultIn(%q) = %q %v, want %q", in, got, ok, want)
		}
	}
	if _, ok := grubDefaultIn("#GRUB_DEFAULT=3\nGRUB_TIMEOUT=1"); ok {
		t.Error("a commented line is not an assignment")
	}
}

// ---------------------------------------------------------------- the reboot history of a panel job

func TestPanelJobsRebootAtMostOncePerHourAndThreeTimesPerDay(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), RebootsFileName)
	run := func(m *fakeMachine, id string) error {
		ctx, cancel := rebootCtx(m)
		defer cancel()
		j := m.job(Spec{JobID: id, Action: ActionReboot, Reboot: RebootAlways}, statusPath(t))
		j.RebootLogPath = logPath
		return j.Do(ctx)
	}
	mk := func(at time.Time) *fakeMachine {
		m := menuMachine(t)
		m.clock = at
		return m
	}
	t0 := time.Unix(1_800_000_000, 0)
	if err := run(mk(t0), "osj_r1"); err != nil {
		t.Fatalf("the first reboot: %v", err)
	}
	// Half an hour later: refused, nothing rebooted.
	m := mk(t0.Add(30 * time.Minute))
	err := run(m, "osj_r2")
	if f := asFailure(t, err); f.Code != CodeRebootLimit || !strings.Contains(f.Detail, "hour") {
		t.Fatalf("within the hour: %v", err)
	}
	noReboot(t, m)
	// A manual job from the command line is not limited, and does not count.
	if err := run(mk(t0.Add(30*time.Minute)), "manual-1"); err != nil {
		t.Fatalf("a manual job: %v", err)
	}
	// An hour later: allowed (2nd in the day), then 3rd; the 4th within 24 hours is refused.
	if err := run(mk(t0.Add(61*time.Minute)), "osj_r3"); err != nil {
		t.Fatalf("an hour later: %v", err)
	}
	if err := run(mk(t0.Add(2*time.Hour+5*time.Minute)), "osj_r4"); err != nil {
		t.Fatalf("the third of the day: %v", err)
	}
	m = mk(t0.Add(4 * time.Hour))
	if f := asFailure(t, run(m, "osj_r5")); f.Code != CodeRebootLimit || !strings.Contains(f.Detail, "24 hours") {
		t.Fatalf("the fourth within a day: %+v", f)
	}
	noReboot(t, m)
	// A day after the first one, the oldest has aged out.
	if err := run(mk(t0.Add(24*time.Hour+10*time.Minute)), "osj_r6"); err != nil {
		t.Fatalf("a day later: %v", err)
	}
	b, _ := os.ReadFile(logPath)
	if strings.Count(string(b), ",") > 2 {
		t.Fatalf("the history keeps more than a day: %s", b)
	}
}

func TestRebootHistoryThatCannotBeTrustedStopsAPanelJobOnly(t *testing.T) {
	t0 := time.Unix(1_800_000_000, 0)
	for name, content := range map[string]string{
		"damaged":            "{not json",
		"ahead of the clock": `{"reboots":[` + "1800003600" + `]}`, // an hour in the future
	} {
		t.Run(name, func(t *testing.T) {
			m := menuMachine(t)
			m.clock = t0
			logPath := filepath.Join(t.TempDir(), RebootsFileName)
			if err := os.WriteFile(logPath, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			j := m.job(Spec{JobID: "osj_x1", Action: ActionReboot, Reboot: RebootAlways}, statusPath(t))
			j.RebootLogPath = logPath
			f := asFailure(t, j.Do(context.Background()))
			if f.Code != CodeRebootLimit || !strings.Contains(f.Detail, "not done") {
				t.Fatalf("failure = %+v", f)
			}
			noReboot(t, m)

			// The same history does not stop a job of the command line.
			m = menuMachine(t)
			m.clock = t0
			ctx, cancel := rebootCtx(m)
			defer cancel()
			j = m.job(Spec{JobID: "manual-7", Action: ActionReboot, Reboot: RebootAlways}, statusPath(t))
			j.RebootLogPath = logPath
			if err := j.Do(ctx); err != nil {
				t.Fatalf("a manual job: %v", err)
			}
		})
	}
}

func TestRebootThatCannotBeRecordedIsNotDone(t *testing.T) {
	m := menuMachine(t)
	blocker := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The history is "under" a regular file: it is read as absent (a panel job passes the check) and cannot be written.
	j := m.job(Spec{JobID: "osj_w1", Action: ActionReboot, Reboot: RebootAlways}, statusPath(t))
	j.RebootLogPath = filepath.Join(blocker, RebootsFileName)
	err := j.Do(context.Background())
	if f := asFailure(t, err); f.Code != CodeRebootLimit {
		t.Fatalf("err = %v", err)
	}
	noReboot(t, m)
}

func TestPanelJobWithoutAPlaceForTheHistoryDoesNotReboot(t *testing.T) {
	m := menuMachine(t)
	j := m.job(Spec{JobID: "osj_n1", Action: ActionReboot, Reboot: RebootAlways}, "")
	j.RebootLogPath = ""
	if f := asFailure(t, j.Do(context.Background())); f.Code != CodeRebootLimit {
		t.Fatalf("failure = %+v", f)
	}
	noReboot(t, m)
}

// ---------------------------------------------------------------- the container, and modinfo

func TestContainerIsDetectedWithSystemdDetectVirtAndOpenVZ(t *testing.T) {
	m := newMachine(t)
	m.ok("systemd-detect-virt --container", "lxc\n")
	m.files["/run/reboot-required"] = "x"
	m.kernelFail = true // inside a container the kernel is not asked about
	ctx, cancel := rebootCtx(m)
	defer cancel()
	if err := m.job(Spec{Action: ActionReboot, Reboot: RebootIfRequired}, "").Do(ctx); err != nil {
		t.Fatalf("a container found by systemd-detect-virt: %v", err)
	}
	if m.ran("linux-version") {
		t.Fatalf("the kernel was asked about in a container: %v", m.calls)
	}

	m = newMachine(t) // OpenVZ: /proc/vz without /proc/bc
	m.sizes["/proc/vz"] = 1
	m.files["/run/reboot-required"] = "x"
	m.kernelFail = true
	ctx, cancel = rebootCtx(m)
	defer cancel()
	if err := m.job(Spec{Action: ActionReboot, Reboot: RebootIfRequired}, "").Do(ctx); err != nil {
		t.Fatalf("an OpenVZ guest: %v", err)
	}

	m = newMachine(t) // the host node of OpenVZ has /proc/bc as well: not a container
	m.sizes["/proc/vz"] = 1
	m.sizes["/proc/bc"] = 1
	m.files["/run/reboot-required"] = "x"
	m.kernelFail = true
	if f := asFailure(t, m.job(Spec{Action: ActionReboot, Reboot: RebootIfRequired}, "").Do(context.Background())); f.Code != CodeKernelUnknown {
		t.Fatalf("failure = %+v", f)
	}
}

// upgrade + if_required with nothing that needs a reboot and a linux-version that fails stays fail-closed: the reboot is not
// "not required" when it cannot be told.
func TestUnknownKernelStaysFailClosedForIfRequired(t *testing.T) {
	m := newMachine(t)
	m.kernelFail = true
	f := asFailure(t, m.job(Spec{Action: ActionUpgrade, Mode: ModeAll, Reboot: RebootIfRequired}, statusPath(t)).Do(context.Background()))
	if f.Code != CodeKernelUnknown {
		t.Fatalf("failure = %+v", f)
	}
	noReboot(t, m)
}

func TestModuleIsFoundWithModinfoBeforeThePathPatterns(t *testing.T) {
	r := &run{host: newMachine(t).host()}
	m := newMachine(t)
	r = &run{j: Job{}, host: m.host(), run: m.run, out: newJournal(nil)}
	ctx := context.Background()
	if r.moduleBuilt(ctx, newKernel) {
		t.Fatal("neither modinfo nor a path knows the module")
	}
	m.modinfo[newKernel] = true // depmod indexed it somewhere the patterns do not look
	if !r.moduleBuilt(ctx, newKernel) {
		t.Fatal("modinfo knows the module")
	}
	m.modinfo = map[string]bool{}
	m.sizes["/lib/modules/"+newKernel+"/updates/amneziawg.ko"] = 1 // depmod has not been run: the pattern is the fallback
	if !r.moduleBuilt(ctx, newKernel) {
		t.Fatal("the path pattern is the fallback")
	}
	if !m.ran("modinfo -k " + newKernel + " -F filename amneziawg") {
		t.Fatalf("modinfo was not asked first: %v", m.calls)
	}
}
