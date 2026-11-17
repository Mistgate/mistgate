package doctor

import (
	"context"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/node/awgprep"
	"github.com/mistgate/mistgate/internal/node/nftinstall"
	"github.com/mistgate/mistgate/internal/node/unit"
)

const nftJobUnit = "systemctl show --property=LoadState,ActiveState " + nftinstall.UnitName + ".service"

// nftHost is a Debian server (systemd, apt-get) without the nft command, the way a cloud image of Debian 13 comes.
func nftHost(t *testing.T) *fake {
	t.Helper()
	f := newFake(t)
	f.put("/etc/os-release", "ID=debian\nVERSION_ID=\"13\"\n")
	f.put("/run/systemd/system/.present", "")
	f.bin("apt-get")
	f.cmd(nftJobUnit, unitNotFound, nil)
	return f
}

func canInstallNft(e *Env) { e.LaunchInstallNftables = func(context.Context) error { return nil } }

func TestNftablesIsOKWhereNftIsInstalled(t *testing.T) {
	f := nftHost(t)
	f.bin("nft")
	r := run(t, f.doctor(canInstallNft), CheckNftables)
	if r.Status != OK || r.Code != CodeNftablesOn || r.FixID != "" {
		t.Fatalf("result = %+v", r)
	}
}

func TestNftablesMissingOffersTheFixOnlyWhereItCanRun(t *testing.T) {
	t.Run("debian with a launcher", func(t *testing.T) {
		r := run(t, nftHost(t).doctor(canInstallNft), CheckNftables)
		if r.Status != Warn || r.Code != CodeNftablesMissing || r.FixID != FixInstallNftables {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("an agent that cannot start the job", func(t *testing.T) {
		r := run(t, nftHost(t).doctor(), CheckNftables)
		if r.Status != Warn || r.Code != CodeNftablesMissing || r.FixID != "" {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("not a Debian or Ubuntu host", func(t *testing.T) {
		f := nftHost(t)
		f.put("/etc/os-release", "ID=fedora\n")
		r := run(t, f.doctor(canInstallNft), CheckNftables)
		if r.Status != Warn || r.Code != CodeNftablesMissing || r.FixID != "" {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("no apt-get", func(t *testing.T) {
		f := nftHost(t)
		delete(f.bins, "apt-get")
		r := run(t, f.doctor(canInstallNft), CheckNftables)
		if r.FixID != "" {
			t.Fatalf("a fix for a host without apt-get: %+v", r)
		}
	})
	t.Run("no systemd to run the unit", func(t *testing.T) {
		f := nftHost(t)
		f.remove("/run/systemd/system")
		r := run(t, f.doctor(canInstallNft), CheckNftables)
		if r.FixID != "" {
			t.Fatalf("a fix for a host without systemd: %+v", r)
		}
	})
}

func TestNftablesJobStates(t *testing.T) {
	withStatus := func(t *testing.T, state, active, reason string) Result {
		t.Helper()
		f := nftHost(t)
		f.cmd(nftJobUnit, active, nil)
		stateDir := filepath.Join(f.root, "state")
		if err := awgprep.WriteStatus(filepath.Join(stateDir, nftinstall.StatusFileName), awgprep.Status{State: state, Reason: reason}); err != nil {
			t.Fatal(err)
		}
		return run(t, f.doctor(func(e *Env) { e.StateDir = stateDir; canInstallNft(e) }), CheckNftables)
	}
	t.Run("the unit runs", func(t *testing.T) {
		r := withStatus(t, awgprep.StateFailed, unitActive, "installing nftables failed") // the file is still the last run's
		if r.Status != Warn || r.Code != CodeNftablesInstalling || r.FixID != "" {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("the last run failed", func(t *testing.T) {
		r := withStatus(t, awgprep.StateFailed, unitNotFound, "installing nftables failed")
		if r.Status != Warn || r.Code != CodeNftablesInstallFailed || r.FixID != FixInstallNftables || r.Params["reason"] != "installing nftables failed" {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("the last run was cut", func(t *testing.T) {
		r := withStatus(t, awgprep.StateRunning, unitNotFound, "")
		if r.Code != CodeNftablesInstallFailed || r.Params["reason"] != "the background task was interrupted before it finished" || r.FixID != FixInstallNftables {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("a finished run that left no nft is just missing again", func(t *testing.T) {
		r := withStatus(t, awgprep.StateDone, unitNotFound, "")
		if r.Code != CodeNftablesMissing || r.FixID != FixInstallNftables {
			t.Fatalf("result = %+v", r)
		}
	})
	t.Run("nft is there: the status file of an old run is ignored", func(t *testing.T) {
		f := nftHost(t)
		f.bin("nft")
		stateDir := filepath.Join(f.root, "state")
		if err := awgprep.WriteStatus(filepath.Join(stateDir, nftinstall.StatusFileName), awgprep.Status{State: awgprep.StateFailed, Reason: "x"}); err != nil {
			t.Fatal(err)
		}
		if r := run(t, f.doctor(func(e *Env) { e.StateDir = stateDir; canInstallNft(e) }), CheckNftables); r.Status != OK {
			t.Fatalf("result = %+v", r)
		}
	})
}

func TestInstallNftablesFix(t *testing.T) {
	apply := func(f *fake, dry bool, tweak ...func(*Env)) (FixOutcome, int) {
		launches := 0
		d := f.doctor(append([]func(*Env){func(e *Env) {
			e.StateDir = f.root
			e.LaunchInstallNftables = func(context.Context) error { launches++; return nil }
		}}, tweak...)...)
		return d.Apply(context.Background(), FixInstallNftables, dry, nil), launches
	}

	t.Run("a dry run says what would happen and starts nothing", func(t *testing.T) {
		out, launches := apply(nftHost(t), true)
		if !out.OK || out.Affected != 1 || out.Params["package"] != "nftables" || !strings.HasPrefix(out.Detail, "would ") || launches != 0 {
			t.Fatalf("plan = %+v, launches=%d", out, launches)
		}
	})
	t.Run("apply starts the job once and answers at once", func(t *testing.T) {
		f := nftHost(t)
		out, launches := apply(f, false)
		if !out.OK || out.Affected != 1 || out.Params["started"] != "1" || launches != 1 {
			t.Fatalf("apply = %+v, launches=%d", out, launches)
		}
		for _, c := range f.calls {
			if strings.Contains(c, "apt-get") {
				t.Fatalf("the agent ran apt itself, inside its sandbox: %s", c)
			}
		}
	})
	t.Run("nft is already there", func(t *testing.T) {
		f := nftHost(t)
		f.bin("nft")
		out, launches := apply(f, false)
		if !out.OK || out.Params["noop"] != "1" || launches != 0 {
			t.Fatalf("apply = %+v, launches=%d", out, launches)
		}
	})
	t.Run("a job already runs, or its unit is still loaded", func(t *testing.T) {
		for _, state := range []string{unitActive, unitLoaded} {
			f := nftHost(t)
			f.cmd(nftJobUnit, state, nil)
			if out, launches := apply(f, false); out.Err != ErrFixBusy || launches != 0 {
				t.Fatalf("%q: apply = %+v, launches=%d", state, out, launches)
			}
		}
	})
	t.Run("systemd-run finds the unit", func(t *testing.T) {
		out, _ := apply(nftHost(t), false, func(e *Env) { e.LaunchInstallNftables = func(context.Context) error { return unit.ErrBusy } })
		if out.Err != ErrFixBusy {
			t.Fatalf("apply = %+v", out)
		}
	})
	t.Run("an agent that cannot launch, and a host that is not apt", func(t *testing.T) {
		f := nftHost(t)
		d := f.doctor(func(e *Env) { e.LaunchInstallNftables = nil })
		if out := d.Apply(context.Background(), FixInstallNftables, false, nil); out.Err != ErrUnsupportedHost {
			t.Fatalf("no launcher: %+v", out)
		}
		f = nftHost(t)
		f.put("/etc/os-release", "ID=alpine\n")
		if out, _ := apply(f, false); out.Err != ErrNotApplicable {
			t.Fatalf("not apt: %+v", out)
		}
	})
	t.Run("parameters are refused", func(t *testing.T) {
		d := nftHost(t).doctor(canInstallNft)
		if out := d.Apply(context.Background(), FixInstallNftables, false, map[string]string{"package": "ncat"}); out.Err != ErrBadParams {
			t.Fatalf("apply = %+v", out)
		}
	})
}
