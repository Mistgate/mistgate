package doctor

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mistgate/mistgate/internal/node/hostctl"
)

// fakeFixer records the calls that would change the host.
type fakeFixer struct {
	vacuum, set int
	plan        hostctl.ResolverPlan
	planErr     error
	setErr      error
	onVacuum    func()
	setTo       []string
}

func (f *fakeFixer) VacuumJournal(context.Context) error {
	f.vacuum++
	if f.onVacuum != nil {
		f.onVacuum()
	}
	return nil
}
func (f *fakeFixer) ResolverPlan(context.Context, []string) (hostctl.ResolverPlan, error) {
	return f.plan, f.planErr
}
func (f *fakeFixer) SetResolver(_ context.Context, s []string) error {
	f.set++
	f.setTo = s
	return f.setErr
}

// fixRig is a host that needs every fix, with recorders for everything that would change it.
type fixRig struct {
	f                 *fake
	fx                *fakeFixer
	baseline          int
	restarts          []string
	warpReconnections int
	warpRechecks      int
}

func newFixRig(t *testing.T) (*fixRig, func(...func(*Env)) *Doctor) {
	t.Helper()
	r := &fixRig{f: newFake(t), fx: &fakeFixer{plan: hostctl.ResolverPlan{Mode: "resolv_conf", Before: []string{"10.0.0.1"}, After: []string{"1.1.1.1", "8.8.8.8"}}}}
	f := r.f
	f.bin("journalctl")
	f.putSized("/var/log/journal/m/system.journal", 400<<20)
	f.put("/proc/sys/net/core/default_qdisc", "fq_codel\n")
	f.put("/proc/sys/net/ipv4/tcp_congestion_control", "cubic\n")
	f.put("/proc/sys/net/ipv4/tcp_available_congestion_control", "cubic bbr\n")
	f.put("/run/systemd/system/.keep", "")
	mk := func(tweak ...func(*Env)) *Doctor {
		return f.doctor(append([]func(*Env){func(e *Env) {
			e.Fixer = r.fx
			e.ApplyBaseline = func(context.Context) error {
				r.baseline++
				f.put("/proc/sys/net/core/default_qdisc", "fq\n")
				f.put("/proc/sys/net/ipv4/tcp_congestion_control", "bbr\n")
				f.put(hostctl.SysctlFilePath, hostctl.SysctlFileBody)
				f.put(hostctl.JournaldFilePath, hostctl.JournaldFileBody)
				return nil
			}
			e.RestartInbound = func(_ context.Context, id string) (uint32, error) { r.restarts = append(r.restarts, id); return 1, nil }
			e.Warp = func(context.Context) WarpInfo { return WarpInfo{Configured: true, State: "down"} }
			e.ReconnectWarp = func(context.Context) (bool, error) { r.warpReconnections++; return true, nil }
			e.RecheckWarp = func(context.Context) (bool, bool, error) { r.warpRechecks++; return true, false, nil }
			e.Inbounds = func() []Inbound {
				return []Inbound{
					{ID: "inb_1", Enabled: true, State: "failed", Network: "udp", Port: 443, Error: "address already in use"},
					{ID: "inb_2", Enabled: true, State: "running", Network: "udp", Port: 8443},
					{ID: "inb_3", Enabled: true, State: "failed", Network: "udp", Port: 9443, Error: "x"},
				}
			}
		}}, tweak...)...)
	}
	return r, mk
}

