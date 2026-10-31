package agent

import (
	"context"
	"net/netip"
	"slices"
	"sync"
	"testing"

	pb "github.com/mistgate/mistgate/gen/mistgate/agent/v1"
	"github.com/mistgate/mistgate/internal/node/doctor"
	"github.com/mistgate/mistgate/internal/node/hostctl"
)

// v6Host is a fake host that can also hold IPv6 addresses (hostctl.OwnedIPv6Host).
type v6Host struct {
	*fakeHost
	rec *v6Rec
}

type v6Rec struct {
	mu      sync.Mutex
	calls   [][]netip.Addr
	state   string // state it answers for every address
	readded bool   // it says it put every address back
	cleaned int
}

func (r *v6Rec) last() []netip.Addr {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.calls) == 0 {
		return nil
	}
	return r.calls[len(r.calls)-1]
}

func (r *v6Rec) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.calls)
}

func (h v6Host) SyncOwnedIPv6(_ context.Context, wanted []netip.Addr) (hostctl.OwnedIPv6Result, error) {
	h.rec.mu.Lock()
	defer h.rec.mu.Unlock()
	h.rec.calls = append(h.rec.calls, slices.Clone(wanted))
	var res hostctl.OwnedIPv6Result
	for _, a := range wanted {
		res.States = append(res.States, hostctl.OwnedIPv6State{Addr: a, State: h.rec.state})
		if h.rec.readded {
			res.Readded = append(res.Readded, a)
		}
	}
	return res, nil
}

func (h v6Host) CleanupOwnedIPv6(context.Context) error {
	h.rec.mu.Lock()
	h.rec.cleaned++
	h.rec.mu.Unlock()
	return nil
}

func v6Harness(t *testing.T, rec *v6Rec, o harnessOpts) *harness {
	t.Helper()
	o.wrapHost = func(f *fakeHost) hostctl.Host { return v6Host{f, rec} }
	return newHarness(t, o)
}

// An agent on a host that cannot hold addresses does not list ipv6-addr/1; one that can, does.
func TestIPv6AddrCapabilityFollowsTheHost(t *testing.T) {
	plain := newHarness(t, harnessOpts{})
	if hello := <-plain.panel.hellos; contains(hello.Capabilities, capIPv6Addr) {
		t.Fatalf("a host without the interface lists %s: %v", capIPv6Addr, hello.Capabilities)
	}
	h := v6Harness(t, &v6Rec{state: hostctl.IPv6AddrOK}, harnessOpts{})
	if hello := <-h.panel.hellos; !contains(hello.Capabilities, "ipv6-addr/1") {
		t.Fatalf("capabilities: %v", hello.Capabilities)
	}
}

// The list of the desired state reaches the host, is asserted again in every sweep, and the doctor says what the host answered, in a partial report of the ipv6 check right after the change.
func TestIPv6AddrFollowsSettingsAndTheDoctorSeesIt(t *testing.T) {
	rec := &v6Rec{state: hostctl.IPv6AddrTentative}
	h := v6Harness(t, rec, harnessOpts{doctorEnv: testDoctorEnv(t)})
	h.waitConnected()

	const addr = "2001:db8:1::beef"
	mustApply(t, h.panel, &pb.DesiredState{Revision: 1, Settings: &pb.NodeSettings{Ipv6Addresses: []string{addr}}})
	if got := rec.last(); len(got) != 1 || got[0] != netip.MustParseAddr(addr) {
		t.Fatalf("the host was asked for %v", got)
	}
	r := nextDoctor(t, h)
	if !r.Partial || resultIDs(r) != doctor.CheckIPv6 || r.Results[0].Params["owned"] != addr+"=tentative" {
		t.Fatalf("report after the change: %+v", r)
	}

	// Reasserted in the sweep (30 ms in tests) without a new desired state.
	n := rec.count()
	eventually(t, func() bool { return rec.count() >= n+2 }, "the sweep asserts the addresses again")

	// The state changes (DAD ended): another partial report, with ok.
	rec.mu.Lock()
	rec.state = hostctl.IPv6AddrOK
	rec.mu.Unlock()
	r = nextDoctor(t, h)
	if resultIDs(r) != doctor.CheckIPv6 || r.Results[0].Params["owned"] != addr+"=ok" {
		t.Fatalf("report after DAD: %+v", r)
	}

	// An empty list takes them away again (the host is asked with nothing).
	mustApply(t, h.panel, &pb.DesiredState{Revision: 2, BaseRevision: 1, Settings: &pb.NodeSettings{}})
	if got := rec.last(); len(got) != 0 {
		t.Fatalf("after an empty list the host was asked for %v", got)
	}
}

