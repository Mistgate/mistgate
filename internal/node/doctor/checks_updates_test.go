package doctor

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/mistgate/mistgate/internal/node/awgprep"
	"github.com/mistgate/mistgate/internal/node/secupdates"
)

const (
	securityPackageQuery = "dpkg-query -W -f=${Status} unattended-upgrades"
	securityAPTConfig    = "apt-config " + secupdates.AptPeriodicArgs
	securityTimers       = "systemctl is-enabled apt-daily.timer apt-daily-upgrade.timer"
	securityTimersActive = "systemctl is-active apt-daily.timer apt-daily-upgrade.timer"
	securityJobUnit      = "systemctl show --property=LoadState,ActiveState " + secupdates.UnitName + ".service"
)

// canFix gives the doctor a launcher: the check offers the fix only to an agent that can start the job.
func canFix(e *Env) { e.LaunchSecurityUpdates = func(context.Context) error { return nil } }

// What systemctl show prints for the job's unit: a unit that does not exist is not-found and inactive (never "").
const (
	unitNotFound = "LoadState=not-found\nActiveState=inactive\n"
	unitActive   = "ActiveState=active\nLoadState=loaded\n"
	unitLoaded   = "LoadState=loaded\nActiveState=inactive\n"
)

func securityUpdatesFixture(f *fake) {
	f.put("/etc/os-release", "ID=ubuntu\n")
	f.put("/run/systemd/system/.present", "")
	f.bin("apt-config")
	f.cmd(securityPackageQuery, "install ok installed", nil)
	f.cmd(securityAPTConfig, "E='1'; L='1'; U='1';", nil)
	f.cmd(securityTimers, "enabled\nenabled\n", nil)
	f.cmd(securityTimersActive, "active\nactive\n", nil)
	f.cmd(securityJobUnit, unitNotFound, nil)
}