func TestDryRunChangesNothing(t *testing.T) {
	r, mk := newFixRig(t)
	d := mk()
	before := snapshot(t, r.f.root)
	for _, fix := range FixIDs() {
		params := map[string]string(nil)
		out := d.Apply(context.Background(), fix, true, params)
		if !out.OK || out.Err != "" || out.Detail == "" {
			t.Errorf("%s dry run: %+v", fix, out)
		}
		if out.Params["noop"] == "1" {
			t.Errorf("%s dry run found nothing to do on a host that needs it: %+v", fix, out)
		}
	}
	if r.fx.vacuum != 0 || r.fx.set != 0 || r.baseline != 0 || len(r.restarts) != 0 || r.warpReconnections != 0 {
		t.Fatalf("a dry run acted: vacuum=%d set=%d baseline=%d restarts=%v warp_reconnects=%d", r.fx.vacuum, r.fx.set, r.baseline, r.restarts, r.warpReconnections)
	}
	if after := snapshot(t, r.f.root); after != before {
		t.Fatalf("a dry run changed the filesystem:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	for _, c := range r.f.calls {
		t.Errorf("a dry run ran a command: %s", c)
	}
}

func TestDryRunFacts(t *testing.T) {
	_, mk := newFixRig(t)
	d := mk()
	out := d.Apply(context.Background(), FixJournaldVacuum, true, nil)
	if out.Params["before_mb"] != "400" || out.Params["target_mb"] != "200" {
		t.Errorf("journal plan: %+v", out)
	}
	out = d.Apply(context.Background(), FixApplyBaseline, true, nil)
	if out.Params["changes"] != "default_qdisc,tcp_congestion_control,sysctl_file,journald_file" {
		t.Errorf("baseline plan: %+v", out)
	}
	out = d.Apply(context.Background(), FixRestartInbound, true, nil)
	if out.Params["inbounds"] != "inb_1,inb_3" || out.Params["disruptive"] != "1" {
		t.Errorf("restart plan: %+v", out)
	}
	out = d.Apply(context.Background(), FixSetResolver, true, nil)
	if out.Params["mode"] != "resolv_conf" || out.Params["before"] != "10.0.0.1" || out.Params["after"] != "1.1.1.1,8.8.8.8" {
		t.Errorf("resolver plan: %+v", out)
	}
}

func TestReconnectWarpOnlyActsOnAStillDownTunnel(t *testing.T) {
	_, mk := newFixRig(t)
	d := mk()

	plan := d.Apply(context.Background(), FixReconnectWarp, true, nil)
	if !plan.OK || plan.Affected != 1 || plan.Params["state"] != "down" || plan.Detail == "" {
		t.Fatalf("plan: %+v", plan)
	}
	if out := d.Apply(context.Background(), FixReconnectWarp, true, map[string]string{"endpoint": "203.0.113.1"}); out.Err != ErrBadParams {
		t.Fatalf("unexpected params: %+v", out)
	}

	result := d.Apply(context.Background(), FixReconnectWarp, false, nil)
	if !result.OK || result.Affected != 1 || result.Params["state"] != "starting" || result.Detail == "" {
		t.Fatalf("reconnect: %+v", result)
	}

	_, recovered := newFixRig(t)
	noLongerDown := recovered(func(e *Env) {
		e.Warp = func(context.Context) WarpInfo { return WarpInfo{Configured: true, State: "up"} }
	})
	if out := noLongerDown.Apply(context.Background(), FixReconnectWarp, false, nil); !out.OK || out.Params["noop"] != "1" {
		t.Fatalf("stale fix should be a no-op: %+v", out)
	}
}

func TestReconnectWarpRetriesCurrentFailureWithoutRestartingAnUpTunnel(t *testing.T) {
	r, mk := newFixRig(t)
	d := mk(func(e *Env) {
		e.Warp = func(context.Context) WarpInfo {
			return WarpInfo{Configured: true, State: "up", LastError: "probe_other_failed"}
		}
	})

	plan := d.Apply(context.Background(), FixReconnectWarp, true, nil)
	if !plan.OK || plan.Affected != 1 || !strings.Contains(plan.Detail, "only if the existing failure threshold") || r.warpRechecks != 0 {
		t.Fatalf("recheck plan=%+v checks=%d", plan, r.warpRechecks)
	}
	out := d.Apply(context.Background(), FixReconnectWarp, false, nil)
	if !out.OK || out.Affected != 1 || out.Params["state"] != "up" || !strings.Contains(out.Detail, "below the automatic failure threshold") || r.warpRechecks != 1 || r.warpReconnections != 0 {
		t.Fatalf("recheck result=%+v checks=%d reconnects=%d", out, r.warpRechecks, r.warpReconnections)
	}

	noLongerFailing := mk(func(e *Env) {
		e.Warp = func(context.Context) WarpInfo { return WarpInfo{Configured: true, State: "up"} }
	})
	if out := noLongerFailing.Apply(context.Background(), FixReconnectWarp, false, nil); !out.OK || out.Params["noop"] != "1" || r.warpRechecks != 1 {
		t.Fatalf("stale recheck should be a no-op: %+v checks=%d", out, r.warpRechecks)
	}
}

func TestReconnectWarpRechecksThenReconnectsOnlyAfterFailureThreshold(t *testing.T) {
	r, mk := newFixRig(t)
	state := "up"
	d := mk(func(e *Env) {
		e.Warp = func(context.Context) WarpInfo {
			lastError := "probe_other_failed"
			if state == "down" || state == "starting" {
				lastError = "probe_other_failed; ladder: reassert"
			}
			return WarpInfo{Configured: true, State: state, LastError: lastError}
		}
		e.RecheckWarp = func(context.Context) (bool, bool, error) {
			r.warpRechecks++
			state = "down" // the recheck became the existing third consecutive failure
			return true, true, nil
		}
		e.ReconnectWarp = func(context.Context) (bool, error) {
			if state != "down" {
				t.Fatalf("guarded reconnect called while WARP state is %q", state)
			}
			r.warpReconnections++
			state = "starting"
			return true, nil
		}
	})

	plan := d.Apply(context.Background(), FixReconnectWarp, true, nil)
	if !plan.OK || !strings.Contains(plan.Detail, "only if the existing failure threshold") || r.warpRechecks != 0 || r.warpReconnections != 0 {
		t.Fatalf("threshold plan=%+v checks=%d reconnects=%d", plan, r.warpRechecks, r.warpReconnections)
	}
	out := d.Apply(context.Background(), FixReconnectWarp, false, nil)
	if !out.OK || out.Affected != 1 || out.Params["state"] != "starting" || !strings.Contains(out.Detail, "failure threshold") ||
		r.warpRechecks != 1 || r.warpReconnections != 1 || state != "starting" {
		t.Fatalf("threshold result=%+v checks=%d reconnects=%d state=%s", out, r.warpRechecks, r.warpReconnections, state)
	}
}

func TestReconnectWarpDoesNotReconnectDownStateWithoutThresholdSignal(t *testing.T) {
	r, mk := newFixRig(t)
	state := "up"
	d := mk(func(e *Env) {
		e.Warp = func(context.Context) WarpInfo {
			return WarpInfo{Configured: true, State: state, LastError: "probe_other_failed"}
		}
		e.RecheckWarp = func(context.Context) (bool, bool, error) {
			r.warpRechecks++
			state = "down"
			return true, false, nil
		}
	})

	out := d.Apply(context.Background(), FixReconnectWarp, false, nil)
	if !out.OK || out.Params["state"] != "down" || !strings.Contains(out.Detail, "did not reach the failure threshold") ||
		r.warpRechecks != 1 || r.warpReconnections != 0 {
		t.Fatalf("result=%+v checks=%d reconnects=%d", out, r.warpRechecks, r.warpReconnections)
	}
}

func TestReconnectWarpUsesScheduledThresholdReachedBeforeDoctorRecheck(t *testing.T) {
	r, mk := newFixRig(t)
	state := "up"
	d := mk(func(e *Env) {
		e.Warp = func(context.Context) WarpInfo {
			return WarpInfo{Configured: true, State: state, LastError: "probe_other_failed"}
		}
		e.RecheckWarp = func(context.Context) (bool, bool, error) {
			r.warpRechecks++
			state = "down" // the scheduled poll reached the threshold just before CheckNow
			return false, true, nil
		}
	})

	out := d.Apply(context.Background(), FixReconnectWarp, false, nil)
	if !out.OK || out.Params["state"] != "starting" || r.warpRechecks != 1 || r.warpReconnections != 1 {
		t.Fatalf("result=%+v checks=%d reconnects=%d", out, r.warpRechecks, r.warpReconnections)
	}
}

func TestApplyJournaldVacuum(t *testing.T) {
	r, mk := newFixRig(t)
	r.fx.onVacuum = func() { r.f.putSized("/var/log/journal/m/system.journal", 150<<20) }
	out := mk().Apply(context.Background(), FixJournaldVacuum, false, nil)
	if !out.OK || out.Affected != 1 || r.fx.vacuum != 1 || out.Params["before_mb"] != "400" || out.Params["after_mb"] != "150" {
		t.Fatalf("%+v vacuum=%d", out, r.fx.vacuum)
	}
	// Nothing left to do: ok, affected 0, noop, and no second vacuum.
	out = mk().Apply(context.Background(), FixJournaldVacuum, false, nil)
	if !out.OK || out.Affected != 0 || out.Params["noop"] != "1" || r.fx.vacuum != 1 {
		t.Fatalf("second apply: %+v vacuum=%d", out, r.fx.vacuum)
	}
	// No journalctl: the host cannot do it.
	delete(r.f.bins, "journalctl")
	if out = mk().Apply(context.Background(), FixJournaldVacuum, false, nil); out.Err != ErrNotApplicable {
		t.Fatalf("no journalctl: %+v", out)
	}
}

func TestApplyBaseline(t *testing.T) {
	r, mk := newFixRig(t)
	out := mk().Apply(context.Background(), FixApplyBaseline, false, nil)
	if !out.OK || out.Affected != 4 || r.baseline != 1 {
		t.Fatalf("%+v baseline=%d", out, r.baseline)
	}
	out = mk().Apply(context.Background(), FixApplyBaseline, false, nil)
	if !out.OK || out.Affected != 0 || out.Params["noop"] != "1" || r.baseline != 1 {
		t.Fatalf("second apply (idempotent): %+v baseline=%d", out, r.baseline)
	}
	// ApplyBaseline failing part-way reports what did get set.
	r2, mk2 := newFixRig(t)
	out = mk2(func(e *Env) {
		e.ApplyBaseline = func(context.Context) error {
			r2.f.put(hostctl.SysctlFilePath, hostctl.SysctlFileBody)
			return errors.New("ssh guard: nft is not installed")
		}
	}).Apply(context.Background(), FixApplyBaseline, false, nil)
	if out.OK || !strings.HasPrefix(out.Err, "failed: ssh guard") || out.Affected != 1 {
		t.Fatalf("partial failure: %+v", out)
	}
	// Only BBR-less kernels' cc is left: the host cannot do it.
	r3, mk3 := newFixRig(t)
	r3.f.put("/proc/sys/net/core/default_qdisc", "fq\n")
	r3.f.put(hostctl.SysctlFilePath, hostctl.SysctlFileBody)
	r3.f.put(hostctl.JournaldFilePath, hostctl.JournaldFileBody)
	r3.f.put("/proc/sys/net/ipv4/tcp_available_congestion_control", "cubic\n")
	if out = mk3().Apply(context.Background(), FixApplyBaseline, false, nil); out.Err != ErrNotApplicable {
		t.Fatalf("bbr unavailable: %+v", out)
	}
}

func TestApplyRestartInbound(t *testing.T) {
	r, mk := newFixRig(t)
	d := mk()
	out := d.Apply(context.Background(), FixRestartInbound, false, map[string]string{"inbound_id": "inb_2"})
	if !out.OK || out.Affected != 1 || strings.Join(r.restarts, ",") != "inb_2" {
		t.Fatalf("one inbound: %+v restarts=%v", out, r.restarts)
	}
	r.restarts = nil
	out = d.Apply(context.Background(), FixRestartInbound, false, nil)
	if !out.OK || out.Affected != 2 || strings.Join(r.restarts, ",") != "inb_1,inb_3" {
		t.Fatalf("all failed: %+v restarts=%v", out, r.restarts)
	}
	r.restarts = nil
	for _, bad := range []map[string]string{
		{"inbound_id": "inb_nope"},        // not one this agent runs
		{"inbound_id": "a b; rm -rf /"},   // never reaches anything
		{"inbound_id": "inb_1", "x": "y"}, // unknown parameter
		{"inbound_id": "../../etc"},
	} {
		if out = d.Apply(context.Background(), FixRestartInbound, false, bad); out.Err != ErrBadParams {
			t.Errorf("%v: %+v", bad, out)
		}
	}
	if len(r.restarts) != 0 {
		t.Fatalf("a rejected request restarted %v", r.restarts)
	}
	// Nothing failed: noop.
	d2 := mk(func(e *Env) {
		e.Inbounds = func() []Inbound { return []Inbound{{ID: "inb_2", Enabled: true, State: "running"}} }
	})
	if out = d2.Apply(context.Background(), FixRestartInbound, false, nil); !out.OK || out.Params["noop"] != "1" {
		t.Fatalf("%+v", out)
	}
	// A restart error is reported with the count that worked.
	d3 := mk(func(e *Env) {
		e.RestartInbound = func(_ context.Context, id string) (uint32, error) {
			if id == "inb_3" {
				return 0, errors.New("listen udp 9443: bind: address already in use")
			}
			return 1, nil
		}
	})
	out = d3.Apply(context.Background(), FixRestartInbound, false, nil)
	if out.OK || out.Affected != 1 || !strings.HasPrefix(out.Err, "failed: inb_3") {
		t.Fatalf("%+v", out)
	}
}

func TestApplySetResolver(t *testing.T) {
	r, mk := newFixRig(t)
	d := mk(func(e *Env) { e.Settings = func() Settings { return Settings{Country: "RU"} } })
	out := d.Apply(context.Background(), FixSetResolver, false, nil)
	if !out.OK || out.Affected != 1 || r.fx.set != 1 || strings.Join(r.fx.setTo, ",") != "77.88.8.8,77.88.8.1" {
		t.Fatalf("%+v set=%d to=%v", out, r.fx.set, r.fx.setTo)
	}
	// Already pointing there: noop and no second write.
	r.fx.plan.Before = r.fx.plan.After
	out = d.Apply(context.Background(), FixSetResolver, false, nil)
	if !out.OK || out.Affected != 0 || out.Params["noop"] != "1" || r.fx.set != 1 {
		t.Fatalf("%+v set=%d", out, r.fx.set)
	}
	r.fx.plan.Before = []string{"10.0.0.1"}
	r.fx.setErr = errors.New("read-only file system")
	if out = d.Apply(context.Background(), FixSetResolver, false, nil); out.OK || !strings.HasPrefix(out.Err, "failed: read-only") {
		t.Fatalf("%+v", out)
	}
	r.fx.planErr = hostctl.ErrUnsupported
	if out = d.Apply(context.Background(), FixSetResolver, true, nil); out.Err != ErrUnsupportedHost {
		t.Fatalf("%+v", out)
	}
}

func TestApplyGuards(t *testing.T) {
	r, mk := newFixRig(t)
	d := mk()
	if out := d.Apply(context.Background(), "rm -rf /", false, nil); out.Err != ErrUnknownFix {
		t.Errorf("unknown fix: %+v", out)
	}
	if out := d.Apply(context.Background(), "enable_ntp", false, nil); out.Err != ErrUnknownFix { // not one of the four
		t.Errorf("enable_ntp: %+v", out)
	}
	for _, fix := range []string{FixJournaldVacuum, FixApplyBaseline, FixSetResolver} {
		if out := d.Apply(context.Background(), fix, false, map[string]string{"x": "y"}); out.Err != ErrBadParams {
			t.Errorf("%s with params: %+v", fix, out)
		}
	}
	if r.fx.vacuum+r.fx.set+r.baseline != 0 {
		t.Fatal("a rejected request acted")
	}
	// Capabilities the host does not offer.
	bare := r.f.doctor()
	for _, fix := range FixIDs() {
		if out := bare.Apply(context.Background(), fix, false, nil); out.Err != ErrUnsupportedHost {
			t.Errorf("%s without actions: %+v", fix, out)
		}
	}
	off := mk(func(e *Env) { e.Unsupported = "not a Linux host" })
	if out := off.Apply(context.Background(), FixJournaldVacuum, false, nil); out.Err != ErrUnsupportedHost {
		t.Errorf("unsupported host: %+v", out)
	}
}

func TestOneFixAtATime(t *testing.T) {
	_, mk := newFixRig(t)
	in, release := make(chan struct{}), make(chan struct{})
	d := mk(func(e *Env) {
		e.RestartInbound = func(context.Context, string) (uint32, error) { close(in); <-release; return 1, nil }
	})
	done := make(chan FixOutcome)
	go func() {
		done <- d.Apply(context.Background(), FixRestartInbound, false, map[string]string{"inbound_id": "inb_2"})
	}()
	<-in
	if out := d.Apply(context.Background(), FixApplyBaseline, true, nil); out.Err != ErrFixBusy {
		t.Fatalf("second fix: %+v", out)
	}
	close(release)
	if out := <-done; !out.OK {
		t.Fatalf("first fix: %+v", out)
	}
	if out := d.Apply(context.Background(), FixApplyBaseline, true, nil); !out.OK {
		t.Fatalf("after the first: %+v", out)
	}
}

func TestRecheckIDsMatchTheChecksOfferingTheFix(t *testing.T) {
	// Every fix a check can offer is re-checked by exactly the checks listed for it.
	for fix, ids := range fixChecks {
		if len(ids) == 0 {
			t.Errorf("%s has no re-check", fix)
		}
		for _, id := range ids {
			if _, ok := lookup(id); !ok {
				t.Errorf("%s re-checks unknown check %s", fix, id)
			}
		}
	}
	if RecheckIDs("nope") != nil {
		t.Error("unknown fix has re-checks")
	}
}