// Without the panel: the saved desired state brings the addresses back when the agent starts.
func TestIPv6AddrComesBackAtStartWithoutThePanel(t *testing.T) {
	rec := &v6Rec{state: hostctl.IPv6AddrOK}
	h := v6Harness(t, rec, harnessOpts{})
	h.waitConnected()
	const addr = "2001:db8:1::beef"
	mustApply(t, h.panel, &pb.DesiredState{Revision: 1, Settings: &pb.NodeSettings{Ipv6Addresses: []string{addr}}})
	h.stop()

	rec2 := &v6Rec{state: hostctl.IPv6AddrOK}
	host := v6Host{h.host, rec2}
	cfg := Config{StateDir: h.dir, DoctorEnv: h.doctorEnv}
	a, err := New(cfg, nil, host)
	if err != nil {
		t.Fatal(err)
	}
	a.restore(context.Background())
	if got := rec2.last(); len(got) != 1 || got[0] != netip.MustParseAddr(addr) {
		t.Fatalf("restore asked the host for %v", got)
	}
}

// The journal hears at most once an hour that an address was put back.
func TestIPv6ReaddedEventIsRateLimited(t *testing.T) {
	rec := &v6Rec{state: hostctl.IPv6AddrOK, readded: true}
	h := v6Harness(t, rec, harnessOpts{})
	h.waitConnected()
	mustApply(t, h.panel, &pb.DesiredState{Revision: 1, Settings: &pb.NodeSettings{Ipv6Addresses: []string{"2001:db8:1::beef"}}})
	n := rec.count()
	eventually(t, func() bool { return rec.count() >= n+4 }, "several sweeps")
	eventually(t, func() bool { return h.panel.hasEvent("ipv6_readded") }, "the event arrives")
	count := 0
	for _, c := range h.panel.eventCodes() {
		if c == "ipv6_readded" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("%d ipv6_readded events in %d syncs", count, rec.count())
	}
}

// Only IPv6 literals without a zone are taken from the list.
func TestIPv6AddrIgnoresBadEntries(t *testing.T) {
	got := wantedIPv6(&pb.NodeSettings{Ipv6Addresses: []string{" 2001:db8:1::1 ", "10.0.0.1", "nope", "::ffff:10.0.0.1", "2001:db8:1::1%eth0"}})
	if len(got) != 1 || got[0] != netip.MustParseAddr("2001:db8:1::1") {
		t.Fatalf("%v", got)
	}
}

// Before the saved state, a HelloAck or a desired state has said anything, the agent knows nothing about the addresses and
// does not ask the host (an owned address is never removed just because the settings are still the empty default).
func TestIPv6AddrNothingIsDoneBeforeSettingsAreKnown(t *testing.T) {
	rec := &v6Rec{state: hostctl.IPv6AddrOK}
	h := v6Harness(t, rec, harnessOpts{noStart: true})
	h.a.syncOwnedIPv6(context.Background())
	if rec.count() != 0 {
		t.Fatalf("the host was asked %d time(s)", rec.count())
	}
	h.a.settings.Store(&pb.NodeSettings{})
	h.a.syncOwnedIPv6(context.Background())
	if rec.count() != 1 {
		t.Fatalf("the host was asked %d time(s) once the settings were known", rec.count())
	}
}