func TestSecurityUpdatesCheck(t *testing.T) {
	t.Run("on for Ubuntu with apt settings enabled", func(t *testing.T) {
		f := newFake(t)
		securityUpdatesFixture(f)
		r := run(t, f.doctor(), CheckSecurityUpdates)
		if r.Status != OK || r.Code != CodeSecurityUpdatesOn || r.FixID != "" {
			t.Fatalf("enabled result = %+v", r)
		}
		if f.called("apt-get") != 0 || f.called("unattended-upgrade") != 0 {
			t.Fatalf("check ran a package-changing command: %v", f.calls)
		}
	})

	t.Run("recognizes an ID_LIKE Debian derivative", func(t *testing.T) {
		f := newFake(t)
		securityUpdatesFixture(f)
		f.put("/etc/os-release", "ID=linuxmint\nID_LIKE=ubuntu debian\n")
		r := run(t, f.doctor(), CheckSecurityUpdates)
		if r.Status != OK || r.Code != CodeSecurityUpdatesOn {
			t.Fatalf("ID_LIKE result = %+v", r)
		}
	})

	t.Run("not apt distro", func(t *testing.T) {
		f := newFake(t)
		f.put("/etc/os-release", "ID=fedora\nID_LIKE=rhel\n")
		f.bin("apt-config")
		r := run(t, f.doctor(), CheckSecurityUpdates)
		if r.Status != Skip || r.Code != CodeSecurityUpdatesNotAPT {
			t.Fatalf("non-apt result = %+v", r)
		}
		if len(f.calls) != 0 {
			t.Fatalf("ran commands on non-apt host: %v", f.calls)
		}
	})

	t.Run("apt-config missing", func(t *testing.T) {
		f := newFake(t)
		f.put("/etc/os-release", "ID=debian\n")
		r := run(t, f.doctor(), CheckSecurityUpdates)
		if r.Status != Skip || r.Code != CodeSecurityUpdatesNotAPT {
			t.Fatalf("missing apt-config result = %+v", r)
		}
		if len(f.calls) != 0 {
			t.Fatalf("ran commands without apt-config: %v", f.calls)
		}
	})

	t.Run("no systemd", func(t *testing.T) {
		f := newFake(t)
		f.put("/etc/os-release", "ID=debian\n")
		f.bin("apt-config")
		r := run(t, f.doctor(), CheckSecurityUpdates)
		if r.Status != Skip || r.Code != CodeSecurityUpdatesNoSystemd {
			t.Fatalf("no systemd result = %+v", r)
		}
		if len(f.calls) != 0 {
			t.Fatalf("ran commands without systemd: %v", f.calls)
		}
	})

	t.Run("package not installed", func(t *testing.T) {
		f := newFake(t)
		securityUpdatesFixture(f)
		f.cmd(securityPackageQuery, "deinstall ok config-files", nil)
		r := run(t, f.doctor(canFix), CheckSecurityUpdates)
		if r.Status != Warn || r.Code != CodeSecurityUpdatesNotInstalled || r.FixID != FixEnableSecurityUpdates {
			t.Fatalf("missing package result = %+v", r)
		}
	})

	t.Run("no fix offered by an agent that cannot launch the job", func(t *testing.T) {
		f := newFake(t)
		securityUpdatesFixture(f)
		f.cmd(securityPackageQuery, "deinstall ok config-files", nil)
		r := run(t, f.doctor(), CheckSecurityUpdates)
		if r.Status != Warn || r.Code != CodeSecurityUpdatesNotInstalled || r.FixID != "" {
			t.Fatalf("result without a launcher = %+v, want the warning without a fix", r)
		}
	})

	t.Run("an enabled timer that is stopped is off", func(t *testing.T) {
		f := newFake(t)
		securityUpdatesFixture(f)
		f.cmd(securityTimersActive, "active\ninactive\n", errors.New("exit status 3"))
		r := run(t, f.doctor(canFix), CheckSecurityUpdates)
		if r.Status != Warn || r.Code != CodeSecurityUpdatesOff || r.Params["off"] != "apt-daily-upgrade.timer inactive" || r.FixID != FixEnableSecurityUpdates {
			t.Fatalf("stopped timer result = %+v", r)
		}
	})

	t.Run("periodic settings and timers off", func(t *testing.T) {
		f := newFake(t)
		securityUpdatesFixture(f)
		f.cmd(securityAPTConfig, "E='0'; L=''; U='0';", nil)
		f.cmd(securityTimers, "enabled\nmasked\n", errors.New("systemctl reports a disabled timer"))
		f.cmd(securityTimersActive, "active\ninactive\n", errors.New("exit status 3"))
		r := run(t, f.doctor(canFix), CheckSecurityUpdates)
		wantOff := `APT::Periodic::Enable "0", APT::Periodic::Update-Package-Lists "", APT::Periodic::Unattended-Upgrade "0", apt-daily-upgrade.timer masked`
		if r.Status != Warn || r.Code != CodeSecurityUpdatesOff || r.Params["off"] != wantOff || r.FixID != FixEnableSecurityUpdates {
			t.Fatalf("off result = %+v, want off=%q", r, wantOff)
		}
		if f.called("apt-get") != 0 || f.called("unattended-upgrade") != 0 {
			t.Fatalf("check ran a package-changing command: %v", f.calls)
		}
	})

	t.Run("disabled timer", func(t *testing.T) {
		f := newFake(t)
		securityUpdatesFixture(f)
		f.cmd(securityTimers, "disabled\nenabled\n", errors.New("systemctl reports a disabled timer"))
		r := run(t, f.doctor(), CheckSecurityUpdates)
		if r.Status != Warn || r.Code != CodeSecurityUpdatesOff || r.Params["off"] != "apt-daily.timer disabled" {
			t.Fatalf("disabled timer result = %+v", r)
		}
	})

	t.Run("always is enabled and missing Enable uses apt default", func(t *testing.T) {
		f := newFake(t)
		securityUpdatesFixture(f)
		f.cmd(securityAPTConfig, "L='always'; U='1';", nil)
		r := run(t, f.doctor(), CheckSecurityUpdates)
		if r.Status != OK || r.Code != CodeSecurityUpdatesOn {
			t.Fatalf("always result = %+v", r)
		}
	})
}

