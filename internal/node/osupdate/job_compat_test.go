package osupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

// The module's source (compat.h) is repaired when it is a file Mistgate knows to break the build, before a build the job
// starts and once more after a build failed; a file it does not know is left as it is, the build failure is the one of
// before and says which file it saw.

const (
	compatFile      = "/usr/src/amneziawg-1.0.0/compat/compat.h"
	compatFixedHash = "e0a3a71e2ae4c8a5d65287af88d02e5f0563c52f0854ee760c2405934d32d590"
)

func fixtureBody(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("../awgprep/testdata/compat/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func hashOf(s string) string { h := sha256.Sum256([]byte(s)); return hex.EncodeToString(h[:]) }

func sourceFixed(m *fakeMachine) bool { return hashOf(m.files[compatFile]) == compatFixedHash }

func (m *fakeMachine) count(cmd string) (n int) {
	for _, c := range m.calls {
		if c == cmd {
			n++
		}
	}
	return n
}

// A source that is bad before the job starts is repaired before dpkg builds the module: the configure succeeds at once.
func TestRepairPatchesAKnownSourceBeforeTheBuild(t *testing.T) {
	m := ownerMachine(t)
	m.files[compatFile] = fixtureBody(t, "ppa-1.0.0.h")
	m.on(configureAll, func() (string, error) { // the build only works on a repaired source
		if !sourceFixed(m) {
			return realDkmsConfigureOut, errExit
		}
		healthy(m)
		return "", nil
	})
	p := statusPath(t)
	if err := m.job(Spec{Action: ActionUpgrade, Mode: ModeSecurity, Reboot: RebootNever}, p).Do(context.Background()); err != nil {
		t.Fatalf("err = %v, calls %v", err, m.calls)
	}
	if !sourceFixed(m) || m.count(configureAll) != 1 || ranPurge(m) {
		t.Fatalf("source fixed = %v, calls %v", sourceFixed(m), m.calls)
	}
	if st := mustStatus(t, p); st.State != StateDone || st.AwgSource != "patched" {
		t.Fatalf("status = %+v", st)
	}
}

// The repair fails in the module's build because a package just unpacked a bad source: the source is repaired and the
// repair goes once more.
func TestRepairRetriesOnceAfterRepairingTheSource(t *testing.T) {
	m := ownerMachine(t)
	m.on(configureAll, func() (string, error) {
		if !sourceFixed(m) {
			m.files[compatFile] = fixtureBody(t, "ppa-1.0.0.h") // unpacked by the package, its postinst then fails
			return realDkmsConfigureOut, errExit
		}
		healthy(m)
		return "", nil
	})
	p := statusPath(t)
	if err := m.job(Spec{Action: ActionUpgrade, Mode: ModeSecurity, Reboot: RebootNever}, p).Do(context.Background()); err != nil {
		t.Fatalf("err = %v, calls %v", err, m.calls)
	}
	if m.count(configureAll) != 2 || ranPurge(m) {
		t.Fatalf("calls %v", m.calls)
	}
	if st := mustStatus(t, p); st.State != StateDone || st.AwgSource != "patched" {
		t.Fatalf("status = %+v", st)
	}
}

// The upgrade itself fails in the module's build (the new kernel's postinst): repaired, dpkg finishes, the job goes on.
func TestInstallFailureOnAKnownSourceIsRepairedAndTheJobGoesOn(t *testing.T) {
	m := ownerMachine(t)
	m.setDpkg(cleanTable)
	m.on(allUpgrade, func() (string, error) {
		m.files[compatFile] = fixtureBody(t, "handfixed.h")
		m.setDpkg(brokenTable)
		return realDkmsConfigureOut, errExit
	})
	m.on(configureAll, func() (string, error) {
		if !sourceFixed(m) {
			return realDkmsConfigureOut, errExit
		}
		healthy(m)
		return "", nil
	})
	p := statusPath(t)
	if err := m.job(Spec{Action: ActionUpgrade, Mode: ModeAll, Reboot: RebootNever}, p).Do(context.Background()); err != nil {
		t.Fatalf("err = %v, calls %v", err, m.calls)
	}
	if m.count(allUpgrade) != 1 || m.count(configureAll) != 1 || ranPurge(m) {
		t.Fatalf("calls %v", m.calls)
	}
	if st := mustStatus(t, p); st.State != StateDone || st.AwgSource != "patched" {
		t.Fatalf("status = %+v", st)
	}
}

// The module step: dkms fails on a bad source that appeared, the source is repaired and dkms is asked once more.
func TestModuleStepRepairsTheSourceAndBuildsAgain(t *testing.T) {
	m := ownerMachine(t)
	m.setDpkg(cleanTable)
	m.on(dkmsAuto, func() (string, error) {
		if !sourceFixed(m) {
			m.files[compatFile] = fixtureBody(t, "ppa-1.0.0.h")
			return "Error! Bad return status for module build on kernel: " + newKernel + " (x86_64)\nConsult /var/lib/dkms/amneziawg/1.0.0/build/make.log for more information.\n", errExit
		}
		healthy(m)
		return "", nil
	})
	p := statusPath(t)
	if err := m.job(Spec{Action: ActionUpgrade, Mode: ModeSecurity, Reboot: RebootNever}, p).Do(context.Background()); err != nil {
		t.Fatalf("err = %v, calls %v", err, m.calls)
	}
	if m.count(dkmsAuto) != 2 || ranPurge(m) {
		t.Fatalf("calls %v", m.calls)
	}
}

// A file Mistgate does not know is left as it is: the failure is the one of before, with the start of the hash in the reason.
func TestUnknownSourceIsLeftAndNamedInTheBuildFailure(t *testing.T) {
	m := ownerMachine(t)
	realRepair(m)
	body := fixtureBody(t, "ppa-1.0.0.h") + "/* local */\n"
	m.files[compatFile] = body
	p := statusPath(t)
	err := m.job(Spec{Action: ActionUpgrade, Mode: ModeSecurity, Reboot: RebootIfRequired}, p).Do(context.Background())
	f := asFailure(t, err)
	sha := hashOf(body)[:12]
	if f.Code != CodeAwgBuildFailed || !strings.Contains(f.Detail, "not one Mistgate knows (sha256 "+sha+"); nothing was changed") {
		t.Fatalf("failure = %+v", f)
	}
	if m.files[compatFile] != body || m.count(configureAll) != 1 || ranPurge(m) {
		t.Fatalf("source untouched = %v, calls %v", m.files[compatFile] == body, m.calls)
	}
	noReboot(t, m)
	if st := mustStatus(t, p); st.Code != CodeAwgBuildFailed || st.AwgSource != "unknown:"+sha {
		t.Fatalf("status = %+v", st)
	}
}

// A repaired source whose build still fails is the build failure of before, and does not blame the source.
func TestBuildFailureAfterTheRepairIsTheSameFailure(t *testing.T) {
	m := ownerMachine(t)
	realRepair(m)
	m.files[compatFile] = fixtureBody(t, "ppa-1.0.0.h")
	err := m.job(Spec{Action: ActionUpgrade, Mode: ModeSecurity, Reboot: RebootNever}, statusPath(t)).Do(context.Background())
	f := asFailure(t, err)
	if f.Code != CodeAwgBuildFailed || strings.Contains(f.Detail, "Mistgate knows") || !sourceFixed(m) || ranPurge(m) {
		t.Fatalf("failure = %+v, calls %v", f, m.calls)
	}
}

// A source that cannot be written leaves the failure as it was.
func TestSourceThatCannotBeWrittenLeavesTheBuildFailure(t *testing.T) {
	m := ownerMachine(t)
	realRepair(m)
	body := fixtureBody(t, "ppa-1.0.0.h")
	m.files[compatFile] = body
	m.failWriteFile = true
	err := m.job(Spec{Action: ActionUpgrade, Mode: ModeSecurity, Reboot: RebootNever}, statusPath(t)).Do(context.Background())
	if f := asFailure(t, err); f.Code != CodeAwgBuildFailed || m.files[compatFile] != body || m.count(configureAll) != 1 {
		t.Fatalf("failure = %+v, calls %v", f, m.calls)
	}
}
