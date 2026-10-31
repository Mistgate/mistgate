package doctor

import (
	"errors"
	"testing"

	"github.com/mistgate/mistgate/internal/node/secupdates"
)

const (
	securityPackageQuery = "dpkg-query -W -f=${Status} unattended-upgrades"
	securityAPTConfig    = "apt-config " + secupdates.AptPeriodicArgs
	securityTimers       = "systemctl is-enabled apt-daily.timer apt-daily-upgrade.timer"
)

func securityUpdatesFixture(f *fake) {
	f.put("/etc/os-release", "ID=ubuntu\n")
	f.put("/run/systemd/system/.present", "")
	f.bin("apt-config")
	f.cmd(securityPackageQuery, "install ok installed", nil)
	f.cmd(securityAPTConfig, "E='1'; L='1'; U='1';", nil)
	f.cmd(securityTimers, "enabled\nenabled\n", nil)
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
		r := run(t, f.doctor(), CheckSecurityUpdates)
		if r.Status != Warn || r.Code != CodeSecurityUpdatesNotInstalled || r.FixID != "" {
			t.Fatalf("missing package result = %+v", r)
		}
	})

	t.Run("periodic settings and timers off", func(t *testing.T) {
		f := newFake(t)
		securityUpdatesFixture(f)
		f.cmd(securityAPTConfig, "E='0'; L=''; U='0';", nil)
		f.cmd(securityTimers, "enabled\nmasked\n", errors.New("systemctl reports a disabled timer"))
		r := run(t, f.doctor(), CheckSecurityUpdates)
		wantOff := `APT::Periodic::Enable "0", APT::Periodic::Update-Package-Lists "", APT::Periodic::Unattended-Upgrade "0", apt-daily-upgrade.timer masked`
		if r.Status != Warn || r.Code != CodeSecurityUpdatesOff || r.Params["off"] != wantOff || r.FixID != "" {
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