func TestSecurityUpdatesJobStates(t *testing.T) {
	const updatesOn, updatesOff = "E='1'; L='1'; U='1';", "E='0'; L='0'; U='0';"
	withStatus := func(t *testing.T, state, active, reason, apt string) (*fake, Result) {
		t.Helper()
		f := newFake(t)
		securityUpdatesFixture(f)
		f.cmd(securityAPTConfig, apt, nil)
		f.cmd(securityJobUnit, active, nil)
		stateDir := filepath.Join(f.root, "state")
		if err := awgprep.WriteStatus(filepath.Join(stateDir, secupdates.StatusFileName), awgprep.Status{State: state, Reason: reason}); err != nil {
			t.Fatal(err)
		}
		d := f.doctor(func(e *Env) { e.StateDir = stateDir; canFix(e) })
		return f, run(t, d, CheckSecurityUpdates)
	}

	t.Run("running and active", func(t *testing.T) {
		_, r := withStatus(t, awgprep.StateRunning, unitActive, "", updatesOff)
		if r.Status != Warn || r.Code != CodeSecurityUpdatesEnabling || r.FixID != "" {
			t.Fatalf("active job result = %+v", r)
		}
	})

	t.Run("the unit runs while the file still holds the last failure", func(t *testing.T) {
		_, r := withStatus(t, awgprep.StateFailed, unitActive, "installing unattended-upgrades failed", updatesOff)
		if r.Status != Warn || r.Code != CodeSecurityUpdatesEnabling || r.FixID != "" {
			t.Fatalf("just-launched job result = %+v", r)
		}
	})

	t.Run("running and inactive is interrupted but fixable", func(t *testing.T) {
		_, r := withStatus(t, awgprep.StateRunning, unitNotFound, "", updatesOff)
		if r.Status != Warn || r.Code != CodeSecurityUpdatesEnableFailed || r.FixID != FixEnableSecurityUpdates ||
			r.Params["reason"] != "the background task was interrupted before it finished" {
			t.Fatalf("interrupted job result = %+v", r)
		}
	})

	t.Run("a job that stopped after its last step leaves the updates on", func(t *testing.T) {
		_, r := withStatus(t, awgprep.StateRunning, unitNotFound, "", updatesOn)
		if r.Status != OK || r.Code != CodeSecurityUpdatesOn || r.FixID != "" {
			t.Fatalf("stale running file with the updates on = %+v, want OK", r)
		}
	})

	t.Run("failed and still off", func(t *testing.T) {
		f := newFake(t)
		securityUpdatesFixture(f)
		f.cmd(securityAPTConfig, "E='0'; L='0'; U='0';", nil)
		stateDir := filepath.Join(f.root, "state")
		if err := awgprep.WriteStatus(filepath.Join(stateDir, secupdates.StatusFileName), awgprep.Status{
			State: awgprep.StateFailed, Reason: "installing unattended-upgrades failed",
		}); err != nil {
			t.Fatal(err)
		}
		r := run(t, f.doctor(func(e *Env) { e.StateDir = stateDir; canFix(e) }), CheckSecurityUpdates)
		if r.Status != Warn || r.Code != CodeSecurityUpdatesEnableFailed || r.FixID != FixEnableSecurityUpdates ||
			r.Params["reason"] != "installing unattended-upgrades failed" {
			t.Fatalf("failed job result = %+v", r)
		}
	})

	t.Run("done and enabled", func(t *testing.T) {
		_, r := withStatus(t, awgprep.StateDone, unitNotFound, "", updatesOn)
		if r.Status != OK || r.Code != CodeSecurityUpdatesOn || r.FixID != "" {
			t.Fatalf("completed job result = %+v", r)
		}
	})
}
